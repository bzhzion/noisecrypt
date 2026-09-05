package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// keypair generates an identity and returns the key file path and the public token,
// the way a user would: read the token off the terminal, keep the file.
func keypair(t *testing.T, env *testEnv, dir, name string) (keyFile, pub string) {
	t.Helper()
	keyFile = filepath.Join(dir, name+".key")
	env.stdout.Reset()
	env.run(t, "keygen", "-out", keyFile)

	for _, line := range strings.Split(env.stdout.String(), "\n") {
		if s := strings.TrimSpace(line); strings.HasPrefix(s, "noisecrypt-public-v1:") {
			pub = s
		}
	}
	if pub == "" {
		t.Fatalf("keygen printed no public identity:\n%s", env.stdout)
	}
	return keyFile, pub
}

// TestSignedRoundTripThroughTheCLI is the whole feature as a user meets it: Alice
// signs, Bob requires her signature specifically, and the file arrives intact.
func TestSignedRoundTripThroughTheCLI(t *testing.T) {
	dir := t.TempDir()
	data := []byte("provably from Alice")
	src := writeFile(t, dir, "note.txt", data)

	env := newTestEnv("")
	env.ReadPassphrase = nil

	aliceKey, alicePub := keypair(t, env, dir, "alice")
	bobKey, bobPub := keypair(t, env, dir, "bob")

	sealed := filepath.Join(dir, "note.ncry")
	out := filepath.Join(dir, "recovered.txt")

	env.run(t, "seal", "-in", src, "-out", sealed, "-to", bobPub, "-sign", aliceKey)

	env.stdout.Reset()
	env.run(t, "open", "-in", sealed, "-out", out, "-identity", bobKey, "-from", alicePub)

	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("reading the recovered file: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("the signed round trip did not preserve the data")
	}
	if !strings.Contains(env.stdout.String(), "Signature verified") {
		t.Fatalf("the successful verification was not reported:\n%s", env.stdout)
	}
}

// TestSignatureFromTheWrongIdentityIsRefused covers the case -from exists for.
func TestSignatureFromTheWrongIdentityIsRefused(t *testing.T) {
	dir := t.TempDir()
	src := writeFile(t, dir, "note.txt", []byte("data"))

	env := newTestEnv("")
	env.ReadPassphrase = nil

	aliceKey, _ := keypair(t, env, dir, "alice")
	bobKey, bobPub := keypair(t, env, dir, "bob")
	_, strangerPub := keypair(t, env, dir, "stranger")

	sealed := filepath.Join(dir, "note.ncry")
	env.run(t, "seal", "-in", src, "-out", sealed, "-to", bobPub, "-sign", aliceKey)

	env.stderr.Reset()
	env.runExpectingFailure(t, "open", "-in", sealed, "-out", filepath.Join(dir, "x.txt"),
		"-identity", bobKey, "-from", strangerPub)
	if !strings.Contains(env.stderr.String(), "signed by a different identity") {
		t.Fatalf("the refusal does not explain itself:\n%s", env.stderr)
	}
}

// TestUnsignedContainerBehaviour pins the two halves of the optionality decision:
// an unsigned container opens normally and says so, unless a signature was demanded.
func TestUnsignedContainerBehaviour(t *testing.T) {
	dir := t.TempDir()
	src := writeFile(t, dir, "note.txt", []byte("anonymous"))

	env := newTestEnv("")
	env.ReadPassphrase = nil

	bobKey, bobPub := keypair(t, env, dir, "bob")
	sealed := filepath.Join(dir, "note.ncry")
	env.run(t, "seal", "-in", src, "-out", sealed, "-to", bobPub)

	t.Run("opens and reports the absence", func(t *testing.T) {
		env.stdout.Reset()
		env.run(t, "open", "-in", sealed, "-out", filepath.Join(dir, "a.txt"), "-identity", bobKey)
		if !strings.Contains(env.stdout.String(), "Not signed") {
			t.Fatalf("the absence of a signature was not reported:\n%s", env.stdout)
		}
	})

	t.Run("refused when a signature is required", func(t *testing.T) {
		env.stderr.Reset()
		env.runExpectingFailure(t, "open", "-in", sealed, "-out", filepath.Join(dir, "b.txt"),
			"-identity", bobKey, "-require-signature")
		if !strings.Contains(env.stderr.String(), "not signed") {
			t.Fatalf("the refusal does not explain itself:\n%s", env.stderr)
		}
	})

	t.Run("refused when a specific signer is required", func(t *testing.T) {
		_, otherPub := keypair(t, env, dir, "other")
		env.runExpectingFailure(t, "open", "-in", sealed, "-out", filepath.Join(dir, "c.txt"),
			"-identity", bobKey, "-from", otherPub)
	})
}

// TestSigningWorksInPassphraseMode checks the case with no recipient at all. There is
// nothing to bind the signature to and nothing to re-aim the container towards, so it
// has to work rather than being rejected for a missing fingerprint.
func TestSigningWorksInPassphraseMode(t *testing.T) {
	dir := t.TempDir()
	data := []byte("signed but not addressed")
	src := writeFile(t, dir, "note.txt", data)
	passFile := writeFile(t, dir, "pass.txt", []byte("une phrase de passe correcte\n"))

	env := newTestEnv("")
	env.ReadPassphrase = nil
	aliceKey, alicePub := keypair(t, env, dir, "alice")

	sealed := filepath.Join(dir, "note.ncry")
	out := filepath.Join(dir, "recovered.txt")

	env.run(t, "seal", "-in", src, "-out", sealed, "-passphrase-file", passFile,
		"-sign", aliceKey, "-kdf-time", "1", "-kdf-memory", "8", "-kdf-lanes", "1")
	env.run(t, "open", "-in", sealed, "-out", out, "-passphrase-file", passFile, "-from", alicePub)

	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("reading the recovered file: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("the data did not survive")
	}
}

// TestSignerIsReportedShort guards the usability fix. Printing the full 4288 character
// token on every successful decode buries the rest of the output and trains people to
// ignore it.
func TestSignerIsReportedShort(t *testing.T) {
	dir := t.TempDir()
	src := writeFile(t, dir, "note.txt", []byte("data"))

	env := newTestEnv("")
	env.ReadPassphrase = nil
	aliceKey, _ := keypair(t, env, dir, "alice")
	bobKey, bobPub := keypair(t, env, dir, "bob")

	sealed := filepath.Join(dir, "note.ncry")
	env.run(t, "seal", "-in", src, "-out", sealed, "-to", bobPub, "-sign", aliceKey)

	env.stdout.Reset()
	env.run(t, "open", "-in", sealed, "-out", filepath.Join(dir, "a.txt"), "-identity", bobKey)

	out := env.stdout.String()
	if strings.Contains(out, "noisecrypt-public-v1:") {
		t.Fatal("the full identity token was printed on a successful decode")
	}
	// Four groups of four hex characters.
	if !strings.Contains(out, "signed by ") {
		t.Fatalf("no short fingerprint in the output:\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "signed by ") && len(line) > 80 {
			t.Fatalf("the signer line is %d characters long:\n%s", len(line), line)
		}
	}
}
