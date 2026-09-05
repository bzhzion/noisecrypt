package crypt

import (
	"crypto/ed25519"
	"errors"
	"fmt"

	"github.com/cloudflare/circl/sign/mldsa/mldsa65"
)

// SignatureSize is the length of a signature block: one Ed25519 signature followed by
// one ML-DSA-65 signature.
const SignatureSize = ed25519.SignatureSize + mldsa65.SignatureSize

// signContext is the domain separator. It goes in ML-DSA's context parameter and is
// prepended to what Ed25519 signs, so a signature made by this tool can never be
// mistaken for, or replayed as, a signature made by anything else using the same key.
const signContext = "noisecrypt/v1 payload signature"

var (
	// ErrBadSignature is returned when a signature is present and does not verify.
	ErrBadSignature = errors.New("crypt: signature does not verify")

	// ErrWrongSigner is returned when a container is signed by an identity other than
	// the one the caller required.
	ErrWrongSigner = errors.New("crypt: container is signed by a different identity")
)

// Sign produces a hybrid signature over payload, bound to a recipient.
//
// # Both signatures, always
//
// Ed25519 and ML-DSA-65 are both applied and both must verify. The reasoning mirrors
// the key exchange: Ed25519 is old, well studied and falls to a quantum computer;
// ML-DSA resists quantum attack and is young enough that its security estimates still
// move. Requiring both means an attacker needs to break both.
//
// # Why the recipient is part of what gets signed
//
// Without it, this construction has a real hole known as surreptitious forwarding.
// Alice signs a message and encrypts it to Bob. Bob decrypts it, keeps the still-valid
// signed payload, and re-encrypts it to Charlie. Charlie sees a payload signed by
// Alice, addressed to him, and reasonably concludes Alice wrote to him. She never did.
//
// Binding the recipient's fingerprint into the signature closes it: Charlie's copy
// declares it was meant for Bob, and verification against Charlie's own identity fails.
//
// In passphrase mode there is no recipient to bind, and no recipient to redirect a
// container towards either, so the fingerprint is zero.
func Sign(payload []byte, recipient [FingerprintSize]byte, id *PrivateIdentity) ([]byte, error) {
	if id == nil {
		return nil, errors.New("crypt: signing needs a private identity")
	}

	msg := signedMessage(payload, recipient, id.Public.Bytes())

	out := make([]byte, 0, SignatureSize)
	out = append(out, ed25519.Sign(id.ed25519, msg)...)

	sig := make([]byte, mldsa65.SignatureSize)
	// randomized: true asks for the hedged variant, which mixes fresh randomness into
	// the signature. The deterministic variant is also sound, but hedging is the
	// recommended default because it degrades more gracefully if the platform's
	// randomness is poor rather than catastrophically if a nonce ever repeats.
	if err := mldsa65.SignTo(id.mldsa, msg, []byte(signContext), true, sig); err != nil {
		return nil, fmt.Errorf("crypt: ML-DSA signing: %w", err)
	}
	out = append(out, sig...)

	return out, nil
}

// Verify checks a hybrid signature against a claimed signer.
//
// It fails if either signature fails. There is deliberately no mode that accepts one
// out of two: a caller who would settle for the classical signature alone has thrown
// away the reason for having the other.
func Verify(payload []byte, recipient [FingerprintSize]byte, signer PublicIdentity, signature []byte) error {
	if len(signature) != SignatureSize {
		return fmt.Errorf("%w: signature block is %d bytes, expected %d",
			ErrBadSignature, len(signature), SignatureSize)
	}

	msg := signedMessage(payload, recipient, signer.Bytes())

	if !ed25519.Verify(signer.ed25519, msg, signature[:ed25519.SignatureSize]) {
		return fmt.Errorf("%w: Ed25519 half", ErrBadSignature)
	}
	if !mldsa65.Verify(signer.mldsa, msg, []byte(signContext), signature[ed25519.SignatureSize:]) {
		return fmt.Errorf("%w: ML-DSA half", ErrBadSignature)
	}
	return nil
}

// signedMessage builds the exact bytes both algorithms sign.
//
// The domain separator is included here as well as passed to ML-DSA as its context,
// because Ed25519 has no context parameter of its own and would otherwise sign a bare
// payload that another protocol using the same key could also have produced.
//
// # Why the signer's own key is in there
//
// This is not decoration, and it was added because a blunt bit-flipping test found the
// hole. A signed container carries the signer's public identity so the recipient knows
// who to check against, and that identity holds four keys: two for signing and two for
// encryption. Only the signing pair takes part in verification, so without self-binding
// the *encryption* keys inside the claimed identity were covered by nothing at all.
//
// An attacker could therefore swap them and keep the signature valid. The recipient
// would see "signed by Alice", correctly, next to encryption keys belonging to the
// attacker, and a reply composed from that identity would go to the attacker instead of
// to Alice. Binding the whole identity closes it: every byte of it is now signed.
func signedMessage(payload []byte, recipient [FingerprintSize]byte, signer []byte) []byte {
	msg := make([]byte, 0, len(signContext)+len(signer)+FingerprintSize+len(payload))
	msg = append(msg, signContext...)
	msg = append(msg, signer...)
	msg = append(msg, recipient[:]...)
	msg = append(msg, payload...)
	return msg
}
