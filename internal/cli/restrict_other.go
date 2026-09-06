//go:build !windows

package cli

import (
	"os"
)

// restrictToOwner makes a file or directory readable by its owner alone.
//
// Away from Windows the mode passed to OpenFile already means this, so the call is only
// here to repair a file that predates the convention or that a permissive umask widened.
// It is not a no-op for the same reason the Windows version is not decoration: a key
// whose permissions were never checked is a key whose permissions are unknown.
func restrictToOwner(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	mode := os.FileMode(0o600)
	if info.IsDir() {
		mode = 0o700
	}
	if info.Mode().Perm() == mode {
		return nil
	}
	return os.Chmod(path, mode)
}
