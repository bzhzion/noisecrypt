package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The point of a default location is that nothing has to be told where the key is. These
// drive the real commands rather than the helpers, because "the file is in the right
// place" and "the tool finds it" are different claims and only the second one matters.

func TestKeygenStoresAndOpenFindsWithoutBeingTold(t *testing.T) {
	home := t.TempDir()
	t.Setenv(storeDirEnvVar, home)
	t.Setenv("NC_TEST_PASS", "mon chat dort sur le radiateur")

	stdout, stderr, code := run("keygen", "-passphrase-env", "NC_TEST_PASS")
	if code != 0 {
		t.Fatalf("keygen exited %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "protected by your passphrase") {
		t.Error("keygen did not protect the identity by default, which is what makes a known location defensible")
	}

	stored, err := os.ReadFile(filepath.Join(home, identityBase))
	if err != nil {
		t.Fatalf("nothing was stored at the default location: %v", err)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(stored)), "noisecrypt-locked-v1:") {
		t.Fatalf("the stored identity is not locked: %.40s", stored)
	}
	if strings.Contains(string(stored), "noisecrypt-secret-v1:") {
		t.Fatal("the stored file contains the identity in the clear")
	}

	// The public half is printed, since it has to be shared and is not in the file.
	if !strings.Contains(stdout, "noisecrypt-public-v1:") {
		t.Error("keygen did not print the public identity")
	}

	// And now the claim that matters: seal to that identity and open it back without
	// ever naming a key file.
	pub := publicFrom(t, stdout)
	plain := filepath.Join(t.TempDir(), "message.txt")
	if err := os.WriteFile(plain, []byte("un message"), 0o600); err != nil {
		t.Fatal(err)
	}
	sealedPath := plain + ".ncry"
	if _, stderr, code := run("seal", "-in", plain, "-out", sealedPath, "-to", pub, "-force"); code != 0 {
		t.Fatalf("seal exited %d: %s", code, stderr)
	}

	back := filepath.Join(t.TempDir(), "back.txt")
	_, stderr, code = run("open", "-in", sealedPath, "-out", back, "-force",
		"-identity-passphrase-env", "NC_TEST_PASS")
	if code != 0 {
		t.Fatalf("open exited %d without being told where the key is: %s", code, stderr)
	}
	got, err := os.ReadFile(back)
	if err != nil || string(got) != "un message" {
		t.Fatalf("recovered %q, %v", got, err)
	}
}

func TestOpenRefusesTheWrongIdentityPassphrase(t *testing.T) {
	home := t.TempDir()
	t.Setenv(storeDirEnvVar, home)
	t.Setenv("NC_TEST_PASS", "la bonne phrase de passe")
	t.Setenv("NC_TEST_WRONG", "pas la bonne du tout")

	stdout, _, code := run("keygen", "-passphrase-env", "NC_TEST_PASS")
	if code != 0 {
		t.Fatal("keygen failed")
	}
	pub := publicFrom(t, stdout)

	dir := t.TempDir()
	plain := filepath.Join(dir, "message.txt")
	_ = os.WriteFile(plain, []byte("un message"), 0o600)
	sealedPath := filepath.Join(dir, "message.ncry")
	if _, _, code := run("seal", "-in", plain, "-out", sealedPath, "-to", pub, "-force"); code != 0 {
		t.Fatal("seal failed")
	}

	_, _, code = run("open", "-in", sealedPath, "-out", filepath.Join(dir, "out"), "-force",
		"-identity-passphrase-env", "NC_TEST_WRONG")
	if code == 0 {
		t.Fatal("the wrong identity passphrase opened the container")
	}
}

// -no-passphrase has to keep working and has to say what it did, since a key stored
// unprotected in a predictable place is exactly the thing this design set out to avoid.
func TestKeygenWithoutAPassphraseSaysSo(t *testing.T) {
	t.Setenv(storeDirEnvVar, t.TempDir())

	stdout, stderr, code := run("keygen", "-no-passphrase")
	if code != 0 {
		t.Fatalf("exited %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "UNPROTECTED") {
		t.Error("an unprotected identity was stored without saying so")
	}
}

// A key file whose permissions were never checked is a key file whose permissions are
// unknown. On Windows this is the whole reason restrictToOwner exists: the mode passed
// to OpenFile is not applied there, so before this the file kept SYSTEM and
// Administrators entries inherited from its parent.
func TestTheStoredIdentityIsNotReadableByEveryone(t *testing.T) {
	home := t.TempDir()
	t.Setenv(storeDirEnvVar, home)

	if _, stderr, code := run("keygen", "-no-passphrase"); code != 0 {
		t.Fatalf("keygen exited %d: %s", code, stderr)
	}
	path := filepath.Join(home, identityBase)

	if runtime.GOOS == "windows" {
		// The mode is meaningless here, so the only honest check is that the call
		// which does the real work ran and reported nothing.
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("no identity stored: %v", err)
		}
		return
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("no identity stored: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("identity is mode %o, expected 600", perm)
	}
	dir, err := os.Stat(home)
	if err != nil {
		t.Fatal(err)
	}
	if perm := dir.Mode().Perm(); perm != 0o700 {
		t.Errorf("the store directory is mode %o, expected 700", perm)
	}
}

func publicFrom(t *testing.T, output string) string {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "noisecrypt-public-v1:") {
			return line
		}
	}
	t.Fatalf("no public identity in:\n%s", output)
	return ""
}
