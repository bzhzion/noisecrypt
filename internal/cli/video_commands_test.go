package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bzhzion/noisecrypt/internal/video"
)

// requireFFmpeg skips unless NOISECRYPT_REQUIRE_FFMPEG is set, in which case a
// missing FFmpeg is a failure. See video.RequireEnv: a skip nobody notices is how a
// suite ends up green while testing nothing.
func requireFFmpeg(t *testing.T) {
	t.Helper()
	if _, err := video.Find(); err != nil {
		if errors.Is(err, video.ErrNotInstalled) && os.Getenv(video.RequireEnv) == "" {
			t.Skip("FFmpeg is not installed")
		}
		t.Fatalf("locating FFmpeg: %v", err)
	}
}

// TestEncodeDecodeThroughTheCLI covers the wiring rather than the codec: the codec
// is measured in its own package, and what can still be wrong here is a flag not
// reaching the layer that needs it. A profile silently defaulting on decode, for
// instance, produces a failure that looks like channel damage.
func TestEncodeDecodeThroughTheCLI(t *testing.T) {
	requireFFmpeg(t)

	for _, profileName := range []string{"archive", "social"} {
		t.Run(profileName, func(t *testing.T) {
			dir := t.TempDir()
			original := bytes.Repeat([]byte("through the command line. "), 40)
			src := writeFile(t, dir, "note.txt", original)
			passFile := writeFile(t, dir, "pass.txt", []byte("a passphrase\n"))
			mp4 := filepath.Join(dir, "out.mp4")
			back := filepath.Join(dir, "back.txt")

			env := newTestEnv("")
			env.ReadPassphrase = nil // headless on purpose

			env.run(t, "encode", "-in", src, "-out", mp4, "-profile", profileName,
				"-passphrase-file", passFile, "-kdf-time", "1", "-kdf-memory", "8", "-kdf-lanes", "1")

			if fi, err := os.Stat(mp4); err != nil || fi.Size() == 0 {
				t.Fatalf("encode produced no video: %v", err)
			}

			env.run(t, "decode", "-in", mp4, "-out", back, "-profile", profileName,
				"-passphrase-file", passFile)

			got, err := os.ReadFile(back)
			if err != nil {
				t.Fatalf("reading the recovered file: %v", err)
			}
			if !bytes.Equal(got, original) {
				t.Fatalf("recovered %d bytes, expected %d", len(got), len(original))
			}
		})
	}
}

// TestDecodeWithTheWrongProfileFails is the mistake a user will actually make, and
// the tool should not appear to hang or produce a corrupt file over it.
func TestDecodeWithTheWrongProfileFails(t *testing.T) {
	requireFFmpeg(t)

	dir := t.TempDir()
	src := writeFile(t, dir, "note.txt", []byte("mismatched profiles"))
	passFile := writeFile(t, dir, "pass.txt", []byte("a passphrase\n"))
	mp4 := filepath.Join(dir, "out.mp4")

	env := newTestEnv("")
	env.ReadPassphrase = nil

	env.run(t, "encode", "-in", src, "-out", mp4, "-profile", "archive",
		"-passphrase-file", passFile, "-kdf-time", "1", "-kdf-memory", "8", "-kdf-lanes", "1")

	env.runExpectingFailure(t, "decode", "-in", mp4, "-out", filepath.Join(dir, "back.txt"),
		"-profile", "social", "-passphrase-file", passFile)
}

func TestEncodeRejectsAnUnknownProfile(t *testing.T) {
	dir := t.TempDir()
	src := writeFile(t, dir, "note.txt", []byte("x"))

	env := newTestEnv("p")
	env.runExpectingFailure(t, "encode", "-in", src, "-profile", "tiktok")

	if !strings.Contains(env.stderr.String(), "unknown profile") {
		t.Fatalf("the error does not name the problem: %s", env.stderr)
	}
}

func TestParseInts(t *testing.T) {
	got, err := parseInts(" 20, 26 ,30 ")
	if err != nil {
		t.Fatalf("parseInts: %v", err)
	}
	if len(got) != 3 || got[0] != 20 || got[1] != 26 || got[2] != 30 {
		t.Fatalf("parseInts returned %v", got)
	}

	for _, bad := range []string{"", "   ", ",,,", "20,x"} {
		if _, err := parseInts(bad); err == nil {
			t.Errorf("parseInts accepted %q", bad)
		}
	}
}
