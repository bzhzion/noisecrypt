// Package video moves frames in and out of a real video file, through FFmpeg.
//
// # Raw greyscale, not a directory of PNGs
//
// The straightforward implementation writes numbered PNGs to a temporary directory
// and points FFmpeg at them. It also writes several gigabytes to disk for a payload
// that never needed to touch it, and then reads them all back. Frames here are pure
// greyscale with no alpha and no colour, so they go over a pipe as raw `gray` planes:
// one byte per pixel, no encoder, no decoder, no temporary files.
//
// The saving is not marginal. Forty megabytes through the archive profile is sixteen
// hundred frames of 1920 by 1080, which is three gigabytes of pixels either way.
//
// # The decoder must not assume the dimensions it wrote
//
// A video that has been through a platform comes back at whatever size the platform
// chose. Reading it back at the size we encoded would slice the pixel stream at the
// wrong offsets and produce garbage that looks like channel damage rather than like
// a bug. So the real dimensions are probed first, every time, and the geometry layer
// is handed whatever actually arrived.
package video

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

var (
	// ErrNotInstalled is returned when FFmpeg cannot be found.
	ErrNotInstalled = errors.New("video: ffmpeg not found")

	// ErrNoFrames is returned when a file yields no frames at all.
	ErrNoFrames = errors.New("video: no frames in input")
)

// RequireEnv names the environment variable that turns a missing FFmpeg from a
// skipped test into a failing one.
//
// It lives here rather than in the test file because the command line package needs
// it too, and a constant declared in a _test.go file is invisible outside its own
// package's test binary.
//
// The reasoning: tests that need an external program default to skipping, so a
// contributor without FFmpeg can still run everything else. But a skip nobody
// notices is how a suite ends up green while testing nothing, which is the exact
// failure this project was started in reaction to. CI installs FFmpeg and sets this,
// so a broken install there is reported rather than silently tolerated.
const RequireEnv = "NOISECRYPT_REQUIRE_FFMPEG"

// Tools holds the resolved paths to the two binaries.
type Tools struct {
	FFmpeg  string
	FFprobe string
}

// Find locates FFmpeg and FFprobe.
//
// PATH first, then the usual installed locations. The fallback list covers all three
// platforms on purpose: a tool that only knows where Windows package managers put
// things works on Linux exactly until someone installs FFmpeg somewhere other than
// /usr/bin, and then reports it as missing while it sits on the disk.
func Find() (Tools, error) {
	ffmpeg, err := locate("ffmpeg")
	if err != nil {
		return Tools{}, err
	}
	ffprobe, err := locate("ffprobe")
	if err != nil {
		return Tools{}, err
	}
	return Tools{FFmpeg: ffmpeg, FFprobe: ffprobe}, nil
}

func locate(name string) (string, error) {
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}

	var dirs, globs []string
	switch runtime.GOOS {
	case "windows":
		local := os.Getenv("LOCALAPPDATA")
		dirs = []string{
			`C:\ffmpeg\bin`,
			filepath.Join(local, `Microsoft\WinGet\Links`),
			filepath.Join(local, `Programs\ffmpeg\bin`),
			filepath.Join(os.Getenv("USERPROFILE"), `scoop\shims`),
			`C:\ProgramData\chocolatey\bin`,
		}
		// WinGet does not shim this package. `winget install Gyan.FFmpeg` succeeds,
		// reports success, adds nothing to PATH and creates no link, leaving the
		// binary under a directory whose name contains the FFmpeg version. A fixed
		// path cannot find it and never will, so this one is a glob. Verified on a
		// real install: the binary landed in
		// Packages\Gyan.FFmpeg_Microsoft.Winget.Source_8wekyb3d8bbwe\ffmpeg-9.0.1-full_build\bin.
		globs = []string{filepath.Join(local, `Microsoft\WinGet\Packages`, `*FFmpeg*`, `*`, `bin`)}
	case "darwin":
		dirs = []string{"/opt/homebrew/bin", "/usr/local/bin", "/opt/local/bin", "/usr/bin"}
	default:
		dirs = []string{"/usr/bin", "/usr/local/bin", "/snap/bin", "/var/lib/flatpak/exports/bin"}
	}

	for _, d := range dirs {
		if p, ok := executableIn(d, name); ok {
			return p, nil
		}
	}
	for _, g := range globs {
		matches, err := filepath.Glob(g)
		if err != nil {
			continue
		}
		for _, d := range matches {
			if p, ok := executableIn(d, name); ok {
				return p, nil
			}
		}
	}

	where := strings.Join(dirs, ", ")
	if len(globs) > 0 {
		where += ", " + strings.Join(globs, ", ")
	}
	return "", fmt.Errorf("%w: looked in PATH and %s", ErrNotInstalled, where)
}

func executableIn(dir, name string) (string, bool) {
	p := filepath.Join(dir, name)
	info, err := os.Stat(p)
	return p, err == nil && !info.IsDir()
}

// WriteOptions configures an encode.
type WriteOptions struct {
	Path   string
	Width  int
	Height int
	FPS    int

	// CRF is the x264 quality parameter, lower being better. 18 is visually
	// lossless for ordinary video; this content is not ordinary video, and the
	// profile's own tolerance decides what is acceptable.
	CRF int

	// Preset is the x264 speed/size tradeoff.
	Preset string
}

func (o WriteOptions) withDefaults() WriteOptions {
	if o.CRF == 0 {
		o.CRF = 18
	}
	if o.Preset == "" {
		o.Preset = "medium"
	}
	if o.FPS == 0 {
		o.FPS = 30
	}
	return o
}

// Write renders frames into a video file. produce is called once per frame and
// returns nil when there are no more.
func Write(ctx context.Context, t Tools, o WriteOptions, produce func() (*image.Gray, error)) error {
	o = o.withDefaults()
	if o.Width <= 0 || o.Height <= 0 {
		return fmt.Errorf("video: frame size %dx%d", o.Width, o.Height)
	}

	args := []string{
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "rawvideo",
		"-pix_fmt", "gray",
		"-s", fmt.Sprintf("%dx%d", o.Width, o.Height),
		"-r", fmt.Sprint(o.FPS),
		"-i", "-",
		"-c:v", "libx264",
		"-preset", o.Preset,
		"-crf", fmt.Sprint(o.CRF),
		// yuv420p rather than gray, because a gray-encoded H.264 stream is legal
		// and widely unplayable: phones, browsers and most platform ingest
		// pipelines expect 4:2:0. The chroma planes are constant and cost almost
		// nothing after compression.
		"-pix_fmt", "yuv420p",
		"-movflags", "+faststart",
		o.Path,
	}

	cmd := exec.CommandContext(ctx, t.FFmpeg, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("video: opening the ffmpeg pipe: %w", err)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("video: starting ffmpeg: %w", err)
	}

	writeErr := func() error {
		defer stdin.Close()
		w := bufio.NewWriterSize(stdin, 1<<20)
		for {
			img, err := produce()
			if err != nil {
				return err
			}
			if img == nil {
				return w.Flush()
			}
			if img.Bounds().Dx() != o.Width || img.Bounds().Dy() != o.Height {
				return fmt.Errorf("video: frame is %dx%d, expected %dx%d",
					img.Bounds().Dx(), img.Bounds().Dy(), o.Width, o.Height)
			}
			// Pix may be padded by Stride, so rows go out one at a time rather
			// than as one slice.
			for y := range o.Height {
				row := img.Pix[y*img.Stride : y*img.Stride+o.Width]
				if _, err := w.Write(row); err != nil {
					return err
				}
			}
		}
	}()

	waitErr := cmd.Wait()
	if writeErr != nil {
		return fmt.Errorf("video: feeding ffmpeg: %w", writeErr)
	}
	if waitErr != nil {
		return fmt.Errorf("video: ffmpeg failed: %w: %s", waitErr, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// Info is what ffprobe reports about a file.
type Info struct {
	Width  int
	Height int
	Frames int
}

// Probe reads a file's real dimensions.
func Probe(ctx context.Context, t Tools, path string) (Info, error) {
	out, err := exec.CommandContext(ctx, t.FFprobe,
		"-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=width,height,nb_frames",
		"-of", "json",
		path,
	).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return Info{}, fmt.Errorf("video: ffprobe failed: %s", strings.TrimSpace(string(ee.Stderr)))
		}
		return Info{}, fmt.Errorf("video: running ffprobe: %w", err)
	}

	var parsed struct {
		Streams []struct {
			Width    int    `json:"width"`
			Height   int    `json:"height"`
			NbFrames string `json:"nb_frames"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		return Info{}, fmt.Errorf("video: parsing ffprobe output: %w", err)
	}
	if len(parsed.Streams) == 0 {
		return Info{}, fmt.Errorf("%w: %s has no video stream", ErrNoFrames, path)
	}

	s := parsed.Streams[0]
	if s.Width <= 0 || s.Height <= 0 {
		return Info{}, fmt.Errorf("%w: %s reports a %dx%d video stream", ErrNoFrames, path, s.Width, s.Height)
	}

	info := Info{Width: s.Width, Height: s.Height}
	// nb_frames is absent or "N/A" for many containers. It is only used for
	// progress reporting, so an unknown count is not an error.
	fmt.Sscanf(s.NbFrames, "%d", &info.Frames)
	return info, nil
}

// Read decodes a video and hands each frame to consume.
//
// The frame size comes from Probe, never from what the caller thinks it wrote.
func Read(ctx context.Context, t Tools, path string, consume func(*image.Gray) error) (Info, error) {
	info, err := Probe(ctx, t, path)
	if err != nil {
		return Info{}, err
	}

	cmd := exec.CommandContext(ctx, t.FFmpeg,
		"-hide_banner", "-loglevel", "error",
		"-i", path,
		"-f", "rawvideo",
		"-pix_fmt", "gray",
		"-",
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return info, fmt.Errorf("video: opening the ffmpeg pipe: %w", err)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return info, fmt.Errorf("video: starting ffmpeg: %w", err)
	}

	readErr := func() error {
		r := bufio.NewReaderSize(stdout, 1<<20)
		size := info.Width * info.Height
		buf := make([]byte, size)
		count := 0

		for {
			if _, err := io.ReadFull(r, buf); err != nil {
				if errors.Is(err, io.EOF) {
					if count == 0 {
						return ErrNoFrames
					}
					return nil
				}
				if errors.Is(err, io.ErrUnexpectedEOF) {
					// A partial trailing frame means the stream was cut. Everything
					// before it is still good, and on this channel a truncated
					// video is a normal thing to be handed.
					return nil
				}
				return err
			}

			img := image.NewGray(image.Rect(0, 0, info.Width, info.Height))
			copy(img.Pix, buf)
			if err := consume(img); err != nil {
				return err
			}
			count++
		}
	}()

	// Drain whatever is left so ffmpeg does not block on a full pipe when the
	// consumer stopped early.
	_, _ = io.Copy(io.Discard, stdout)
	waitErr := cmd.Wait()

	if readErr != nil {
		return info, fmt.Errorf("video: reading frames: %w", readErr)
	}
	if waitErr != nil {
		return info, fmt.Errorf("video: ffmpeg failed: %w: %s", waitErr, strings.TrimSpace(stderr.String()))
	}
	return info, nil
}
