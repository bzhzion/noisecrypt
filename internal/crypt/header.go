package crypt

import (
	"crypto/hkdf"
	"crypto/mlkem"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"

	"golang.org/x/crypto/argon2"
)

// Magic identifies a NoiseCrypt v1 encrypted stream.
const Magic = "NCR1"

// Version is the format version this package reads and writes.
const Version = 1

// Mode selects how the stream key is established.
type Mode uint8

const (
	// ModePassphrase derives the key from a passphrase with Argon2id. Symmetric
	// only, therefore already post-quantum: Grover halves the effective key
	// length, and 256 bits halved is still 128 bits.
	ModePassphrase Mode = 1

	// ModeHybrid derives the key from an X25519 + ML-KEM-768 key encapsulation
	// addressed to a recipient identity.
	ModeHybrid Mode = 2
)

// Suite selects the AEAD and KDF. There is exactly one for now; the field exists
// so a future suite can be added without a format version bump, and so a decoder
// refuses a suite it does not implement instead of silently misreading bytes.
type Suite uint8

// SuiteXChaCha20Poly1305 is XChaCha20-Poly1305 for the AEAD and HKDF-SHA-512 for
// key derivation.
const SuiteXChaCha20Poly1305 Suite = 1

const (
	noncePrefixSize = 19 // 24-byte XChaCha nonce, minus 4 counter bytes and 1 final flag
	argonSaltSize   = 16

	headerCommonSize     = 4 + 1 + 1 + 1 + 1 + 4 + noncePrefixSize
	headerPassphraseSize = headerCommonSize + argonSaltSize + 4 + 4 + 1
	headerHybridSize     = headerCommonSize + FingerprintSize + x25519PublicKeySize + mlkem.CiphertextSize768
)

// DefaultChunkSize is the plaintext size of one STREAM chunk.
//
// The trade is between overhead and blast radius. Every chunk costs a 16-byte
// authentication tag, so small chunks waste bandwidth on a channel where bandwidth
// is already the binding constraint. Large chunks mean a single unrecoverable chunk
// destroys more plaintext. 64 KiB puts the tag overhead at 0.02 percent while
// keeping the damage from one lost chunk to something the erasure layer above can
// usually repair.
const DefaultChunkSize = 64 * 1024

const (
	minChunkSize = 1024
	maxChunkSize = 16 * 1024 * 1024
)

// Argon2id defaults. Tuned for a desktop machine sealing an archive it expects to
// survive for years, not for a login form: three passes over 128 MiB takes on the
// order of a second and puts a GPU cracking rig at a serious disadvantage.
const (
	DefaultArgonTime   uint32 = 3
	DefaultArgonMemory uint32 = 128 * 1024 // KiB
	DefaultArgonLanes  uint8  = 4
)

// maxArgonMemory and maxArgonTime cap what a *parsed* header may ask us to spend.
//
// Both fields come from the container, so they are attacker controlled, and they
// are read before any authentication has happened: there is no way to verify them
// first, because verifying requires the key they produce. Without ceilings, a
// four-byte edit turns every decode attempt into an out-of-memory kill (memory) or
// an unbounded hang (time). This was not theoretical, it showed up as a test suite
// that took three minutes because a bit flip in the time field asked for sixteen
// million passes.
//
// These are denial-of-service bounds, not security bounds. They are generous enough
// that no honest producer reaches them; a caller wanting more work should raise the
// memory, which is what actually costs an attacker money.
const (
	maxArgonMemory uint32 = 2 * 1024 * 1024 // 2 GiB expressed in KiB
	maxArgonTime   uint32 = 16
)

var (
	// ErrMalformedHeader is returned when a header cannot be parsed at all.
	ErrMalformedHeader = errors.New("crypt: malformed header")

	// ErrUnsupported is returned when the header is well formed but announces a
	// version, mode or suite this build does not implement.
	ErrUnsupported = errors.New("crypt: unsupported container")
)

// Header is the cleartext preamble of an encrypted stream.
//
// It is authenticated but not confidential: its SHA-256 is bound into the
// associated data of every chunk, so tampering with any field makes every chunk
// fail to open, but an observer can read it. It carries no file metadata by
// design; name, size and modification time belong to the encrypted payload.
type Header struct {
	Mode      Mode
	Suite     Suite
	ChunkSize uint32

	noncePrefix [noncePrefixSize]byte

	// Passphrase mode.
	argonSalt   [argonSaltSize]byte
	argonTime   uint32
	argonMemory uint32
	argonLanes  uint8

	// Hybrid mode.
	recipient    [FingerprintSize]byte
	ephemeralPub [x25519PublicKeySize]byte
	kemCipher    []byte
}

// Size returns the encoded length of the header.
func (h *Header) Size() int {
	switch h.Mode {
	case ModePassphrase:
		return headerPassphraseSize
	case ModeHybrid:
		return headerHybridSize
	default:
		return 0
	}
}

// Recipient returns the fingerprint of the identity a hybrid container is
// addressed to. It is the zero value in passphrase mode.
func (h *Header) Recipient() [FingerprintSize]byte { return h.recipient }

// MarshalBinary encodes the header.
func (h *Header) MarshalBinary() ([]byte, error) {
	size := h.Size()
	if size == 0 {
		return nil, fmt.Errorf("%w: unknown mode %d", ErrUnsupported, h.Mode)
	}

	buf := make([]byte, 0, size)
	buf = append(buf, Magic...)
	buf = append(buf, Version, byte(h.Mode), byte(h.Suite), 0 /* flags, reserved */)
	buf = binary.BigEndian.AppendUint32(buf, h.ChunkSize)
	buf = append(buf, h.noncePrefix[:]...)

	switch h.Mode {
	case ModePassphrase:
		buf = append(buf, h.argonSalt[:]...)
		buf = binary.BigEndian.AppendUint32(buf, h.argonTime)
		buf = binary.BigEndian.AppendUint32(buf, h.argonMemory)
		buf = append(buf, h.argonLanes)
	case ModeHybrid:
		buf = append(buf, h.recipient[:]...)
		buf = append(buf, h.ephemeralPub[:]...)
		buf = append(buf, h.kemCipher...)
	}

	if len(buf) != size {
		// Unreachable unless the layout constants and this function drift apart.
		return nil, fmt.Errorf("crypt: internal header size mismatch: built %d, expected %d", len(buf), size)
	}
	return buf, nil
}

// ParseHeader decodes a header from the front of b and returns it along with the
// number of bytes consumed.
//
// Every bound is checked before it is used to size an allocation. A header is the
// one part of the format an attacker fully controls before any authentication has
// happened, so it is parsed defensively on purpose.
func ParseHeader(b []byte) (*Header, int, error) {
	if len(b) < headerCommonSize {
		return nil, 0, fmt.Errorf("%w: %d bytes is shorter than the common prefix", ErrMalformedHeader, len(b))
	}
	if string(b[:4]) != Magic {
		return nil, 0, fmt.Errorf("%w: bad magic", ErrMalformedHeader)
	}
	if b[4] != Version {
		return nil, 0, fmt.Errorf("%w: format version %d, this build reads %d", ErrUnsupported, b[4], Version)
	}

	h := &Header{
		Mode:  Mode(b[5]),
		Suite: Suite(b[6]),
	}
	if b[7] != 0 {
		return nil, 0, fmt.Errorf("%w: reserved flags byte is %#x, expected 0", ErrUnsupported, b[7])
	}
	if h.Suite != SuiteXChaCha20Poly1305 {
		return nil, 0, fmt.Errorf("%w: cipher suite %d", ErrUnsupported, h.Suite)
	}

	h.ChunkSize = binary.BigEndian.Uint32(b[8:12])
	if h.ChunkSize < minChunkSize || h.ChunkSize > maxChunkSize {
		return nil, 0, fmt.Errorf("%w: chunk size %d is outside [%d, %d]",
			ErrMalformedHeader, h.ChunkSize, minChunkSize, maxChunkSize)
	}
	copy(h.noncePrefix[:], b[12:headerCommonSize])

	switch h.Mode {
	case ModePassphrase:
		if len(b) < headerPassphraseSize {
			return nil, 0, fmt.Errorf("%w: truncated passphrase header", ErrMalformedHeader)
		}
		p := b[headerCommonSize:]
		copy(h.argonSalt[:], p[:argonSaltSize])
		h.argonTime = binary.BigEndian.Uint32(p[argonSaltSize : argonSaltSize+4])
		h.argonMemory = binary.BigEndian.Uint32(p[argonSaltSize+4 : argonSaltSize+8])
		h.argonLanes = p[argonSaltSize+8]

		if h.argonTime == 0 || h.argonLanes == 0 {
			return nil, 0, fmt.Errorf("%w: Argon2id time and lanes must be non-zero", ErrMalformedHeader)
		}
		if h.argonTime > maxArgonTime {
			return nil, 0, fmt.Errorf("%w: header asks for %d Argon2id passes, refusing above %d",
				ErrMalformedHeader, h.argonTime, maxArgonTime)
		}
		if h.argonMemory < 8*uint32(h.argonLanes) {
			return nil, 0, fmt.Errorf("%w: Argon2id memory %d KiB is below the %d KiB minimum for %d lanes",
				ErrMalformedHeader, h.argonMemory, 8*uint32(h.argonLanes), h.argonLanes)
		}
		if h.argonMemory > maxArgonMemory {
			return nil, 0, fmt.Errorf("%w: header asks for %d KiB of Argon2id memory, refusing above %d KiB",
				ErrMalformedHeader, h.argonMemory, maxArgonMemory)
		}
		return h, headerPassphraseSize, nil

	case ModeHybrid:
		if len(b) < headerHybridSize {
			return nil, 0, fmt.Errorf("%w: truncated hybrid header", ErrMalformedHeader)
		}
		p := b[headerCommonSize:]
		copy(h.recipient[:], p[:FingerprintSize])
		copy(h.ephemeralPub[:], p[FingerprintSize:FingerprintSize+x25519PublicKeySize])
		h.kemCipher = make([]byte, mlkem.CiphertextSize768)
		copy(h.kemCipher, p[FingerprintSize+x25519PublicKeySize:])
		return h, headerHybridSize, nil

	default:
		return nil, 0, fmt.Errorf("%w: mode %d", ErrUnsupported, h.Mode)
	}
}

// KDFParams lets a caller override the Argon2id cost. Zero fields take the default.
type KDFParams struct {
	Time   uint32
	Memory uint32 // KiB
	Lanes  uint8
}

func (p KDFParams) withDefaults() KDFParams {
	if p.Time == 0 {
		p.Time = DefaultArgonTime
	}
	if p.Memory == 0 {
		p.Memory = DefaultArgonMemory
	}
	if p.Lanes == 0 {
		p.Lanes = DefaultArgonLanes
	}
	return p
}

// newPassphraseHeader builds a header and the matching master key for a fresh
// passphrase-sealed stream.
func newPassphraseHeader(passphrase []byte, chunkSize uint32, kdf KDFParams) (*Header, []byte, error) {
	kdf = kdf.withDefaults()
	if kdf.Memory > maxArgonMemory {
		return nil, nil, fmt.Errorf("crypt: Argon2id memory %d KiB exceeds the %d KiB ceiling", kdf.Memory, maxArgonMemory)
	}
	if kdf.Time > maxArgonTime {
		return nil, nil, fmt.Errorf("crypt: Argon2id time %d exceeds the ceiling of %d passes", kdf.Time, maxArgonTime)
	}

	h := &Header{
		Mode:        ModePassphrase,
		Suite:       SuiteXChaCha20Poly1305,
		ChunkSize:   chunkSize,
		argonTime:   kdf.Time,
		argonMemory: kdf.Memory,
		argonLanes:  kdf.Lanes,
	}
	if err := fillRandom(h.noncePrefix[:], h.argonSalt[:]); err != nil {
		return nil, nil, err
	}

	return h, h.passphraseMasterKey(passphrase), nil
}

func (h *Header) passphraseMasterKey(passphrase []byte) []byte {
	return argon2.IDKey(passphrase, h.argonSalt[:], h.argonTime, h.argonMemory, h.argonLanes, 32)
}

// newHybridHeader runs the hybrid key encapsulation against a recipient identity
// and returns the header plus the master key.
func newHybridHeader(to PublicIdentity, chunkSize uint32) (*Header, []byte, error) {
	ephPriv, err := ecdhX25519Generate()
	if err != nil {
		return nil, nil, err
	}

	// Classical half.
	classical, err := ephPriv.ECDH(to.x25519)
	if err != nil {
		return nil, nil, fmt.Errorf("crypt: X25519 exchange: %w", err)
	}

	// Lattice half. Encapsulate returns the shared key first, ciphertext second.
	lattice, kemCipher := to.mlkem.Encapsulate()

	h := &Header{
		Mode:      ModeHybrid,
		Suite:     SuiteXChaCha20Poly1305,
		ChunkSize: chunkSize,
		recipient: to.Fingerprint(),
		kemCipher: kemCipher,
	}
	copy(h.ephemeralPub[:], ephPriv.PublicKey().Bytes())
	if err := fillRandom(h.noncePrefix[:]); err != nil {
		return nil, nil, err
	}

	master, err := hybridMasterKey(classical, lattice, to.Bytes(), h.ephemeralPub[:], kemCipher)
	if err != nil {
		return nil, nil, err
	}
	return h, master, nil
}

// openHybridHeader recovers the master key from a hybrid header using the
// receiver's private identity.
func (h *Header) openHybridHeader(id *PrivateIdentity) ([]byte, error) {
	want := id.Public.Fingerprint()
	if subtle.ConstantTimeCompare(want[:], h.recipient[:]) != 1 {
		return nil, ErrWrongRecipient
	}

	ephPub, err := ecdhX25519PublicKey(h.ephemeralPub[:])
	if err != nil {
		return nil, fmt.Errorf("%w: ephemeral X25519 key: %v", ErrMalformedHeader, err)
	}
	classical, err := id.x25519.ECDH(ephPub)
	if err != nil {
		return nil, fmt.Errorf("crypt: X25519 exchange: %w", err)
	}

	// ML-KEM decapsulation never reports "wrong ciphertext": by design it returns
	// a deterministic pseudo-random key instead, so an attacker learns nothing from
	// timing or error behaviour. A mismatch therefore surfaces later, as an AEAD
	// authentication failure on the first chunk. That is the intended shape.
	lattice, err := id.mlkem.Decapsulate(h.kemCipher)
	if err != nil {
		return nil, fmt.Errorf("%w: ML-KEM ciphertext: %v", ErrMalformedHeader, err)
	}

	return hybridMasterKey(classical, lattice, id.Public.Bytes(), h.ephemeralPub[:], h.kemCipher)
}

// hybridMasterKey combines the two shared secrets.
//
// Both secrets go into the HKDF input keying material and the full transcript goes
// into the salt. Concatenating the secrets is what makes the construction robust:
// HKDF-Extract over the pair is a secure PRF as long as either half is unpredictable,
// so breaking X25519 alone or ML-KEM alone does not reveal the key.
//
// Binding the transcript (recipient identity, ephemeral public key, KEM ciphertext)
// stops an attacker who can rewrite header bytes from steering two parties onto
// different keys, and gives the exchange contributory behaviour it would not have
// from the raw secrets alone.
func hybridMasterKey(classical, lattice, recipientPub, ephemeralPub, kemCipher []byte) ([]byte, error) {
	ikm := make([]byte, 0, len(classical)+len(lattice))
	ikm = append(ikm, classical...)
	ikm = append(ikm, lattice...)

	transcript := sha512.New()
	transcript.Write([]byte("noisecrypt/v1 hybrid transcript"))
	transcript.Write(recipientPub)
	transcript.Write(ephemeralPub)
	transcript.Write(kemCipher)

	master, err := hkdf.Key(sha512.New, ikm, transcript.Sum(nil), "noisecrypt/v1 hybrid master", 32)
	if err != nil {
		return nil, fmt.Errorf("crypt: deriving hybrid master key: %w", err)
	}
	return master, nil
}

// streamKey derives the AEAD key from the master key, binding the whole header.
//
// The header hash is the HKDF salt, which is what makes header tampering fatal:
// flipping a bit in the chunk size, the mode byte or the KEM ciphertext changes the
// derived key, so every chunk fails to authenticate rather than being decrypted
// under attacker-chosen parameters.
func (h *Header) streamKey(master []byte) ([]byte, error) {
	encoded, err := h.MarshalBinary()
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(encoded)

	key, err := hkdf.Key(sha512.New, master, sum[:], "noisecrypt/v1 stream key", 32)
	if err != nil {
		return nil, fmt.Errorf("crypt: deriving stream key: %w", err)
	}
	return key, nil
}

// headerHash returns the SHA-256 of the encoded header, used as associated data.
func (h *Header) headerHash() ([32]byte, error) {
	encoded, err := h.MarshalBinary()
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(encoded), nil
}

func fillRandom(slices ...[]byte) error {
	for _, s := range slices {
		if _, err := rand.Read(s); err != nil {
			return fmt.Errorf("crypt: reading system randomness: %w", err)
		}
	}
	return nil
}
