package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Where an identity lives when nobody says otherwise.
//
// `~/.noisecrypt`, the same on every platform, chosen over the per-platform
// configuration directory for one reason that outweighs convention: a private identity
// is the one file in this system whose loss is unrecoverable, and the user is told to
// back it up. A directory they can find is a directory they can copy. Buried under
// AppData or Library, it is a key that quietly disappears with the machine.
//
// Deliberately not the Windows roaming profile, which `os.UserConfigDir` would have
// given: on a domain or Entra joined machine that directory synchronises to a server, and
// a private key that copies itself elsewhere without being asked is not what anyone
// wanted from a default.
const (
	storeDirName    = ".noisecrypt"
	identityBase    = "identity"
	storeDirEnvVar  = "NOISECRYPT_HOME"
	identityEnvVar  = "NOISECRYPT_IDENTITY"
	storeDirPermMax = 0o700
)

// StoreDir is the directory identities are kept in.
func StoreDir() (string, error) {
	if custom := os.Getenv(storeDirEnvVar); custom != "" {
		return custom, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot locate your home directory: %w", err)
	}
	return filepath.Join(home, storeDirName), nil
}

// DefaultIdentityPath is the file consulted when no identity is named.
func DefaultIdentityPath() (string, error) {
	if custom := os.Getenv(identityEnvVar); custom != "" {
		return custom, nil
	}
	dir, err := StoreDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, identityBase), nil
}

// writeIdentityFile stores an identity, creating the directory if needed and refusing to
// overwrite unless told to.
//
// O_EXCL rather than a stat followed by a write: checking first leaves a window another
// process can win, and silently replacing somebody's only copy of a private key cannot
// be undone.
func writeIdentityFile(path, contents string, force bool) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, storeDirPermMax); err != nil {
			return err
		}
		if err := restrictToOwner(dir); err != nil {
			return fmt.Errorf("securing %s: %w", dir, err)
		}
	}

	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if force {
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%s already exists; pass -force to overwrite it", path)
		}
		return err
	}
	if _, err := fmt.Fprintln(f, contents); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}

	// The mode above is a request that Windows does not honour, so this is where the
	// file actually becomes private on that platform. Done after writing rather than
	// before, since the permissions have to survive the write.
	return restrictToOwner(path)
}
