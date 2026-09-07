//go:build windows

package cli

import (
	"strings"
	"testing"

	"golang.org/x/sys/windows/registry"
)

// These run against a scratch registry root rather than the real one. A test that
// rewrites the developer's actual file associations is a test somebody disables, and then
// the mechanics go untested; a test that leaves half its keys behind on failure is worse.
func scratchRoot(t *testing.T) {
	t.Helper()
	previous := shellRoot
	shellRoot = `Software\Classes\NoiseCryptScratch`
	t.Cleanup(func() {
		_ = shellUnregister(&Env{Stdout: discard{}, Stderr: discard{}})
		// The scratch parent itself, which unregister has no reason to know about.
		for _, k := range []string{
			shellRoot + `\*\shell`, shellRoot + `\*`,
			shellRoot + `\Directory\Background\shell`, shellRoot + `\Directory\Background`,
			shellRoot + `\Directory`, shellRoot,
		} {
			_ = registry.DeleteKey(registry.CURRENT_USER, k)
		}
		shellRoot = previous
	})
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

func TestShellRegisterWritesEveryEntry(t *testing.T) {
	scratchRoot(t)

	if _, _, code := run("shell", "register"); code != 0 {
		t.Fatalf("shell register exited %d", code)
	}

	cases := []struct {
		key, wants string
	}{
		// Right-click on any file. The `*` is a literal key name here and not a
		// wildcard, which is worth pinning: a check written with a wildcard-aware
		// helper reports this key missing when it is present, and that is how the
		// first manual verification of this feature went.
		{encryptKey() + `\command`, ` -pause seal -in "%1"`},
		// Double-click a .ncry.
		{decryptKey() + `\shell\open\command`, ` -pause open -in "%1"`},
		// Right-click the background of a folder.
		{identityKey() + `\command`, ` -pause keygen`},
	}
	for _, c := range cases {
		k, err := registry.OpenKey(registry.CURRENT_USER, c.key, registry.QUERY_VALUE)
		if err != nil {
			t.Errorf("%s was not created: %v", c.key, err)
			continue
		}
		command, _, err := k.GetStringValue("")
		k.Close()
		if err != nil {
			t.Errorf("%s has no command: %v", c.key, err)
			continue
		}
		if !strings.HasSuffix(command, c.wants) {
			t.Errorf("%s is %q, expected it to end with %q", c.key, command, c.wants)
		}
		// Every entry has to ask for the pause, or the console closes before the
		// result can be read and the whole gesture looks broken.
		if !strings.Contains(command, "-pause") {
			t.Errorf("%s does not pause, so its window will vanish", c.key)
		}
	}

	// The extension has to point at the ProgID, or double-clicking does nothing at all.
	ext, err := registry.OpenKey(registry.CURRENT_USER, extensionKey(), registry.QUERY_VALUE)
	if err != nil {
		t.Fatalf("the .ncry association was not created: %v", err)
	}
	defer ext.Close()
	if got, _, _ := ext.GetStringValue(""); got != progID {
		t.Errorf("the extension points at %q rather than %q", got, progID)
	}
}

// Uninstalling has to leave nothing. A stale key pointing at a program that no longer
// exists gives a menu entry that silently does nothing, which is worse than no entry.
func TestShellUnregisterLeavesNothing(t *testing.T) {
	scratchRoot(t)

	if _, _, code := run("shell", "register"); code != 0 {
		t.Fatal("shell register failed")
	}
	if _, _, code := run("shell", "unregister"); code != 0 {
		t.Fatal("shell unregister failed")
	}

	for _, key := range []string{
		encryptKey(), decryptKey(), extensionKey(), identityKey(),
	} {
		if k, err := registry.OpenKey(registry.CURRENT_USER, key, registry.QUERY_VALUE); err == nil {
			k.Close()
			t.Errorf("%s survived unregistering", key)
		}
	}
}

// status is the only thing that can catch the one real weakness of registering a path:
// the binary moving. Nothing else notices, because Explorer just does nothing.
func TestShellStatusNoticesAMissingProgram(t *testing.T) {
	scratchRoot(t)

	stdout, _, _ := run("shell", "status")
	if !strings.Contains(stdout, "not registered") {
		t.Errorf("status on a clean root does not say so: %q", stdout)
	}

	if _, _, code := run("shell", "register"); code != 0 {
		t.Fatal("shell register failed")
	}
	stdout, _, _ = run("shell", "status")
	if !strings.Contains(stdout, "registered") {
		t.Error("status does not report a registration that exists")
	}

	// Point it at something that is not there and check it is noticed.
	k, _, err := registry.CreateKey(registry.CURRENT_USER, encryptKey()+`\command`, registry.SET_VALUE)
	if err != nil {
		t.Fatal(err)
	}
	_ = k.SetStringValue("", `"C:\nowhere\noisecrypt.exe" -pause seal -in "%1"`)
	k.Close()

	stdout, _, _ = run("shell", "status")
	if !strings.Contains(stdout, "no longer exists") {
		t.Errorf("status did not notice the program is gone: %q", stdout)
	}
}

func TestPauseFlagIsStrippedBeforeDispatch(t *testing.T) {
	// The flag has to be invisible to the command behind it, or every command would
	// need to know about it.
	stdout, stderr, code := run("-pause", "version")
	if code != 0 {
		t.Fatalf("exited %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, Version) {
		t.Errorf("the command behind -pause did not run: %q", stdout)
	}

	// And on its own it is not a command.
	if _, _, code := run("-pause"); code != 2 {
		t.Errorf("-pause alone exited %d, expected usage", code)
	}
}
