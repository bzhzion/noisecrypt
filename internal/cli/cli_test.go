package cli

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bzhzion/noisecrypt/internal/container"
	"github.com/bzhzion/noisecrypt/internal/crypt"
)

// testEnv drives the CLI without touching the real terminal.
type testEnv struct {
	*Env
	stdout *bytes.Buffer
	stderr *bytes.Buffer
}

func newTestEnv(passphrase string) *testEnv {
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	return &testEnv{
		Env: &Env{
			Stdout: out,
			Stderr: errOut,
			Stdin:  strings.NewReader(""),
			ReadPassphrase: func(string) ([]byte, error) {
				return []byte(passphrase), nil
			},
		},
		stdout: out,
		stderr: errOut,
	}
}

func (e *testEnv) run(t *testing.T, args ...string) {
	t.Helper()
	if code := Run(e.Env, args); code != 0 {
		t.Fatalf("noisecrypt %s exited %d\nstdout: %s\nstderr: %s",
			strings.Join(args, " "), code, e.stdout, e.stderr)
	}
}

func (e *testEnv) runExpectingFailure(t *testing.T, args ...string) {
	t.Helper()
	if code := Run(e.Env, args); code == 0 {
		t.Fatalf("noisecrypt %s unexpectedly succeeded\nstdout: %s", strings.Join(args, " "), e.stdout)
	}
}

func writeFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return path
}

// sampleFiles covers the input kinds the tool is expected to carry. The format is
// byte oriented and type agnostic by construction, so these are regression fixtures
// rather than special cases: they exist so that a future change which breaks, say,
// binary input with embedded nulls fails here instead of in production.
func sampleFiles(t *testing.T) map[string][]byte {
	t.Helper()

	binary := make([]byte, 200*1024)
	if _, err := rand.Read(binary); err != nil {
		t.Fatalf("reading randomness: %v", err)
	}

	// A minimal but structurally real PDF: binary header comment, null bytes, and
	// a trailer. PDFs are the input most likely to expose a byte-transparency bug.
	pdf := []byte("%PDF-1.7\n%\xE2\xE3\xCF\xD3\n1 0 obj\n<< /Type /Catalog >>\nendobj\n" +
		"\x00\x01\x02\xFF\xFE\n" +
		"trailer\n<< /Root 1 0 R >>\n%%EOF\n")

	return map[string][]byte{
		"notes.txt":   []byte("plain text, with a trailing newline\n"),
		"readme.md":   []byte("# Title\n\n- bullet\n- another\n\n```go\nfmt.Println(\"hi\")\n```\n"),
		"config.json": []byte(`{"key":"value","nested":{"list":[1,2,3],"unicode":"éàü 안녕"}}`),
		"report.pdf":  pdf,
		"payload.bin": binary,
		"empty.dat":   {},
	}
}

// TestRoundTripEveryFileKindPassphrase seals and opens each sample file and
// requires byte-for-byte equality.
func TestRoundTripEveryFileKindPassphrase(t *testing.T) {
	for name, data := range sampleFiles(t) {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			src := writeFile(t, dir, name, data)
			sealed := filepath.Join(dir, name+".ncry")
			out := filepath.Join(dir, "recovered-"+name)

			env := newTestEnv("a decent passphrase")
			env.run(t, "seal", "-in", src, "-out", sealed,
				"-kdf-time", "1", "-kdf-memory", "8", "-kdf-lanes", "1")
			env.run(t, "open", "-in", sealed, "-out", out)

			got, err := os.ReadFile(out)
			if err != nil {
				t.Fatalf("reading recovered file: %v", err)
			}
			if !bytes.Equal(got, data) {
				t.Fatalf("%s did not survive the round trip: got %d bytes, want %d", name, len(got), len(data))
			}
		})
	}
}

// TestRoundTripHybridViaKeygen exercises the recipient path exactly as a user
// would: generate an identity to a file, seal to its public half, open with it.
func TestRoundTripHybridViaKeygen(t *testing.T) {
	dir := t.TempDir()
	data := []byte("addressed to a specific identity")
	src := writeFile(t, dir, "message.txt", data)
	keyFile := filepath.Join(dir, "identity.key")

	env := newTestEnv("")
	env.run(t, "keygen", "-out", keyFile)

	// The public identity is printed; pull it out of stdout the way a user would
	// copy it out of their terminal.
	var pub string
	for _, line := range strings.Split(env.stdout.String(), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "noisecrypt-public-v1:") {
			pub = strings.TrimSpace(line)
		}
	}
	if pub == "" {
		t.Fatalf("keygen did not print a public identity:\n%s", env.stdout)
	}

	sealed := filepath.Join(dir, "message.ncry")
	out := filepath.Join(dir, "recovered.txt")

	env.run(t, "seal", "-in", src, "-out", sealed, "-to", pub)
	env.run(t, "open", "-in", sealed, "-out", out, "-identity", keyFile)

	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("reading recovered file: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("hybrid round trip did not preserve the data")
	}
}

// TestKeygenRefusesToClobber guards the one mistake that cannot be undone:
// overwriting the only copy of a private key.
func TestKeygenRefusesToClobber(t *testing.T) {
	dir := t.TempDir()
	keyFile := writeFile(t, dir, "identity.key", []byte("existing key material"))

	env := newTestEnv("")
	env.runExpectingFailure(t, "keygen", "-out", keyFile)

	got, err := os.ReadFile(keyFile)
	if err != nil {
		t.Fatalf("reading the key file: %v", err)
	}
	if string(got) != "existing key material" {
		t.Fatal("keygen overwrote an existing key file")
	}

	// With -force it must go through, because refusing unconditionally would make
	// key rotation impossible.
	env.run(t, "keygen", "-out", keyFile, "-force")
}

func TestSealRefusesToClobber(t *testing.T) {
	dir := t.TempDir()
	src := writeFile(t, dir, "a.txt", []byte("data"))
	sealed := writeFile(t, dir, "a.ncry", []byte("do not lose me"))

	env := newTestEnv("passphrase")
	env.runExpectingFailure(t, "seal", "-in", src, "-out", sealed,
		"-kdf-time", "1", "-kdf-memory", "8", "-kdf-lanes", "1")

	got, _ := os.ReadFile(sealed)
	if string(got) != "do not lose me" {
		t.Fatal("seal overwrote an existing file")
	}
}

func TestOpenWithWrongPassphraseFails(t *testing.T) {
	dir := t.TempDir()
	src := writeFile(t, dir, "a.txt", []byte("data"))
	sealed := filepath.Join(dir, "a.ncry")

	sealer := newTestEnv("right passphrase")
	sealer.run(t, "seal", "-in", src, "-out", sealed,
		"-kdf-time", "1", "-kdf-memory", "8", "-kdf-lanes", "1")

	opener := newTestEnv("wrong passphrase")
	opener.runExpectingFailure(t, "open", "-in", sealed, "-out", filepath.Join(dir, "out.txt"))
}

func TestOpenRestoresTheStoredName(t *testing.T) {
	dir := t.TempDir()
	src := writeFile(t, dir, "original-name.txt", []byte("data"))
	sealed := filepath.Join(dir, "sealed.ncry")

	env := newTestEnv("passphrase")
	env.run(t, "seal", "-in", src, "-out", sealed,
		"-kdf-time", "1", "-kdf-memory", "8", "-kdf-lanes", "1")

	// Open without -out, in a fresh directory, so the name has to come from inside
	// the container.
	work := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(work); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	env.run(t, "open", "-in", sealed)

	if _, err := os.Stat(filepath.Join(work, "original-name.txt")); err != nil {
		t.Fatalf("the stored name was not used: %v", err)
	}
}

func TestPassphraseFromFile(t *testing.T) {
	dir := t.TempDir()
	src := writeFile(t, dir, "a.txt", []byte("scripted"))
	passFile := writeFile(t, dir, "pass.txt", []byte("from a file\n"))
	sealed := filepath.Join(dir, "a.ncry")
	out := filepath.Join(dir, "out.txt")

	// No terminal reader at all: this must work headless, which is the whole point
	// of having a real command line.
	env := newTestEnv("")
	env.ReadPassphrase = nil

	env.run(t, "seal", "-in", src, "-out", sealed, "-passphrase-file", passFile,
		"-kdf-time", "1", "-kdf-memory", "8", "-kdf-lanes", "1")
	env.run(t, "open", "-in", sealed, "-out", out, "-passphrase-file", passFile)

	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("reading recovered file: %v", err)
	}
	if string(got) != "scripted" {
		t.Fatalf("got %q", got)
	}
}

// TestPassphraseFileTrailingNewline pins a detail that silently breaks scripts:
// `echo secret > pass.txt` appends a newline, and a tool that keeps it produces
// containers that the same command cannot open on another platform.
func TestPassphraseFileTrailingNewline(t *testing.T) {
	dir := t.TempDir()
	src := writeFile(t, dir, "a.txt", []byte("data"))
	unix := writeFile(t, dir, "unix.txt", []byte("un secret assez long\n"))
	windows := writeFile(t, dir, "windows.txt", []byte("un secret assez long\r\n"))
	sealed := filepath.Join(dir, "a.ncry")
	out := filepath.Join(dir, "out.txt")

	env := newTestEnv("")
	env.ReadPassphrase = nil

	env.run(t, "seal", "-in", src, "-out", sealed, "-passphrase-file", unix,
		"-kdf-time", "1", "-kdf-memory", "8", "-kdf-lanes", "1")
	env.run(t, "open", "-in", sealed, "-out", out, "-passphrase-file", windows)
}

// TestPassphraseFloorAppliesToSealingOnly covers a finding from the 2026-09-05 audit.
// A one-character passphrase used to be accepted silently, which makes the Argon2id
// cost irrelevant: a work factor multiplies the price of searching a keyspace, it does
// not create one.
//
// The floor must not apply when opening, or a container sealed elsewhere, or before
// this check existed, would become unreadable.
func TestPassphraseFloorAppliesToSealingOnly(t *testing.T) {
	dir := t.TempDir()
	src := writeFile(t, dir, "note.txt", []byte("data"))
	short := writeFile(t, dir, "short.txt", []byte("abc"))
	long := writeFile(t, dir, "long.txt", []byte("une phrase de passe correcte"))
	sealed := filepath.Join(dir, "a.ncry")

	env := newTestEnv("")
	env.ReadPassphrase = nil

	// Sealing with a short passphrase must be refused, and must say why.
	env.runExpectingFailure(t, "seal", "-in", src, "-out", sealed, "-passphrase-file", short,
		"-kdf-time", "1", "-kdf-memory", "8", "-kdf-lanes", "1")
	if !strings.Contains(env.stderr.String(), "minimum for sealing") {
		t.Fatalf("the refusal does not explain itself: %s", env.stderr)
	}
	if _, err := os.Stat(sealed); err == nil {
		t.Fatal("a container was written despite the refusal")
	}

	// A long one goes through.
	env.run(t, "seal", "-in", src, "-out", sealed, "-passphrase-file", long,
		"-kdf-time", "1", "-kdf-memory", "8", "-kdf-lanes", "1")

	// Opening is never subject to the floor. Build a container with a deliberately
	// short passphrase through the library, then open it through the CLI.
	legacy := filepath.Join(dir, "legacy.ncry")
	packed, err := container.Pack(container.Metadata{Name: "old.txt"}, []byte("ancien"))
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}
	blob, err := crypt.Seal(packed, crypt.SealOptions{
		Passphrase: []byte("abc"),
		KDF:        crypt.KDFParams{Time: 1, Memory: 8, Lanes: 1},
	})
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if err := os.WriteFile(legacy, blob, 0o600); err != nil {
		t.Fatalf("writing the container: %v", err)
	}

	env.run(t, "open", "-in", legacy, "-out", filepath.Join(dir, "old.txt"),
		"-passphrase-file", short)
}

// TestFlagsRefuseToTruncate covers the other 2026-09-05 finding. uint8(*flag) wraps
// silently, so -kdf-lanes 260 became 4 and the container was sealed at a cost the user
// never asked for and could not notice.
func TestFlagsRefuseToTruncate(t *testing.T) {
	dir := t.TempDir()
	src := writeFile(t, dir, "note.txt", []byte("data"))
	pass := writeFile(t, dir, "pass.txt", []byte("une phrase de passe correcte"))

	env := newTestEnv("")
	env.ReadPassphrase = nil

	for _, args := range [][]string{
		{"-kdf-lanes", "260"},
		{"-kdf-lanes", "4294967296"},
	} {
		full := append([]string{"seal", "-in", src, "-out", filepath.Join(dir, "x.ncry"),
			"-passphrase-file", pass, "-kdf-time", "1", "-kdf-memory", "8"}, args...)
		env.stderr.Reset()
		env.runExpectingFailure(t, full...)
		if !strings.Contains(env.stderr.String(), "the maximum is") {
			t.Fatalf("%v was not refused with a bound: %s", args, env.stderr)
		}
	}
}

func TestEstimateReportsEveryProfile(t *testing.T) {
	dir := t.TempDir()
	src := writeFile(t, dir, "a.bin", bytes.Repeat([]byte("x"), 100*1024))

	env := newTestEnv("")
	env.run(t, "estimate", "-in", src)

	out := env.stdout.String()
	for _, want := range []string{"archive", "social", "frames", "per frame"} {
		if !strings.Contains(out, want) {
			t.Fatalf("estimate output is missing %q:\n%s", want, out)
		}
	}
}

func TestUnknownCommandAndHelp(t *testing.T) {
	env := newTestEnv("")
	if code := Run(env.Env, []string{"frobnicate"}); code != 2 {
		t.Fatalf("unknown command exited %d, want 2", code)
	}
	if code := Run(env.Env, []string{"--help"}); code != 0 {
		t.Fatalf("--help exited %d, want 0", code)
	}
	if code := Run(env.Env, nil); code != 2 {
		t.Fatalf("no arguments exited %d, want 2", code)
	}
}

func TestMissingRequiredFlags(t *testing.T) {
	env := newTestEnv("passphrase")
	env.runExpectingFailure(t, "seal")
	env.runExpectingFailure(t, "open")
	env.runExpectingFailure(t, "estimate")
}
