package container

import (
	"bytes"
	"errors"
	"testing"

	"github.com/bzhzion/noisecrypt/internal/crypt"
)

func identity(t *testing.T) *crypt.PrivateIdentity {
	t.Helper()
	id, err := crypt.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	return id
}

func TestSignedRoundTrip(t *testing.T) {
	alice := identity(t)
	bob := identity(t)

	data := []byte("a message with a provable author")
	packed, err := PackWith(PackOptions{
		Metadata:  Metadata{Name: "note.txt", Compression: CompressionGzip},
		Signer:    alice,
		Recipient: bob.Public.Fingerprint(),
	}, data)
	if err != nil {
		t.Fatalf("PackWith: %v", err)
	}

	got, err := Open(packed, bob.Public.Fingerprint())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(got.Data, data) {
		t.Fatal("data did not survive")
	}
	if got.Signer == nil {
		t.Fatal("a signed container came back with no signer")
	}
	if !bytes.Equal(got.Signer.Bytes(), alice.Public.Bytes()) {
		t.Fatal("the reported signer is not the one who signed")
	}
}

// TestUnsignedIsNotAnError pins the decision that signing is optional. An unsigned
// container must open normally and simply report no signer.
func TestUnsignedIsNotAnError(t *testing.T) {
	packed, err := Pack(Metadata{Name: "note.txt"}, []byte("anonymous"))
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}

	got, err := Open(packed, [crypt.FingerprintSize]byte{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got.Signer != nil {
		t.Fatal("an unsigned container reported a signer")
	}
}

// TestSurreptitiousForwardingIsRefused is the attack the recipient binding exists for,
// and the reason signing the plaintext is not enough on its own.
//
// Alice signs a message and encrypts it to Bob. Bob decrypts it, keeps the still-valid
// signed payload, and re-encrypts it to Charlie. Without the binding, Charlie sees a
// payload signed by Alice, addressed to him, and reasonably concludes Alice wrote to
// him. She never did.
func TestSurreptitiousForwardingIsRefused(t *testing.T) {
	alice, bob, charlie := identity(t), identity(t), identity(t)

	packed, err := PackWith(PackOptions{
		Metadata:  Metadata{Name: "for-bob.txt"},
		Signer:    alice,
		Recipient: bob.Public.Fingerprint(),
	}, []byte("meant for Bob alone"))
	if err != nil {
		t.Fatalf("PackWith: %v", err)
	}

	// Bob can open it, as intended.
	if _, err := Open(packed, bob.Public.Fingerprint()); err != nil {
		t.Fatalf("the intended recipient could not open it: %v", err)
	}

	// Charlie, handed the very same payload, must not see a valid Alice signature.
	if _, err := Open(packed, charlie.Public.Fingerprint()); !errors.Is(err, crypt.ErrBadSignature) {
		t.Fatalf("expected ErrBadSignature when re-aimed at a third party, got %v", err)
	}
}

// TestSignatureCoversMetadata checks that the file name is inside the signed region.
// Signing only the body would let a container keep a valid signature while claiming a
// different name, which is exactly the kind of thing a user would trust.
func TestSignatureCoversMetadata(t *testing.T) {
	alice, bob := identity(t), identity(t)
	fp := bob.Public.Fingerprint()

	packed, err := PackWith(PackOptions{
		Metadata:  Metadata{Name: "invoice.pdf", Compression: CompressionNone},
		Signer:    alice,
		Recipient: fp,
	}, []byte("body"))
	if err != nil {
		t.Fatalf("PackWith: %v", err)
	}

	// Rewrite the stored name in place, same length so nothing else shifts.
	tampered := bytes.Clone(packed)
	at := bytes.Index(tampered, []byte("invoice.pdf"))
	if at < 0 {
		t.Fatal("could not locate the name in the payload")
	}
	copy(tampered[at:], []byte("payslip.pdf"))

	if _, err := Open(tampered, fp); !errors.Is(err, crypt.ErrBadSignature) {
		t.Fatalf("expected ErrBadSignature after renaming the file, got %v", err)
	}
}

// TestEveryByteOfASignedPayloadIsCovered flips one bit in every byte and requires that
// nothing opens. It is the blunt version of the two targeted tests above and catches
// any region a future change might accidentally leave outside the signature.
func TestEveryByteOfASignedPayloadIsCovered(t *testing.T) {
	alice, bob := identity(t), identity(t)
	fp := bob.Public.Fingerprint()

	packed, err := PackWith(PackOptions{
		Metadata:  Metadata{Name: "n.txt", Compression: CompressionNone},
		Signer:    alice,
		Recipient: fp,
	}, []byte("some body bytes"))
	if err != nil {
		t.Fatalf("PackWith: %v", err)
	}

	for i := range packed {
		tampered := bytes.Clone(packed)
		tampered[i] ^= 0x01

		if _, err := Open(tampered, fp); err == nil {
			t.Fatalf("byte %d: a tampered signed payload opened successfully", i)
		}
	}
}

// TestStrippingTheSignatureIsDetectable is the honest limit of this design, written
// down as a test so nobody discovers it by surprise.
//
// Removing the signature block and clearing the flag produces a valid *unsigned*
// container. Nothing in the payload can prevent that, because the thing that would
// prove a signature was expected is exactly the thing being removed. What the payload
// can do is refuse to look signed while carrying no signature, which is checked here.
//
// Requiring a signature is therefore the caller's job, and the command line exposes
// that as a flag. This test exists to make sure the reasoning is not forgotten.
func TestStrippingTheSignatureIsDetectable(t *testing.T) {
	alice, bob := identity(t), identity(t)
	fp := bob.Public.Fingerprint()

	packed, err := PackWith(PackOptions{
		Metadata:  Metadata{Name: "n.txt", Compression: CompressionNone},
		Signer:    alice,
		Recipient: fp,
	}, []byte("body"))
	if err != nil {
		t.Fatalf("PackWith: %v", err)
	}

	// Keep the flag, drop the block: must fail rather than ignore the flag.
	truncated := packed[:len(packed)-crypt.SignatureSize-crypt.PublicIdentitySize]
	if _, err := Open(truncated, fp); err == nil {
		t.Fatal("a payload claiming a signature it does not carry was accepted")
	}

	// Drop the block and clear the flag: this is a valid unsigned container, and the
	// caller has to notice the missing signer for itself.
	stripped := bytes.Clone(truncated)
	stripped[6] = 0
	got, err := Open(stripped, fp)
	if err != nil {
		t.Fatalf("a correctly stripped payload should open as unsigned: %v", err)
	}
	if got.Signer != nil {
		t.Fatal("a stripped payload reported a signer")
	}
}
