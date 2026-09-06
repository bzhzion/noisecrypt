//go:build windows

package cli

import (
	"fmt"
	"os/exec"
	"os/user"
)

// restrictToOwner makes a file or directory readable by its owner alone.
//
// This exists because `os.OpenFile(path, flags, 0o600)` is a lie on Windows. The mode
// controls the read-only attribute and nothing else: no access control entry is written,
// so the file keeps whatever it inherited from its parent. Measured on a real key written
// by this tool before the change, the result was
//
//	NT AUTHORITY\SYSTEM        FullControl
//	BUILTIN\Administrators     FullControl
//	AzureAD\<user>             FullControl
//
// which is to say the code announced a protection it had not applied. That was worth
// fixing on its own, and it was the precondition for keeping keys anywhere predictable.
//
// icacls rather than a golang.org/x/sys/windows security descriptor: the same result in
// one line the reader can verify by hand, against an API where a mistake produces a file
// that looks protected and is not. Administrators and SYSTEM are removed too, which is
// what OpenSSH does with its own key files, and which is honest about what it buys: it
// stops another ordinary account reading the file, and stops nothing at all against
// somebody who can already elevate on this machine.
func restrictToOwner(path string) error {
	// The account is named by its SID rather than by its name. `%USERNAME%` would have
	// needed a shell to expand, and a name needs a domain prefix that differs between a
	// local, a domain and an Entra joined account. The SID is the same string everywhere
	// and is what the access control entry stores anyway.
	me, err := user.Current()
	if err != nil {
		return fmt.Errorf("cannot identify the current user: %w", err)
	}
	owner := "*" + me.Uid

	steps := [][]string{
		// Break inheritance first, keeping the inherited entries as explicit ones, or
		// there would be a moment with no entries at all.
		{path, "/inheritance:d"},
		{path, "/remove:g", "*S-1-5-32-544"}, // Administrators
		{path, "/remove:g", "*S-1-5-18"},     // SYSTEM
		{path, "/remove:g", "*S-1-5-32-545"}, // Users
		{path, "/grant:r", owner + ":(F)"},
	}
	for _, args := range steps {
		out, err := exec.Command("icacls", args...).CombinedOutput()
		if err != nil {
			// A removal that finds nothing to remove reports failure, which is not one.
			// Only the grant is load-bearing, so that is the one that must succeed.
			if args[1] == "/grant:r" {
				return fmt.Errorf("icacls %v: %v: %s", args[1:], err, out)
			}
		}
	}
	return nil
}
