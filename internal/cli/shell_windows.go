//go:build windows

package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows/registry"
)

// Windows shell integration: right-click to encrypt, double-click to decrypt.
//
// # Why the binary does this and not the installer
//
// The installer could write these keys itself, and every installer script in the world
// does. It would then hold a second copy of knowledge that lives here, and the two would
// drift the first time an entry changed. So the installer calls `noisecrypt shell
// register` after copying files and `shell unregister` before removing them, and there is
// one description of what integration means.
//
// It also means someone who downloaded the bare binary gets the same feature with one
// command, and can undo it with another.
//
// # Why HKCU and no administrator
//
// Everything below lives under HKCU\Software\Classes, which is writable by an ordinary
// user. Nothing here needs elevation, so nothing here asks for it.
//
// The `.ncry` association works because nobody owns that extension. Since Windows 8 an
// extension already claimed by an application is protected by a hash under
// Explorer\FileExts\<ext>\UserChoice, and writing the ProgID alone would be ignored: that
// is deliberate, and it is what stops programs stealing `.pdf` from each other. Measured
// before relying on it: `.ncry` is absent from HKCU\Software\Classes, from
// HKLM\Software\Classes and from FileExts, so the classic registration is honoured.

const progID = "NoiseCrypt.Container"

// Which icon inside the executable a container gets. The binary carries two: the
// application tile at 0, and the document page at 1. Kept as a named constant next to a
// test that checks the executable really has a second icon, because the failure mode of
// getting this wrong is not an error: Windows silently falls back to index 0, so every
// container would quietly wear the program's own icon and nothing would report it.
const containerIconIndex = "1"

// shellRoot is a variable so a test can exercise the registry mechanics somewhere
// harmless. A test that rewrites the developer's real file associations is a test that
// gets disabled, and then the mechanics go untested.
var shellRoot = `Software\Classes`

func encryptKey() string   { return shellRoot + `\*\shell\NoiseCryptEncrypt` }
func decryptKey() string   { return shellRoot + `\` + progID }
func extensionKey() string { return shellRoot + `\.ncry` }
func identityKey() string {
	return shellRoot + `\Directory\Background\shell\NoiseCryptIdentity`
}

func runShell(env *Env, args []string) error {
	fs := newFlagSet(env, "shell", "register | unregister | status")
	if err := fs.Parse(args); err != nil {
		return err
	}
	switch fs.Arg(0) {
	case "register":
		return shellRegister(env)
	case "unregister":
		return shellUnregister(env)
	case "status", "":
		return shellStatus(env)
	default:
		fs.Usage()
		return fmt.Errorf("unknown action %q", fs.Arg(0))
	}
}

func shellRegister(env *Env) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot locate this program: %w", err)
	}
	exe, err = filepath.Abs(exe)
	if err != nil {
		return err
	}
	quoted := `"` + exe + `"`

	entries := []struct {
		key, name, command, icon string
	}{
		{
			encryptKey(),
			"Encrypt with NoiseCrypt",
			quoted + ` -pause seal -in "%1"`,
			quoted + ",0",
		},
		{
			decryptKey() + `\shell\open`,
			"",
			quoted + ` -pause open -in "%1"`,
			"",
		},
		{
			identityKey(),
			"New NoiseCrypt identity",
			quoted + " -pause keygen",
			quoted + ",0",
		},
	}

	for _, e := range entries {
		k, _, err := registry.CreateKey(registry.CURRENT_USER, e.key, registry.SET_VALUE)
		if err != nil {
			return fmt.Errorf("creating %s: %w", e.key, err)
		}
		if e.name != "" {
			if err := k.SetStringValue("", e.name); err != nil {
				k.Close()
				return err
			}
		}
		if e.icon != "" {
			if err := k.SetStringValue("Icon", e.icon); err != nil {
				k.Close()
				return err
			}
		}
		k.Close()

		cmd, _, err := registry.CreateKey(registry.CURRENT_USER, e.key+`\command`, registry.SET_VALUE)
		if err != nil {
			return fmt.Errorf("creating %s\\command: %w", e.key, err)
		}
		if err := cmd.SetStringValue("", e.command); err != nil {
			cmd.Close()
			return err
		}
		cmd.Close()
	}

	// The description Explorer shows for the file type, and the extension pointing at it.
	desc, _, err := registry.CreateKey(registry.CURRENT_USER, decryptKey(), registry.SET_VALUE)
	if err != nil {
		return err
	}
	_ = desc.SetStringValue("", "NoiseCrypt encrypted container")
	desc.Close()

	// What a .ncry looks like in Explorer. Distinct from the Icon values set on the
	// verbs above, which only decorate the menu entries: without DefaultIcon on the
	// ProgID the files themselves stay blank white pages, which is the one place the
	// icon is actually load-bearing. A container is meant to be recognisable at a
	// glance, and an unrecognisable one invites double-clicking to find out.
	//
	// Index 1, not 0. The executable carries two icons: the tile at 0, which is the
	// program, and the page at 1, which is a document. Pointing a file type at index 0
	// would give every container the application's own icon, and then a folder of
	// containers looks like a folder of copies of the program.
	iconKey, _, err := registry.CreateKey(registry.CURRENT_USER, decryptKey()+`\DefaultIcon`, registry.SET_VALUE)
	if err != nil {
		return err
	}
	if err := iconKey.SetStringValue("", quoted+","+containerIconIndex); err != nil {
		iconKey.Close()
		return err
	}
	iconKey.Close()

	ext, _, err := registry.CreateKey(registry.CURRENT_USER, extensionKey(), registry.SET_VALUE)
	if err != nil {
		return err
	}
	if err := ext.SetStringValue("", progID); err != nil {
		ext.Close()
		return err
	}
	ext.Close()

	notifyShell()

	fmt.Fprintf(env.Stdout, "Shell integration registered, pointing at:\n  %s\n", exe)
	fmt.Fprintln(env.Stdout, "\n  Right-click any file      Encrypt with NoiseCrypt")
	fmt.Fprintln(env.Stdout, "  Double-click a .ncry      decrypts it")
	fmt.Fprintln(env.Stdout, "  Right-click in a folder   New NoiseCrypt identity")
	// The one real weakness of registering without an installer, said rather than
	// discovered: the path above is absolute, so it stops working silently if the
	// binary moves.
	fmt.Fprintln(env.Stdout, "\nThat path is recorded as it stands. Move or rename this program and the")
	fmt.Fprintln(env.Stdout, "entries stop working; run 'noisecrypt shell register' again to repair them.")
	return nil
}

func shellUnregister(env *Env) error {
	// Deepest first: a key with subkeys cannot be deleted.
	for _, key := range []string{
		encryptKey() + `\command`, encryptKey(),
		identityKey() + `\command`, identityKey(),
		decryptKey() + `\shell\open\command`, decryptKey() + `\shell\open`,
		decryptKey() + `\shell`, decryptKey() + `\DefaultIcon`, decryptKey(),
		extensionKey(),
	} {
		if err := registry.DeleteKey(registry.CURRENT_USER, key); err != nil && !os.IsNotExist(err) {
			// Reported and not fatal. Leaving one stale key behind is untidy; giving
			// up halfway through leaves the integration in a state where half of it
			// still points at a program the user is uninstalling.
			fmt.Fprintf(env.Stderr, "  could not remove %s: %v\n", key, err)
		}
	}
	notifyShell()
	fmt.Fprintln(env.Stdout, "Shell integration removed.")
	return nil
}

func shellStatus(env *Env) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, encryptKey()+`\command`, registry.QUERY_VALUE)
	if err != nil {
		fmt.Fprintln(env.Stdout, "Shell integration is not registered.")
		fmt.Fprintln(env.Stdout, "Run 'noisecrypt shell register' to add it.")
		return nil
	}
	defer k.Close()

	command, _, err := k.GetStringValue("")
	if err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "Shell integration is registered:\n  %s\n", command)

	// Checking that the recorded program still exists is the point of having a status
	// command at all: the failure this guards against is silent everywhere else.
	if path := commandPath(command); path != "" {
		if _, err := os.Stat(path); err != nil {
			fmt.Fprintf(env.Stdout, "\n  WARNING: %s no longer exists.\n", path)
			fmt.Fprintln(env.Stdout, "  The menu entries will do nothing. Run 'noisecrypt shell register' to repair.")
		} else {
			fmt.Fprintln(env.Stdout, "\n  The program it points at exists.")
		}
	}
	return nil
}

// commandPath pulls the executable out of a quoted command line.
func commandPath(command string) string {
	if len(command) < 2 || command[0] != '"' {
		return ""
	}
	if end := indexByteFrom(command, '"', 1); end > 1 {
		return command[1:end]
	}
	return ""
}

func indexByteFrom(s string, b byte, from int) int {
	for i := from; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}
