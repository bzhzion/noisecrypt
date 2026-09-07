package keystore

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
// # The two file names, and why the public one exists at all
//
// The public identity is *derived* from the private one, so writing it to disk stores
// nothing that could not be recomputed. It is written anyway, for the reason SSH writes
// `id_ed25519.pub` next to `id_ed25519`: the public identity is the one thing the user is
// meant to hand out, and a value that exists only in the output of the command that
// created it is a value nobody has an hour later.
//
// That was not a hypothesis. The first version printed the public identity once, from
// `keygen`, into a console that closes on its own, and provided no way to ever see it
// again. The private key was safe and the user could still decrypt; they simply had no way
// to tell anyone how to encrypt to them, short of regenerating and losing access to
// everything already sealed to the old identity. The only shareable half was
// write-once.
//
// # And why there is no fingerprint file
//
// A fingerprint verifies that a public identity is the right one, by being compared over a
// *different* channel: read aloud, sent by another medium, checked against a business
// card. Written into the same directory as the key it describes, it verifies nothing at
// all, because whoever can replace the `.ncrypub` can replace the fingerprint beside it in
// the same gesture. It would look like a protection while being none, which is worse than
// its absence. Fingerprints are therefore printed and displayed, never stored.
const (
	StoreDirName = ".noisecrypt"
	IdentityBase = "identity"

	// Distinct final extensions rather than `.priv.ncrykey` / `.pub.ncrykey`: Windows
	// reads only the last segment, so both would be the same file type, share one icon
	// and one association. A secret and a thing meant to be published should not look
	// alike in a file listing.
	PrivateExt = ".ncrykey"
	PublicExt  = ".ncrypub"

	StoreDirEnvVar  = "NOISECRYPT_HOME"
	IdentityEnvVar  = "NOISECRYPT_IDENTITY"
	storeDirPermMax = 0o700
)

// PublicPathFor returns the public file that belongs beside a private identity file.
//
// Derived from the private path rather than stored, so the pair cannot drift apart, and
// tolerant of the legacy extensionless name so an identity created before this existed
// still gets its public half in the right place.
func PublicPathFor(private string) string {
	if ext := filepath.Ext(private); ext == PrivateExt {
		return private[:len(private)-len(ext)] + PublicExt
	}
	return private + PublicExt
}

// StoreDir is the directory identities are kept in.
func StoreDir() (string, error) {
	if custom := os.Getenv(StoreDirEnvVar); custom != "" {
		return custom, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot locate your home directory: %w", err)
	}
	return filepath.Join(home, StoreDirName), nil
}

// DefaultIdentityPath is the file consulted when no identity is named.
//
// The extensionless name written by earlier versions is still honoured when it is the one
// present, so an identity created before the extension existed keeps working without being
// touched. New identities get `identity.ncrykey`. A rename would have been tidier and would
// also have been us moving somebody's only copy of a private key to satisfy a naming
// preference, which is not a trade worth making.
func DefaultIdentityPath() (string, error) {
	if custom := os.Getenv(IdentityEnvVar); custom != "" {
		return custom, nil
	}
	dir, err := StoreDir()
	if err != nil {
		return "", err
	}
	avecExt := filepath.Join(dir, IdentityBase+PrivateExt)
	if _, err := os.Stat(avecExt); err == nil {
		return avecExt, nil
	}
	legacy := filepath.Join(dir, IdentityBase)
	if _, err := os.Stat(legacy); err == nil {
		return legacy, nil
	}
	// Ni l'un ni l'autre : c'est une creation, donc le nom courant.
	return avecExt, nil
}

// WriteIdentityFile stores an identity, creating the directory if needed and refusing to
// overwrite unless told to.
//
// O_EXCL rather than a stat followed by a write: checking first leaves a window another
// process can win, and silently replacing somebody's only copy of a private key cannot
// be undone.
func WriteIdentityFile(path, contents string, force bool) error {
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
