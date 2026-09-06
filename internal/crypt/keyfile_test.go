package crypt

import (
	"strings"
	"testing"
)

// A private identity written to disk in the clear was the only thing keygen could
// produce, and that was what stood between this tool and a well-known directory. SSH
// gets away with the convention because ssh-keygen asks for a passphrase; taking the
// directory without the passphrase would be taking the half that costs and leaving the
// half that pays.

func TestAnIdentitySurvivesBeingLockedAndUnlocked(t *testing.T) {
	id, err := GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	pass := []byte("mon chat dort sur le radiateur")

	locked, err := LockIdentity(id, pass, KDFParams{Time: 1, Memory: 8 << 10, Lanes: 1})
	if err != nil {
		t.Fatalf("LockIdentity: %v", err)
	}

	// The stored form must not contain the identity it protects. Obvious, and exactly
	// the kind of thing a refactor breaks without anything else noticing.
	if strings.Contains(locked, id.String()) {
		t.Fatal("the locked form contains the identity in the clear")
	}
	if !IsLockedIdentity(locked) {
		t.Error("a locked identity is not recognised as one, so nothing will ask for a passphrase")
	}

	back, err := UnlockIdentity(locked, pass)
	if err != nil {
		t.Fatalf("UnlockIdentity: %v", err)
	}
	if back.String() != id.String() {
		t.Error("the identity did not survive the round trip")
	}
	if back.Public.Fingerprint() != id.Public.Fingerprint() {
		t.Error("the public half no longer matches")
	}
}

func TestALockedIdentityRefusesTheWrongPassphrase(t *testing.T) {
	id, _ := GenerateIdentity()
	kdf := KDFParams{Time: 1, Memory: 8 << 10, Lanes: 1}
	locked, err := LockIdentity(id, []byte("la bonne phrase de passe"), kdf)
	if err != nil {
		t.Fatalf("LockIdentity: %v", err)
	}

	if _, err := UnlockIdentity(locked, []byte("pas la bonne du tout")); err == nil {
		t.Fatal("a wrong passphrase opened the identity")
	}
}

// Asking is a different thing from failing. A caller needs to know it should prompt,
// which is not the same as being told the file is broken.
func TestALockedIdentitySaysItNeedsAPassphrase(t *testing.T) {
	id, _ := GenerateIdentity()
	locked, _ := LockIdentity(id, []byte("phrase"), KDFParams{Time: 1, Memory: 8 << 10, Lanes: 1})

	_, err := UnlockIdentity(locked, nil)
	if err != ErrIdentityLocked {
		t.Fatalf("got %v, expected ErrIdentityLocked so the caller knows to ask", err)
	}
}

// The plain form has to keep working. Identities exist that were written before any of
// this, and a tool that cannot read what it wrote last week is a tool that eats keys.
func TestAPlainIdentityStillReads(t *testing.T) {
	id, _ := GenerateIdentity()
	plain := id.String()

	if IsLockedIdentity(plain) {
		t.Error("a plain identity is mistaken for a locked one")
	}
	// Passphrase supplied or not, a plain identity reads either way, so a caller can
	// pass whatever it holds without first working out which shape it has.
	for _, pass := range [][]byte{nil, []byte("inutile ici")} {
		back, err := UnlockIdentity(plain, pass)
		if err != nil {
			t.Fatalf("UnlockIdentity(plain): %v", err)
		}
		if back.String() != plain {
			t.Error("a plain identity did not survive")
		}
	}
}
