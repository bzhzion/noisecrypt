package crypt

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/mlkem"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/cloudflare/circl/sign/mldsa/mldsa65"
)

// An identity carries four public keys: two for encryption and two for signing, one
// classical and one post-quantum in each pair. They are always generated, stored and
// used together. Splitting them would let a caller accidentally build a
// classical-only or lattice-only exchange, which is exactly what the hybrid design
// exists to prevent.
//
// # Why signing needs its own keys
//
// A common assumption is that one key pair does both jobs. It does not: encryption and
// signature are different algorithms over different key material, and an X25519 or
// ML-KEM key cannot produce a signature at any price. So supporting signatures means
// two more keys, and that is why the identity grew rather than gaining a flag.
//
// The cost is real and worth stating: a public identity goes from 1216 bytes to 3200,
// so the token people paste to each other goes from about 1620 characters to about
// 4270. The alternative was a second, separate signing token, which keeps the first one
// short at the price of two things to exchange and confuse. One identity won.
//
// The private half stays small, at 160 bytes, because every secret is stored as its
// seed and expanded on load rather than kept in its unpacked form.

const (
	x25519PublicKeySize  = 32
	x25519PrivateKeySize = 32

	// FingerprintSize is the length of the identity fingerprint stored in the
	// header so a receiver holding several keys knows which one to try.
	FingerprintSize = 32

	publicIdentityPrefix  = "noisecrypt-public-v1:"
	privateIdentityPrefix = "noisecrypt-secret-v1:"
)

var (
	// ErrMalformedIdentity is returned when an identity string or blob does not
	// have the exact expected shape.
	ErrMalformedIdentity = errors.New("crypt: malformed identity")

	// ErrWrongRecipient is returned when a container was sealed for a different
	// identity than the one supplied.
	ErrWrongRecipient = errors.New("crypt: container is not addressed to this identity")
)

// PublicIdentity is what a sender needs in order to seal a container, and what a
// receiver needs in order to verify who signed one.
type PublicIdentity struct {
	x25519  *ecdh.PublicKey
	mlkem   *mlkem.EncapsulationKey768
	ed25519 ed25519.PublicKey
	mldsa   *mldsa65.PublicKey
}

// PrivateIdentity is what a receiver needs in order to open a container, and what a
// sender needs in order to sign one. It embeds the matching PublicIdentity so callers
// can hand out the public half without a second object.
type PrivateIdentity struct {
	Public PublicIdentity

	x25519 *ecdh.PrivateKey
	mlkem  *mlkem.DecapsulationKey768

	// Signing secrets are kept as their seeds so the serialised private identity
	// stays at 160 bytes instead of carrying four kilobytes of expanded state.
	ed25519Seed [ed25519.SeedSize]byte
	mldsaSeed   [mldsa65.SeedSize]byte

	ed25519 ed25519.PrivateKey
	mldsa   *mldsa65.PrivateKey
}

// GenerateIdentity creates a fresh hybrid identity from the system CSPRNG.
func GenerateIdentity() (*PrivateIdentity, error) {
	xPriv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("crypt: generating X25519 key: %w", err)
	}

	kemPriv, err := mlkem.GenerateKey768()
	if err != nil {
		return nil, fmt.Errorf("crypt: generating ML-KEM-768 key: %w", err)
	}

	id := &PrivateIdentity{x25519: xPriv, mlkem: kemPriv}
	if err := fillRandom(id.ed25519Seed[:], id.mldsaSeed[:]); err != nil {
		return nil, err
	}
	id.finish(xPriv.PublicKey(), kemPriv.EncapsulationKey())
	return id, nil
}

// finish expands both signing seeds and assembles the public half.
//
// It is the single place where a PrivateIdentity becomes complete, called by both
// GenerateIdentity and ParsePrivateIdentity, so the two cannot drift apart and produce
// identities whose public and private halves disagree.
func (p *PrivateIdentity) finish(xPub *ecdh.PublicKey, kemPub *mlkem.EncapsulationKey768) {
	p.ed25519 = ed25519.NewKeyFromSeed(p.ed25519Seed[:])
	mldsaPub, mldsaPriv := mldsa65.NewKeyFromSeed(&p.mldsaSeed)
	p.mldsa = mldsaPriv

	p.Public = PublicIdentity{
		x25519:  xPub,
		mlkem:   kemPub,
		ed25519: p.ed25519.Public().(ed25519.PublicKey),
		mldsa:   mldsaPub,
	}
}

// PublicIdentitySize is the encoded length of a public identity.
const PublicIdentitySize = x25519PublicKeySize + mlkem.EncapsulationKeySize768 +
	ed25519.PublicKeySize + mldsa65.PublicKeySize

// PrivateIdentitySize is the encoded length of a private identity.
const PrivateIdentitySize = x25519PrivateKeySize + mlkem.SeedSize +
	ed25519.SeedSize + mldsa65.SeedSize

// Bytes serialises the public identity: X25519, then the ML-KEM-768 encapsulation
// key, then Ed25519, then ML-DSA-65.
//
// Fixed width, no length prefixes: every component has a size fixed by its algorithm,
// and a self-describing format here would only create room for a parser bug. The fixed
// width is also what makes a version mismatch fail loudly rather than silently, since
// an identity from a different layout cannot possibly be the right length.
func (p PublicIdentity) Bytes() []byte {
	out := make([]byte, 0, PublicIdentitySize)
	out = append(out, p.x25519.Bytes()...)
	out = append(out, p.mlkem.Bytes()...)
	out = append(out, p.ed25519...)
	out = append(out, p.mldsa.Bytes()...)
	return out
}

// Fingerprint is SHA-256 over the serialised public identity. It goes in the
// container header so a receiver with several identities can pick the right one
// without trial decapsulation.
//
// It is not a secret and not a security boundary: it tells an observer which
// identity a container is addressed to. That is a deliberate trade against making
// every receiver brute-force its own keyring on every container.
func (p PublicIdentity) Fingerprint() [FingerprintSize]byte {
	return sha256.Sum256(p.Bytes())
}

// String renders the public identity as a single prefixed, base64url token, meant
// to be pasted into a message or a config file.
func (p PublicIdentity) String() string {
	return publicIdentityPrefix + base64.RawURLEncoding.EncodeToString(p.Bytes())
}

// Short renders the fingerprint as grouped hex, for showing a human which identity is
// involved without filling their terminal.
//
// The full token is 4288 characters, which is fine to paste once and hostile to print
// on every successful decode. Sixteen hex characters is 64 bits of the fingerprint:
// enough that nobody stumbles onto a collision by accident, not enough to be a
// cryptographic commitment, which is why comparing identities in anger should still be
// done on the full value.
func (p PublicIdentity) Short() string {
	fp := p.Fingerprint()
	h := hex.EncodeToString(fp[:8])
	return fmt.Sprintf("%s-%s-%s-%s", h[0:4], h[4:8], h[8:12], h[12:16])
}

// ParsePublicIdentity is the inverse of PublicIdentity.String.
func ParsePublicIdentity(s string) (PublicIdentity, error) {
	raw, err := decodeToken(s, publicIdentityPrefix)
	if err != nil {
		return PublicIdentity{}, err
	}
	return publicIdentityFromBytes(raw)
}

// ParsePublicIdentityBytes decodes the raw form, for callers reading an identity out
// of a binary structure rather than out of a pasted token.
func ParsePublicIdentityBytes(raw []byte) (PublicIdentity, error) {
	return publicIdentityFromBytes(raw)
}

func publicIdentityFromBytes(raw []byte) (PublicIdentity, error) {
	if len(raw) != PublicIdentitySize {
		return PublicIdentity{}, fmt.Errorf("%w: public identity is %d bytes, expected %d",
			ErrMalformedIdentity, len(raw), PublicIdentitySize)
	}

	r := raw
	take := func(n int) []byte {
		out := r[:n]
		r = r[n:]
		return out
	}

	xPub, err := ecdh.X25519().NewPublicKey(take(x25519PublicKeySize))
	if err != nil {
		return PublicIdentity{}, fmt.Errorf("%w: X25519 component: %v", ErrMalformedIdentity, err)
	}

	// NewEncapsulationKey768 rejects keys that are not a valid encoding of a
	// lattice element, which is the "modulus check" FIPS 203 requires of callers.
	kemPub, err := mlkem.NewEncapsulationKey768(take(mlkem.EncapsulationKeySize768))
	if err != nil {
		return PublicIdentity{}, fmt.Errorf("%w: ML-KEM component: %v", ErrMalformedIdentity, err)
	}

	edPub := ed25519.PublicKey(bytes.Clone(take(ed25519.PublicKeySize)))

	var mldsaPub mldsa65.PublicKey
	if err := mldsaPub.UnmarshalBinary(take(mldsa65.PublicKeySize)); err != nil {
		return PublicIdentity{}, fmt.Errorf("%w: ML-DSA component: %v", ErrMalformedIdentity, err)
	}

	return PublicIdentity{x25519: xPub, mlkem: kemPub, ed25519: edPub, mldsa: &mldsaPub}, nil
}

// Bytes serialises the private identity as the X25519 scalar followed by three seeds.
//
// Seeds, not expanded keys: an ML-DSA-65 private key is 4032 bytes unpacked and 32 as a
// seed, and regenerating from the seed is deterministic. The whole private identity
// therefore fits in 160 bytes.
func (p *PrivateIdentity) Bytes() []byte {
	out := make([]byte, 0, PrivateIdentitySize)
	out = append(out, p.x25519.Bytes()...)
	out = append(out, p.mlkem.Bytes()...)
	out = append(out, p.ed25519Seed[:]...)
	out = append(out, p.mldsaSeed[:]...)
	return out
}

// String renders the private identity as a prefixed base64url token. Callers are
// responsible for storing it somewhere that is not world readable.
func (p *PrivateIdentity) String() string {
	return privateIdentityPrefix + base64.RawURLEncoding.EncodeToString(p.Bytes())
}

// ParsePrivateIdentity is the inverse of PrivateIdentity.String. It recomputes the
// public half rather than trusting a stored copy, so a tampered file cannot make a
// receiver advertise a public key that does not match its secret.
func ParsePrivateIdentity(s string) (*PrivateIdentity, error) {
	raw, err := decodeToken(s, privateIdentityPrefix)
	if err != nil {
		return nil, err
	}

	if len(raw) != PrivateIdentitySize {
		return nil, fmt.Errorf("%w: private identity is %d bytes, expected %d",
			ErrMalformedIdentity, len(raw), PrivateIdentitySize)
	}

	r := raw
	take := func(n int) []byte {
		out := r[:n]
		r = r[n:]
		return out
	}

	xPriv, err := ecdh.X25519().NewPrivateKey(take(x25519PrivateKeySize))
	if err != nil {
		return nil, fmt.Errorf("%w: X25519 component: %v", ErrMalformedIdentity, err)
	}

	kemPriv, err := mlkem.NewDecapsulationKey768(take(mlkem.SeedSize))
	if err != nil {
		return nil, fmt.Errorf("%w: ML-KEM component: %v", ErrMalformedIdentity, err)
	}

	id := &PrivateIdentity{x25519: xPriv, mlkem: kemPriv}
	copy(id.ed25519Seed[:], take(ed25519.SeedSize))
	copy(id.mldsaSeed[:], take(mldsa65.SeedSize))
	id.finish(xPriv.PublicKey(), kemPriv.EncapsulationKey())
	return id, nil
}

// ecdhX25519Generate creates an ephemeral X25519 key pair for one encapsulation.
func ecdhX25519Generate() (*ecdh.PrivateKey, error) {
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("crypt: generating ephemeral X25519 key: %w", err)
	}
	return k, nil
}

// ecdhX25519PublicKey parses a 32-byte X25519 public key.
func ecdhX25519PublicKey(b []byte) (*ecdh.PublicKey, error) {
	return ecdh.X25519().NewPublicKey(b)
}

func decodeToken(s, prefix string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, prefix) {
		return nil, fmt.Errorf("%w: expected the %q prefix", ErrMalformedIdentity, prefix)
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(s, prefix))
	if err != nil {
		return nil, fmt.Errorf("%w: base64: %v", ErrMalformedIdentity, err)
	}
	return raw, nil
}
