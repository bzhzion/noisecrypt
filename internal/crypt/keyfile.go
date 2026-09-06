package crypt

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// A private identity written to disk in the clear is a secret sitting in a file, and
// that was the only thing keygen could produce. It is also what stood between this tool
// and a well-known directory: putting keys somewhere predictable is a convenience when
// what is found there is locked, and an invitation when it is not. SSH gets away with
// the convention because ssh-keygen asks for a passphrase; copying the directory without
// the passphrase would be taking the half that costs and leaving the half that pays.
//
// So a stored identity comes in two shapes, and both are ordinary text:
//
//	noisecrypt-secret-v1:...     the identity itself, readable by anyone holding the file
//	noisecrypt-locked-v1:...     the same identity, sealed under a passphrase
//
// The locked shape is not a new format. It is the container this tool already builds for
// everything else, holding the identity token as its payload, which means the passphrase
// hardening, the AEAD and the header all come from code that the rest of the program
// exercises constantly rather than from a second implementation written for keys alone.

const lockedIdentityPrefix = "noisecrypt-locked-v1:"

// ErrIdentityLocked is returned when a stored identity needs a passphrase and none was
// supplied. Callers use it to know they should ask, rather than to report a failure.
var ErrIdentityLocked = errors.New("crypt: this identity is protected by a passphrase")

// LockIdentity seals a private identity under a passphrase for storage.
func LockIdentity(id *PrivateIdentity, passphrase []byte, kdf KDFParams) (string, error) {
	if id == nil {
		return "", errors.New("crypt: no identity to lock")
	}
	if len(passphrase) == 0 {
		return "", errors.New("crypt: locking an identity needs a passphrase")
	}

	sealed, err := Seal([]byte(id.String()), SealOptions{Passphrase: passphrase, KDF: kdf})
	if err != nil {
		return "", fmt.Errorf("crypt: locking the identity: %w", err)
	}
	return lockedIdentityPrefix + base64.RawURLEncoding.EncodeToString(sealed), nil
}

// IsLockedIdentity reports whether stored text needs a passphrase to be read.
//
// Offered separately so a caller can ask before prompting. Prompting first and finding
// out afterwards means asking for a passphrase that is not wanted, which teaches people
// to type one at any prompt.
func IsLockedIdentity(s string) bool {
	return strings.HasPrefix(strings.TrimSpace(s), lockedIdentityPrefix)
}

// UnlockIdentity reads a stored identity in either shape.
//
// A passphrase is required for the locked shape and ignored for the plain one, so a
// caller can pass whatever it has without first working out which it is holding.
func UnlockIdentity(s string, passphrase []byte) (*PrivateIdentity, error) {
	s = strings.TrimSpace(s)

	if !IsLockedIdentity(s) {
		return ParsePrivateIdentity(s)
	}
	if len(passphrase) == 0 {
		return nil, ErrIdentityLocked
	}

	raw, err := decodeToken(s, lockedIdentityPrefix)
	if err != nil {
		return nil, err
	}

	token, err := Open(raw, OpenOptions{Passphrase: passphrase})
	if err != nil {
		// Passed through as it stands. A wrong passphrase and a tampered file are one
		// indistinguishable failure here for the same reason they are everywhere else
		// in this tool: telling them apart would tell an attacker which of the two they
		// are looking at.
		return nil, err
	}
	defer func() {
		for i := range token {
			token[i] = 0
		}
	}()

	id, err := ParsePrivateIdentity(string(bytes.TrimSpace(token)))
	if err != nil {
		return nil, fmt.Errorf("crypt: the passphrase was accepted but the contents are not an identity: %w", err)
	}
	return id, nil
}
