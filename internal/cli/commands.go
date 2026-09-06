package cli

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/bzhzion/noisecrypt/internal/container"
	"github.com/bzhzion/noisecrypt/internal/crypt"
	"github.com/bzhzion/noisecrypt/internal/profile"
)

// maxInputSize bounds what the tool will read into memory in one go. The pipeline
// above needs the whole payload resident to interleave it across frames, so this is
// a real constraint rather than an arbitrary one.
const maxInputSize = 8 << 30 // 8 GiB

func runKeygen(env *Env, args []string) error {
	fs := newFlagSet(env, "keygen", "[flags]")
	out := fs.String("out", "", "write the private identity to this file instead of standard output")
	force := fs.Bool("force", false, "overwrite the output file if it already exists")
	if err := fs.Parse(args); err != nil {
		return err
	}

	id, err := crypt.GenerateIdentity()
	if err != nil {
		return err
	}

	if *out == "" {
		// Printing a private key to a terminal is a footgun, so say so on stderr
		// while the key itself goes to stdout, where a redirect can catch it.
		fmt.Fprintln(env.Stderr, "Private identity follows on standard output. Store it somewhere only you can read.")
		fmt.Fprintln(env.Stdout, id.String())
		fmt.Fprintf(env.Stderr, "\nPublic identity (share this):\n%s\n", id.Public.String())
		return nil
	}

	// O_EXCL, not a stat-then-write: checking for existence first and creating
	// afterwards leaves a window where another process wins the race, and silently
	// overwriting somebody's only copy of a private key is unrecoverable.
	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if *force {
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	f, err := os.OpenFile(*out, flags, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%s already exists; pass -force to overwrite it", *out)
		}
		return err
	}
	defer f.Close()

	if _, err := fmt.Fprintln(f, id.String()); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}

	fmt.Fprintf(env.Stdout, "Private identity written to %s (mode 0600).\n", *out)
	fmt.Fprintf(env.Stdout, "Public identity (share this):\n%s\n", id.Public.String())
	return nil
}

func runSeal(env *Env, args []string) error {
	fs := newFlagSet(env, "seal", "-in FILE [-out FILE] [-to IDENTITY | passphrase flags]")
	in := fs.String("in", "", "file to encrypt (required)")
	out := fs.String("out", "", "container to write (default: the input name with .ncry appended)")
	to := fs.String("to", "", "recipient public identity, or a file containing one; omit to seal with a passphrase")
	noCompress := fs.Bool("no-compress", false, "skip compression (use for already-compressed input)")
	signWith := fs.String("sign", "", "sign the container with this private identity, or a file containing one")
	chunk := fs.Uint("chunk-size", uint(crypt.DefaultChunkSize), "plaintext bytes per encrypted chunk")
	kdfTime := fs.Uint("kdf-time", uint(crypt.DefaultArgonTime), "Argon2id passes (passphrase mode)")
	kdfMemory := fs.Uint("kdf-memory", uint(crypt.DefaultArgonMemory), "Argon2id memory in KiB (passphrase mode)")
	kdfLanes := fs.Uint("kdf-lanes", uint(crypt.DefaultArgonLanes), "Argon2id lanes (passphrase mode)")
	force := fs.Bool("force", false, "overwrite the output file if it already exists")
	pass := &passphraseSource{confirm: true}
	pass.register(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *in == "" {
		fs.Usage()
		return errors.New("-in is required")
	}

	if _, err := narrow[uint32]("chunk-size", *chunk, math.MaxUint32); err != nil {
		return err
	}
	kdf, err := kdfFromFlags(*kdfTime, *kdfMemory, *kdfLanes)
	if err != nil {
		return err
	}

	sealed, plainSize, err := sealFile(env, *in, sealOptions{
		to: *to, signWith: *signWith, noCompress: *noCompress, kdf: kdf,
	}, pass)
	if err != nil {
		return err
	}

	target := *out
	if target == "" {
		target = *in + ".ncry"
	}
	if err := writeOutput(target, sealed, *force, 0o600); err != nil {
		return err
	}

	fmt.Fprintf(env.Stdout, "Sealed %s (%s) into %s (%s).\n",
		*in, humanBytes(plainSize), target, humanBytes(int64(len(sealed))))
	reportVideoCost(env, plainSize, int64(len(sealed)))
	return nil
}

func runOpen(env *Env, args []string) error {
	fs := newFlagSet(env, "open", "-in FILE [-out FILE] [-identity FILE | passphrase flags]")
	in := fs.String("in", "", "container to decrypt (required)")
	out := fs.String("out", "", "file to write (default: the name stored inside the container)")
	identity := fs.String("identity", "", "private identity, or a file containing one")
	from := fs.String("from", "", "require that the container be signed by this public identity")
	requireSig := fs.Bool("require-signature", false, "refuse a container that carries no signature")
	force := fs.Bool("force", false, "overwrite the output file if it already exists")
	pass := &passphraseSource{}
	pass.register(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *in == "" {
		fs.Usage()
		return errors.New("-in is required")
	}

	sealed, err := readInput(*in)
	if err != nil {
		return err
	}

	opened, err := openSealed(env, sealed, openOptions{
		identity: *identity, from: *from, requireSignature: *requireSig,
	}, pass)
	if err != nil {
		return err
	}

	target := *out
	if target == "" {
		// The stored name is already sanitised to a base name by the container
		// layer, so it cannot escape the current directory.
		target = opened.Name
	}
	if err := writeOutput(target, opened.Data, *force, 0o600); err != nil {
		return err
	}

	if !opened.ModTime.IsZero() {
		// Best effort: failing to restore a timestamp is not worth failing the
		// whole extraction over.
		_ = os.Chtimes(target, opened.ModTime, opened.ModTime)
	}

	fmt.Fprintf(env.Stdout, "Recovered %s (%s) from %s.\n",
		target, humanBytes(int64(len(opened.Data))), *in)
	reportSigner(env, opened)
	return nil
}

func runEstimate(env *Env, args []string) error {
	fs := newFlagSet(env, "estimate", "-in FILE [-profile NAME]")
	in := fs.String("in", "", "file to measure (required)")
	name := fs.String("profile", "", "report only this profile instead of all of them")
	noCompress := fs.Bool("no-compress", false, "assume compression is skipped")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *in == "" {
		fs.Usage()
		return errors.New("-in is required")
	}

	data, err := readInput(*in)
	if err != nil {
		return err
	}

	// Pack for real rather than guessing at the compression ratio. Guessing is how
	// an estimate ends up wrong by a factor of five on exactly the inputs where
	// being wrong matters, namely already-compressed archives.
	compression := container.CompressionGzip
	if *noCompress {
		compression = container.CompressionNone
	}
	packed, err := container.Pack(container.Metadata{Name: filepath.Base(*in), Compression: compression}, data)
	if err != nil {
		return err
	}

	// Sealing adds the header plus one tag per chunk. Compute it rather than
	// sealing, so estimate never has to ask for a passphrase.
	sealed := int64(len(packed)) + sealOverhead(int64(len(packed)), crypt.DefaultChunkSize)

	profiles := profile.All()
	if *name != "" {
		p, err := profile.Lookup(*name)
		if err != nil {
			return err
		}
		profiles = []profile.Profile{p}
	}

	fmt.Fprintf(env.Stdout, "%s: %s on disk, %s after packing and sealing\n\n",
		*in, humanBytes(int64(len(data))), humanBytes(sealed))

	for _, p := range profiles {
		e := p.Estimate(int64(len(data)), sealed)
		fmt.Fprintf(env.Stdout, "  %-8s %dx%d, %d px cells, %d levels, %.0f%% parity\n",
			p.Name, p.Width, p.Height, p.CellSize, p.Levels, p.Redundancy()*100)
		fmt.Fprintf(env.Stdout, "           %s per frame, %s/s\n",
			humanBytes(int64(p.PayloadBytesPerFrame())), humanBytes(int64(e.BytesPerSec)))
		fmt.Fprintf(env.Stdout, "           %d frames, %s of video at %d fps\n",
			e.Frames, humanDuration(e.Duration), p.FPS)
		fmt.Fprintf(env.Stdout, "           measured: %s (%s)\n",
			p.Evidence, p.EvidenceNote)
		fmt.Fprintln(env.Stdout)
	}

	return nil
}

func runProfiles(env *Env, args []string) error {
	fs := newFlagSet(env, "profiles", "")
	if err := fs.Parse(args); err != nil {
		return err
	}

	for _, p := range profile.All() {
		fmt.Fprintf(env.Stdout, "%s\n  %s\n", p.Name, p.Summary)
		fmt.Fprintf(env.Stdout, "  %dx%d at %d fps, %d px cells, %d levels, %.0f%% parity\n",
			p.Width, p.Height, p.FPS, p.CellSize, p.Levels, p.Redundancy()*100)
		fmt.Fprintf(env.Stdout, "  %s per frame, %s/s\n",
			humanBytes(int64(p.PayloadBytesPerFrame())),
			humanBytes(int64(p.PayloadBytesPerFrame()*p.FPS)))
		fmt.Fprintf(env.Stdout, "  measured: %s (%s)\n\n", p.Evidence, p.EvidenceNote)
	}
	return nil
}

// sealOverhead returns how many bytes sealing adds: the header plus one
// authentication tag and one length prefix per chunk.
func sealOverhead(payload int64, chunkSize int) int64 {
	chunks := (payload + int64(chunkSize) - 1) / int64(chunkSize)
	if chunks == 0 {
		chunks = 1
	}
	// The passphrase header is the smaller of the two; using it keeps the estimate
	// from over-reporting for the common case.
	const headerBytes = 56
	return headerBytes + chunks*int64(4+crypt.TagSize)
}

func reportVideoCost(env *Env, input, sealed int64) {
	fmt.Fprintln(env.Stdout, "\nVideo cost, if encoded:")
	for _, p := range profile.All() {
		e := p.Estimate(input, sealed)
		fmt.Fprintf(env.Stdout, "  %-8s %d frames, %s at %dx%d\n",
			p.Name, e.Frames, humanDuration(e.Duration), p.Width, p.Height)
	}
}

func readInput(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("%s is a directory", path)
	}
	if info.Size() > maxInputSize {
		return nil, fmt.Errorf("%s is %s, above the %s ceiling this build reads into memory",
			path, humanBytes(info.Size()), humanBytes(maxInputSize))
	}
	return os.ReadFile(path)
}

func writeOutput(path string, data []byte, force bool, mode os.FileMode) error {
	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if force {
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, mode)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%s already exists; pass -force to overwrite it", path)
		}
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// resolveRecipient accepts either a public identity token or a path to a file
// containing one, so a caller does not have to care which they have.
func resolveRecipient(s string) (crypt.PublicIdentity, error) {
	if id, err := crypt.ParsePublicIdentity(s); err == nil {
		return id, nil
	}
	b, err := os.ReadFile(s)
	if err != nil {
		return crypt.PublicIdentity{}, fmt.Errorf("%q is neither a public identity nor a readable file", s)
	}
	return crypt.ParsePublicIdentity(strings.TrimSpace(string(b)))
}

func resolveIdentity(s string) (*crypt.PrivateIdentity, error) {
	if id, err := crypt.ParsePrivateIdentity(s); err == nil {
		return id, nil
	}
	b, err := os.ReadFile(s)
	if err != nil {
		return nil, fmt.Errorf("%q is neither a private identity nor a readable file", s)
	}
	return crypt.ParsePrivateIdentity(strings.TrimSpace(string(b)))
}
