package crypt

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"testing"
)

// cheapKDF keeps Argon2id honest but fast enough to run in a test suite. Never use
// these parameters for a real container.
var cheapKDF = KDFParams{Time: 1, Memory: 8, Lanes: 1}

func testSizes(chunk uint32) []int {
	c := int(chunk)
	return []int{0, 1, 17, c - 1, c, c + 1, 2*c - 1, 2 * c, 3*c + 5}
}

func randomBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("reading randomness: %v", err)
	}
	return b
}

func TestRoundTripPassphrase(t *testing.T) {
	const chunk = 4096
	pass := []byte("correct horse battery staple")

	for _, size := range testSizes(chunk) {
		t.Run(fmt.Sprintf("%d bytes", size), func(t *testing.T) {
			plain := randomBytes(t, size)

			sealed, err := Seal(plain, SealOptions{Passphrase: pass, KDF: cheapKDF, ChunkSize: chunk})
			if err != nil {
				t.Fatalf("Seal: %v", err)
			}

			got, err := Open(sealed, OpenOptions{Passphrase: pass})
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			if !bytes.Equal(got, plain) {
				t.Fatalf("round trip mismatch: got %d bytes, want %d", len(got), len(plain))
			}
		})
	}
}

func TestRoundTripHybrid(t *testing.T) {
	const chunk = 4096

	id, err := GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}

	for _, size := range testSizes(chunk) {
		t.Run(fmt.Sprintf("%d bytes", size), func(t *testing.T) {
			plain := randomBytes(t, size)

			sealed, err := Seal(plain, SealOptions{Recipient: &id.Public, ChunkSize: chunk})
			if err != nil {
				t.Fatalf("Seal: %v", err)
			}

			got, err := Open(sealed, OpenOptions{Identity: id})
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			if !bytes.Equal(got, plain) {
				t.Fatalf("round trip mismatch: got %d bytes, want %d", len(got), len(plain))
			}
		})
	}
}

func TestWrongPassphraseFails(t *testing.T) {
	sealed, err := Seal([]byte("secret"), SealOptions{Passphrase: []byte("right"), KDF: cheapKDF})
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	if _, err := Open(sealed, OpenOptions{Passphrase: []byte("wrong")}); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("expected ErrAuthentication, got %v", err)
	}
}

func TestWrongRecipientFails(t *testing.T) {
	alice, err := GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	bob, err := GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}

	sealed, err := Seal([]byte("for alice only"), SealOptions{Recipient: &alice.Public})
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	if _, err := Open(sealed, OpenOptions{Identity: bob}); !errors.Is(err, ErrWrongRecipient) {
		t.Fatalf("expected ErrWrongRecipient, got %v", err)
	}
}

// TestWrongRecipientWithForgedFingerprintFails covers the case the fingerprint
// shortcut would otherwise hide: an attacker rewrites the recipient fingerprint so
// Bob's client agrees to try. Decapsulation must then fail closed at the AEAD, not
// produce plaintext.
func TestWrongRecipientWithForgedFingerprintFails(t *testing.T) {
	alice, err := GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	bob, err := GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}

	sealed, err := Seal([]byte("for alice only"), SealOptions{Recipient: &alice.Public})
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	forged := bytes.Clone(sealed)
	bobPrint := bob.Public.Fingerprint()
	copy(forged[headerCommonSize:headerCommonSize+FingerprintSize], bobPrint[:])

	if _, err := Open(forged, OpenOptions{Identity: bob}); err == nil {
		t.Fatal("a container with a forged recipient fingerprint opened successfully")
	}
}

// TestEveryByteIsAuthenticated flips one bit in every byte of a container, one at a
// time, and requires that none of the results opens. This is the property the whole
// design rests on: there is no byte an attacker can change for free.
func TestEveryByteIsAuthenticated(t *testing.T) {
	pass := []byte("passphrase")
	plain := randomBytes(t, 3000)

	sealed, err := Seal(plain, SealOptions{Passphrase: pass, KDF: cheapKDF, ChunkSize: 1024})
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	for i := range sealed {
		tampered := bytes.Clone(sealed)
		tampered[i] ^= 0x01

		got, err := Open(tampered, OpenOptions{Passphrase: pass})
		if err == nil {
			t.Fatalf("byte %d: tampered container opened and returned %d bytes", i, len(got))
		}
	}
}

func TestTruncationIsDetected(t *testing.T) {
	pass := []byte("passphrase")
	plain := randomBytes(t, 5000)

	sealed, err := Seal(plain, SealOptions{Passphrase: pass, KDF: cheapKDF, ChunkSize: 1024})
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	header, consumed, err := ParseHeader(sealed)
	if err != nil {
		t.Fatalf("ParseHeader: %v", err)
	}
	chunkLen := 4 + int(header.ChunkSize) + TagSize

	// Drop the final chunk. The remaining chunks are individually valid, which is
	// exactly why a format without an end-of-stream marker would silently accept a
	// short plaintext here.
	truncated := sealed[:consumed+4*chunkLen]

	if _, err := Open(truncated, OpenOptions{Passphrase: pass}); !errors.Is(err, ErrTruncated) {
		t.Fatalf("expected ErrTruncated, got %v", err)
	}
}

func TestChunkReorderingIsDetected(t *testing.T) {
	pass := []byte("passphrase")
	plain := randomBytes(t, 4096)

	sealed, err := Seal(plain, SealOptions{Passphrase: pass, KDF: cheapKDF, ChunkSize: 1024})
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	header, consumed, err := ParseHeader(sealed)
	if err != nil {
		t.Fatalf("ParseHeader: %v", err)
	}
	chunkLen := 4 + int(header.ChunkSize) + TagSize

	swapped := bytes.Clone(sealed)
	first := swapped[consumed : consumed+chunkLen]
	second := swapped[consumed+chunkLen : consumed+2*chunkLen]
	tmp := bytes.Clone(first)
	copy(first, second)
	copy(second, tmp)

	if _, err := Open(swapped, OpenOptions{Passphrase: pass}); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("expected ErrAuthentication, got %v", err)
	}
}

// TestSplicingIsDetected appends the chunks of one message to another sealed under
// the same passphrase. The Argon2id salt and the nonce prefix differ per container,
// so the derived keys differ; this checks that the failure is reported rather than
// producing a partially valid plaintext.
func TestSplicingIsDetected(t *testing.T) {
	pass := []byte("passphrase")

	a, err := Seal(randomBytes(t, 2048), SealOptions{Passphrase: pass, KDF: cheapKDF, ChunkSize: 1024})
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	b, err := Seal(randomBytes(t, 2048), SealOptions{Passphrase: pass, KDF: cheapKDF, ChunkSize: 1024})
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	_, consumed, err := ParseHeader(b)
	if err != nil {
		t.Fatalf("ParseHeader: %v", err)
	}

	spliced := append(bytes.Clone(a), b[consumed:]...)
	if _, err := Open(spliced, OpenOptions{Passphrase: pass}); err == nil {
		t.Fatal("a spliced container opened successfully")
	}
}

func TestIdentitySerialisationRoundTrip(t *testing.T) {
	id, err := GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}

	pub, err := ParsePublicIdentity(id.Public.String())
	if err != nil {
		t.Fatalf("ParsePublicIdentity: %v", err)
	}
	if !bytes.Equal(pub.Bytes(), id.Public.Bytes()) {
		t.Fatal("public identity did not survive a serialisation round trip")
	}

	priv, err := ParsePrivateIdentity(id.String())
	if err != nil {
		t.Fatalf("ParsePrivateIdentity: %v", err)
	}
	if !bytes.Equal(priv.Bytes(), id.Bytes()) {
		t.Fatal("private identity did not survive a serialisation round trip")
	}
	// The public half must be recomputed from the secret, never read from the blob.
	if !bytes.Equal(priv.Public.Bytes(), id.Public.Bytes()) {
		t.Fatal("recomputed public identity does not match the original")
	}

	// A container sealed to the original must open with the reparsed private key.
	sealed, err := Seal([]byte("payload"), SealOptions{Recipient: &id.Public})
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := Open(sealed, OpenOptions{Identity: priv}); err != nil {
		t.Fatalf("Open with reparsed identity: %v", err)
	}
}

func TestMalformedIdentitiesAreRejected(t *testing.T) {
	id, err := GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}

	cases := map[string]string{
		"empty":          "",
		"no prefix":      "AAAA",
		"wrong prefix":   privateIdentityPrefix + "AAAA",
		"bad base64":     publicIdentityPrefix + "!!!!",
		"truncated":      id.Public.String()[:40],
		"private as pub": id.String(),
	}

	for name, s := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParsePublicIdentity(s); err == nil {
				t.Fatalf("ParsePublicIdentity accepted %q", name)
			}
		})
	}
}

func TestHeaderRejectsHostileParameters(t *testing.T) {
	sealed, err := Seal([]byte("x"), SealOptions{Passphrase: []byte("p"), KDF: cheapKDF})
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	t.Run("absurd Argon2id memory", func(t *testing.T) {
		// Without a ceiling this is a one-line denial of service: every decode
		// attempt tries to allocate the declared amount before any authentication.
		h := bytes.Clone(sealed)
		off := headerCommonSize + argonSaltSize + 4
		binary.BigEndian.PutUint32(h[off:off+4], 0xFFFFFFFF)

		if _, err := Open(h, OpenOptions{Passphrase: []byte("p")}); !errors.Is(err, ErrMalformedHeader) {
			t.Fatalf("expected ErrMalformedHeader, got %v", err)
		}
	})

	t.Run("absurd Argon2id pass count", func(t *testing.T) {
		// The memory ceiling alone does not close this: a header declaring sixteen
		// million passes over a modest amount of memory hangs the decoder just as
		// effectively. Found because it made this very test suite take minutes.
		h := bytes.Clone(sealed)
		off := headerCommonSize + argonSaltSize
		binary.BigEndian.PutUint32(h[off:off+4], 0xFFFFFFFF)

		if _, err := Open(h, OpenOptions{Passphrase: []byte("p")}); !errors.Is(err, ErrMalformedHeader) {
			t.Fatalf("expected ErrMalformedHeader, got %v", err)
		}
	})

	t.Run("absurd chunk size", func(t *testing.T) {
		h := bytes.Clone(sealed)
		binary.BigEndian.PutUint32(h[8:12], 0xFFFFFFFF)

		if _, err := Open(h, OpenOptions{Passphrase: []byte("p")}); !errors.Is(err, ErrMalformedHeader) {
			t.Fatalf("expected ErrMalformedHeader, got %v", err)
		}
	})

	t.Run("unknown suite", func(t *testing.T) {
		h := bytes.Clone(sealed)
		h[6] = 0x7F

		if _, err := Open(h, OpenOptions{Passphrase: []byte("p")}); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("expected ErrUnsupported, got %v", err)
		}
	})

	t.Run("reserved flags set", func(t *testing.T) {
		h := bytes.Clone(sealed)
		h[7] = 0x01

		if _, err := Open(h, OpenOptions{Passphrase: []byte("p")}); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("expected ErrUnsupported, got %v", err)
		}
	})

	t.Run("empty input", func(t *testing.T) {
		if _, err := Open(nil, OpenOptions{Passphrase: []byte("p")}); !errors.Is(err, ErrMalformedHeader) {
			t.Fatalf("expected ErrMalformedHeader, got %v", err)
		}
	})
}

// TestSealIsNonDeterministic guards against the worst failure a stream cipher can
// have: sealing the same plaintext under the same passphrase twice must never
// produce the same bytes, because that would mean a reused nonce.
func TestSealIsNonDeterministic(t *testing.T) {
	pass := []byte("passphrase")
	plain := []byte("the same plaintext, twice")

	a, err := Seal(plain, SealOptions{Passphrase: pass, KDF: cheapKDF})
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	b, err := Seal(plain, SealOptions{Passphrase: pass, KDF: cheapKDF})
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	if bytes.Equal(a, b) {
		t.Fatal("two seals of the same plaintext produced identical containers")
	}
}

func TestSealOptionValidation(t *testing.T) {
	id, err := GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}

	cases := map[string]SealOptions{
		"neither passphrase nor recipient": {},
		"both at once":                     {Passphrase: []byte("p"), Recipient: &id.Public},
		"chunk size too small":             {Passphrase: []byte("p"), ChunkSize: 1},
		"chunk size too large":             {Passphrase: []byte("p"), ChunkSize: maxChunkSize + 1},
	}

	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Seal([]byte("x"), opts); err == nil {
				t.Fatalf("Seal accepted invalid options: %s", name)
			}
		})
	}
}

func TestOpenRequiresMatchingCredentialKind(t *testing.T) {
	id, err := GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}

	byPass, err := Seal([]byte("x"), SealOptions{Passphrase: []byte("p"), KDF: cheapKDF})
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	byIdentity, err := Seal([]byte("x"), SealOptions{Recipient: &id.Public})
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	if _, err := Open(byPass, OpenOptions{Identity: id}); err == nil {
		t.Fatal("opened a passphrase container with an identity")
	}
	if _, err := Open(byIdentity, OpenOptions{Passphrase: []byte("p")}); err == nil {
		t.Fatal("opened an identity container with a passphrase")
	}
}
