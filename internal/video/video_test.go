package video

import (
	"bytes"
	"context"
	"errors"
	"image"
	"os"
	"path/filepath"
	"testing"

	"github.com/bzhzion/noisecrypt/internal/codec"
	"github.com/bzhzion/noisecrypt/internal/container"
	"github.com/bzhzion/noisecrypt/internal/crypt"
	"github.com/bzhzion/noisecrypt/internal/profile"
)

// tools resolves FFmpeg, skipping unless RequireEnv is set. See its documentation
// for why the default is a skip and why CI does not accept one.
func tools(t *testing.T) Tools {
	t.Helper()
	tl, err := Find()
	if err != nil {
		if errors.Is(err, ErrNotInstalled) && os.Getenv(RequireEnv) == "" {
			t.Skipf("FFmpeg is not installed: %v", err)
		}
		t.Fatalf("Find: %v (%s is set, so this is a failure and not a skip)", err, RequireEnv)
	}
	return tl
}

func TestFindReportsWhereItLooked(t *testing.T) {
	// Not a skip: even when FFmpeg is present, a missing-tool error must name the
	// places it searched. "ffmpeg not found" with no list is the least useful error
	// message a tool can produce.
	if _, err := locate("definitely-not-a-real-binary-name"); err == nil {
		t.Fatal("locate found a binary that does not exist")
	} else if !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("expected ErrNotInstalled, got %v", err)
	} else if len(err.Error()) < 40 {
		t.Fatalf("the not-found error names nowhere useful: %q", err)
	}
}

// TestRealVideoRoundTrip is the end of the road: a file is sealed, drawn as frames,
// muxed into H.264, decoded back from the file, and opened. Everything before this
// point was measured against a simulated channel; this one is measured against
// x264.
func TestRealVideoRoundTrip(t *testing.T) {
	tl := tools(t)
	ctx := context.Background()

	c, err := codec.New(profile.Social)
	if err != nil {
		t.Fatalf("codec.New: %v", err)
	}

	original := bytes.Repeat([]byte("carried as video, recovered as bytes. "), 60)
	packed, err := container.Pack(container.Metadata{
		Name:        "message.txt",
		Compression: container.CompressionGzip,
	}, original)
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}
	sealed, err := crypt.Seal(packed, crypt.SealOptions{
		Passphrase: []byte("a passphrase"),
		KDF:        crypt.KDFParams{Time: 1, Memory: 8, Lanes: 1},
	})
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	// Render every frame up front. Only a couple of dozen at this payload size, and
	// holding them keeps the test readable.
	var frames []*image.Gray
	if err := c.EncodeTo(sealed, func(_ int, img *image.Gray) error {
		frames = append(frames, img)
		return nil
	}); err != nil {
		t.Fatalf("EncodeTo: %v", err)
	}

	path := filepath.Join(t.TempDir(), "out.mp4")
	next := 0
	err = Write(ctx, tl, WriteOptions{
		Path:   path,
		Width:  profile.Social.Width,
		Height: profile.Social.Height,
		FPS:    profile.Social.FPS,
		CRF:    23,
	}, func() (*image.Gray, error) {
		if next >= len(frames) {
			return nil, nil
		}
		img := frames[next]
		next++
		return img, nil
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	d := c.NewDecoder()
	info, err := Read(ctx, tl, path, func(img *image.Gray) error {
		d.Add(img)
		return nil
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	t.Logf("video is %dx%d, %d frames written, %d read, %d unreadable",
		info.Width, info.Height, len(frames), d.Seen, d.Unreadable)

	recovered, err := d.Finish()
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if !bytes.Equal(recovered, sealed) {
		t.Fatal("the sealed container did not survive H.264")
	}

	opened, err := crypt.Open(recovered, crypt.OpenOptions{Passphrase: []byte("a passphrase")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	meta, out, err := container.Unpack(opened)
	if err != nil {
		t.Fatalf("Unpack: %v", err)
	}
	if !bytes.Equal(out, original) {
		t.Fatal("the recovered file differs from the original")
	}
	if meta.Name != "message.txt" {
		t.Fatalf("recovered name %q", meta.Name)
	}
}

// TestSurvivesRecompression is the closest thing to a platform this test suite has
// without uploading anything: the produced video is re-encoded at a much lower
// quality, exactly as an ingest pipeline would, and must still decode.
func TestSurvivesRecompression(t *testing.T) {
	tl := tools(t)
	ctx := context.Background()

	c, err := codec.New(profile.Social)
	if err != nil {
		t.Fatalf("codec.New: %v", err)
	}
	data := bytes.Repeat([]byte("recompressed"), 200)

	var frames []*image.Gray
	if err := c.EncodeTo(data, func(_ int, img *image.Gray) error {
		frames = append(frames, img)
		return nil
	}); err != nil {
		t.Fatalf("EncodeTo: %v", err)
	}

	dir := t.TempDir()
	first := filepath.Join(dir, "first.mp4")
	next := 0
	if err := Write(ctx, tl, WriteOptions{
		Path: first, Width: profile.Social.Width, Height: profile.Social.Height,
		FPS: profile.Social.FPS, CRF: 20,
	}, func() (*image.Gray, error) {
		if next >= len(frames) {
			return nil, nil
		}
		img := frames[next]
		next++
		return img, nil
	}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// Read it back and write it out again at a punishing quality, which is what a
	// second pass through someone else's encoder amounts to.
	var pass1 []*image.Gray
	if _, err := Read(ctx, tl, first, func(img *image.Gray) error {
		pass1 = append(pass1, img)
		return nil
	}); err != nil {
		t.Fatalf("Read: %v", err)
	}

	second := filepath.Join(dir, "second.mp4")
	next = 0
	if err := Write(ctx, tl, WriteOptions{
		Path: second, Width: profile.Social.Width, Height: profile.Social.Height,
		FPS: profile.Social.FPS, CRF: 34, Preset: "veryfast",
	}, func() (*image.Gray, error) {
		if next >= len(pass1) {
			return nil, nil
		}
		img := pass1[next]
		next++
		return img, nil
	}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	d := c.NewDecoder()
	if _, err := Read(ctx, tl, second, func(img *image.Gray) error {
		d.Add(img)
		return nil
	}); err != nil {
		t.Fatalf("Read: %v", err)
	}

	got, err := d.Finish()
	if err != nil {
		t.Fatalf("after a second encode at CRF 34: %v (%d seen, %d unreadable)", err, d.Seen, d.Unreadable)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("the payload changed across a re-encode")
	}
	t.Logf("two H.264 passes, the second at CRF 34: %d frames, %d unreadable, payload recovered",
		d.Seen, d.Unreadable)
}

func TestProbeRejectsNonVideo(t *testing.T) {
	tl := tools(t)

	path := filepath.Join(t.TempDir(), "not-a-video.mp4")
	if err := writeFile(path, []byte("this is not an mp4")); err != nil {
		t.Fatalf("writing the file: %v", err)
	}
	if _, err := Probe(context.Background(), tl, path); err == nil {
		t.Fatal("Probe accepted a file that is not a video")
	}
}

func writeFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o600)
}
