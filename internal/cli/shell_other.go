//go:build !windows

package cli

import (
	"errors"
	"fmt"
)

// runShell exists away from Windows only to explain why it does nothing.
//
// A command that vanishes from the help on some platforms is a command whose absence
// looks like a packaging mistake, so it stays listed and says what it would do and why
// it cannot. Linux desktop integration is a real possibility (a .desktop file and a
// shared-mime-info entry), and it is not written yet rather than impossible.
func runShell(env *Env, args []string) error {
	fs := newFlagSet(env, "shell", "register | unregister | status")
	if err := fs.Parse(args); err != nil {
		return err
	}
	fmt.Fprintln(env.Stderr, "Shell integration is Windows only for now.")
	fmt.Fprintln(env.Stderr, "On Linux it would mean a .desktop entry and a shared-mime-info type for")
	fmt.Fprintln(env.Stderr, "application/x-noisecrypt; that is unwritten, not impossible.")
	return errors.New("not available on this platform")
}
