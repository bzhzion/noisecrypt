package crypt

import (
	"crypto/ecdh"
	"crypto/mlkem"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// A recipient identity is the pair of public keys a sender needs: the classical
// X25519 one and the lattice ML-KEM-768 one. They are always generated, stored and
// used together. Splitting them would let a caller accidentally build a
// classical-only or lattice-only exchange, which is exactly what the hybrid design
// exists to prevent.

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

// PublicIdentity is what a sender needs in order to seal a container.
type PublicIdentity struct {
	x25519 *ecdh.PublicKey
	mlkem  *mlkem.EncapsulationKey768
}

// PrivateIdentity is what a receiver needs in order to open a container. It
// embeds the matching PublicIdentity so callers can hand out the public half
// without a second object.
type PrivateIdentity struct {
	Public PublicIdentity

	x25519 *ecdh.PrivateKey
	mlkem  *mlkem.DecapsulationKey768
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

	return &PrivateIdentity{
		Public: PublicIdentity{
			x25519: xPriv.PublicKey(),
			mlkem:  kemPriv.EncapsulationKey(),
		},
		x25519: xPriv,
		mlkem:  kemPriv,
	}, nil
}

// Bytes serialises the public identity as X25519 public key followed by the
// ML-KEM-768 encapsulation key. Fixed width, no length prefixes: both components
// have a size fixed by their algorithm, and a self-describing format here would
// only create room for a parser bug.
func (p PublicIdentity) Bytes() []byte {
	out := make([]byte, 0, x25519PublicKeySize+mlkem.EncapsulationKeySize768)
	out = append(out, p.x25519.Bytes()...)
	out = append(out, p.mlkem.Bytes()...)
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

// ParsePublicIdentity is the inverse of PublicIdentity.String.
func ParsePublicIdentity(s string) (PublicIdentity, error) {
	raw, err := decodeToken(s, publicIdentityPrefix)
	if err != nil {
		return PublicIdentity{}, err
	}
	return publicIdentityFromBytes(raw)
}

func publicIdentityFromBytes(raw []byte) (PublicIdentity, error) {
	want := x25519PublicKeySize + mlkem.EncapsulationKeySize768
	if len(raw) != want {
		return PublicIdentity{}, fmt.Errorf("%w: public identity is %d bytes, expected %d",
			ErrMalformedIdentity, len(raw), want)
	}

	xPub, err := ecdh.X25519().NewPublicKey(raw[:x25519PublicKeySize])
	if err != nil {
		return PublicIdentity{}, fmt.Errorf("%w: X25519 component: %v", ErrMalformedIdentity, err)
	}

	// NewEncapsulationKey768 rejects keys that are not a valid encoding of a
	// lattice element, which is the "modulus check" FIPS 203 requires of callers.
	kemPub, err := mlkem.NewEncapsulationKey768(raw[x25519PublicKeySize:])
	if err != nil {
		return PublicIdentity{}, fmt.Errorf("%w: ML-KEM component: %v", ErrMalformedIdentity, err)
	}

	return PublicIdentity{x25519: xPub, mlkem: kemPub}, nil
}

// Bytes serialises the private identity as X25519 scalar followed by the ML-KEM
// seed. The seed, not the expanded key: it is 64 bytes instead of 2400 and
// regenerating from it is deterministic.
func (p *PrivateIdentity) Bytes() []byte {
	out := make([]byte, 0, x25519PrivateKeySize+mlkem.SeedSize)
	out = append(out, p.x25519.Bytes()...)
	out = append(out, p.mlkem.Bytes()...)
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

	want := x25519PrivateKeySize + mlkem.SeedSize
	if len(raw) != want {
		return nil, fmt.Errorf("%w: private identity is %d bytes, expected %d",
			ErrMalformedIdentity, len(raw), want)
	}

	xPriv, err := ecdh.X25519().NewPrivateKey(raw[:x25519PrivateKeySize])
	if err != nil {
		return nil, fmt.Errorf("%w: X25519 component: %v", ErrMalformedIdentity, err)
	}

	kemPriv, err := mlkem.NewDecapsulationKey768(raw[x25519PrivateKeySize:])
	if err != nil {
		return nil, fmt.Errorf("%w: ML-KEM component: %v", ErrMalformedIdentity, err)
	}

	return &PrivateIdentity{
		Public: PublicIdentity{
			x25519: xPriv.PublicKey(),
			mlkem:  kemPriv.EncapsulationKey(),
		},
		x25519: xPriv,
		mlkem:  kemPriv,
	}, nil
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
