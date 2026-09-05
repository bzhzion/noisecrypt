package crypt

import (
	"crypto/cipher"
	"encoding/binary"
	"errors"
	"fmt"
	"math"

	"golang.org/x/crypto/chacha20poly1305"
)

// TagSize is the authentication tag appended to every sealed chunk.
const TagSize = chacha20poly1305.Overhead

// maxChunks bounds the counter so it cannot wrap. A wrapped counter would reuse a
// nonce under the same key, which for a stream cipher leaks the XOR of two
// plaintexts. At the default 64 KiB chunk this ceiling sits at 256 TiB, far past
// anything this format will carry.
const maxChunks = 1 << 32

var (
	// ErrAuthentication is returned when a chunk fails to authenticate: wrong key,
	// wrong passphrase, wrong recipient, or tampered ciphertext. The four cases are
	// deliberately indistinguishable to a caller.
	ErrAuthentication = errors.New("crypt: authentication failed")

	// ErrTruncated is returned when the stream ends without a chunk carrying the
	// end-of-stream marker.
	ErrTruncated = errors.New("crypt: stream ends without an end-of-stream marker")

	// ErrTooLarge is returned when a plaintext would need more chunks than the
	// counter can address.
	ErrTooLarge = errors.New("crypt: plaintext exceeds the addressable chunk count")
)

// SealOptions configures a sealing operation. Exactly one of Passphrase and
// Recipient must be set.
type SealOptions struct {
	// Passphrase selects ModePassphrase.
	Passphrase []byte

	// Recipient selects ModeHybrid.
	Recipient *PublicIdentity

	// KDF overrides the Argon2id cost in passphrase mode. Ignored otherwise.
	KDF KDFParams

	// ChunkSize overrides DefaultChunkSize.
	ChunkSize uint32
}

func (o SealOptions) validate() error {
	switch {
	case len(o.Passphrase) == 0 && o.Recipient == nil:
		return errors.New("crypt: seal needs either a passphrase or a recipient")
	case len(o.Passphrase) > 0 && o.Recipient != nil:
		return errors.New("crypt: seal takes a passphrase or a recipient, not both")
	}
	if o.ChunkSize != 0 && (o.ChunkSize < minChunkSize || o.ChunkSize > maxChunkSize) {
		return fmt.Errorf("crypt: chunk size %d is outside [%d, %d]", o.ChunkSize, minChunkSize, maxChunkSize)
	}
	return nil
}

// OpenOptions configures an opening operation. Exactly one of Passphrase and
// Identity must be set, matching the mode the header announces.
type OpenOptions struct {
	Passphrase []byte
	Identity   *PrivateIdentity
}

// Seal encrypts plaintext and returns the complete container: header followed by
// length-prefixed sealed chunks.
//
// This is the whole-buffer convenience form. It is the right shape for NoiseCrypt
// because the erasure and modulation layers above need the full ciphertext in
// memory anyway to interleave it across frames.
func Seal(plaintext []byte, opts SealOptions) ([]byte, error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}

	chunkSize := opts.ChunkSize
	if chunkSize == 0 {
		chunkSize = DefaultChunkSize
	}

	var (
		header *Header
		master []byte
		err    error
	)
	if opts.Recipient != nil {
		header, master, err = newHybridHeader(*opts.Recipient, chunkSize)
	} else {
		header, master, err = newPassphraseHeader(opts.Passphrase, chunkSize, opts.KDF)
	}
	if err != nil {
		return nil, err
	}

	w, err := newChunkWriter(header, master)
	if err != nil {
		return nil, err
	}

	encodedHeader, err := header.MarshalBinary()
	if err != nil {
		return nil, err
	}

	// An empty plaintext still produces exactly one chunk, the final one. Without
	// it there would be no end-of-stream marker and Open could not tell an empty
	// message from a message truncated to nothing.
	nChunks := (len(plaintext) + int(chunkSize) - 1) / int(chunkSize)
	if nChunks == 0 {
		nChunks = 1
	}
	if nChunks > maxChunks {
		return nil, ErrTooLarge
	}

	out := make([]byte, 0, len(encodedHeader)+len(plaintext)+nChunks*(4+TagSize))
	out = append(out, encodedHeader...)

	for i := 0; i < nChunks; i++ {
		start := i * int(chunkSize)
		end := min(start+int(chunkSize), len(plaintext))
		final := i == nChunks-1

		sealed := w.seal(plaintext[start:end], uint32(i), final)
		out = binary.BigEndian.AppendUint32(out, uint32(len(sealed)))
		out = append(out, sealed...)
	}

	return out, nil
}

// Open decrypts a container produced by Seal.
func Open(container []byte, opts OpenOptions) ([]byte, error) {
	header, consumed, err := ParseHeader(container)
	if err != nil {
		return nil, err
	}

	master, err := masterKeyFor(header, opts)
	if err != nil {
		return nil, err
	}

	r, err := newChunkWriter(header, master)
	if err != nil {
		return nil, err
	}

	body := container[consumed:]
	var (
		out      []byte
		index    uint32
		sawFinal bool
	)

	for len(body) > 0 {
		if len(body) < 4 {
			return nil, fmt.Errorf("%w: %d trailing bytes are not a chunk length", ErrTruncated, len(body))
		}
		length := binary.BigEndian.Uint32(body[:4])
		if length < TagSize {
			return nil, fmt.Errorf("%w: chunk %d declares %d bytes, below the %d-byte tag",
				ErrMalformedHeader, index, length, TagSize)
		}
		// Bound the declared length against what is actually present before slicing,
		// and against the header's own chunk size so a hostile length cannot make us
		// reserve an arbitrary buffer.
		if uint64(length) > uint64(header.ChunkSize)+TagSize {
			return nil, fmt.Errorf("%w: chunk %d declares %d bytes, above the %d-byte maximum",
				ErrMalformedHeader, index, length, header.ChunkSize+TagSize)
		}
		if uint64(len(body)-4) < uint64(length) {
			return nil, fmt.Errorf("%w: chunk %d declares %d bytes, %d are present",
				ErrTruncated, index, length, len(body)-4)
		}
		sealed := body[4 : 4+length]
		body = body[4+length:]

		// Whether this is the final chunk is bound into the nonce, so we cannot know
		// it in advance: we try the non-final interpretation first and fall back. A
		// wrong guess simply fails to authenticate, which is why this is safe rather
		// than an oracle.
		plain, err := r.open(sealed, index, false)
		if err != nil {
			plain, err = r.open(sealed, index, true)
			if err != nil {
				return nil, fmt.Errorf("%w: chunk %d", ErrAuthentication, index)
			}
			sawFinal = true
		}
		out = append(out, plain...)

		if sawFinal {
			if len(body) != 0 {
				// Data after the end-of-stream marker means someone appended chunks
				// from another message, or the same message replayed. Refuse.
				return nil, fmt.Errorf("%w: %d bytes follow the end-of-stream chunk", ErrAuthentication, len(body))
			}
			return out, nil
		}

		if index == math.MaxUint32 {
			// The next chunk would wrap the counter and reuse a nonce. Refuse
			// rather than let the loop continue.
			return nil, ErrTooLarge
		}
		index++
	}

	return nil, ErrTruncated
}

func masterKeyFor(header *Header, opts OpenOptions) ([]byte, error) {
	switch header.Mode {
	case ModePassphrase:
		if len(opts.Passphrase) == 0 {
			return nil, errors.New("crypt: container is passphrase sealed, no passphrase supplied")
		}
		return header.passphraseMasterKey(opts.Passphrase), nil
	case ModeHybrid:
		if opts.Identity == nil {
			return nil, errors.New("crypt: container is sealed to an identity, no private identity supplied")
		}
		return header.openHybridHeader(opts.Identity)
	default:
		return nil, fmt.Errorf("%w: mode %d", ErrUnsupported, header.Mode)
	}
}

// streamCipher holds the AEAD and the per-stream constants shared by every chunk.
type streamCipher struct {
	aead        cipher.AEAD
	noncePrefix [noncePrefixSize]byte
	headerHash  [32]byte
}

func newChunkWriter(h *Header, master []byte) (*streamCipher, error) {
	key, err := h.streamKey(master)
	if err != nil {
		return nil, err
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("crypt: initialising XChaCha20-Poly1305: %w", err)
	}
	hh, err := h.headerHash()
	if err != nil {
		return nil, err
	}
	sc := &streamCipher{aead: aead, headerHash: hh}
	copy(sc.noncePrefix[:], h.noncePrefix[:])
	return sc, nil
}

// nonce builds the STREAM nonce: a per-message random prefix, the chunk counter,
// and a final-chunk flag.
//
// The counter is what stops chunks being reordered or dropped from the middle; the
// flag is what stops the stream being truncated, because the last chunk of a
// truncated stream would have to authenticate under a nonce it was never sealed
// with. Both live in the nonce rather than only in the associated data so that even
// an implementation that ignored the associated data would keep those properties.
func (s *streamCipher) nonce(index uint32, final bool) []byte {
	var n [chacha20poly1305.NonceSizeX]byte
	copy(n[:noncePrefixSize], s.noncePrefix[:])
	binary.BigEndian.PutUint32(n[noncePrefixSize:noncePrefixSize+4], index)
	if final {
		n[noncePrefixSize+4] = 1
	}
	return n[:]
}

// associatedData binds the header and the chunk position to the tag. The header
// hash is the part that matters: without it, a container's header could be swapped
// for another with the same derived key, and the chunk index alone would not notice.
func (s *streamCipher) associatedData(index uint32, final bool) []byte {
	ad := make([]byte, 0, len(s.headerHash)+5)
	ad = append(ad, s.headerHash[:]...)
	ad = binary.BigEndian.AppendUint32(ad, index)
	if final {
		ad = append(ad, 1)
	} else {
		ad = append(ad, 0)
	}
	return ad
}

func (s *streamCipher) seal(plaintext []byte, index uint32, final bool) []byte {
	return s.aead.Seal(nil, s.nonce(index, final), plaintext, s.associatedData(index, final))
}

func (s *streamCipher) open(sealed []byte, index uint32, final bool) ([]byte, error) {
	return s.aead.Open(nil, s.nonce(index, final), sealed, s.associatedData(index, final))
}
