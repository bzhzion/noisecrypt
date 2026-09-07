package webui

import (
	"context"
	"fmt"
	"image"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/bzhzion/noisecrypt/internal/codec"
	"github.com/bzhzion/noisecrypt/internal/fetch"
	"github.com/bzhzion/noisecrypt/internal/profile"
	"github.com/bzhzion/noisecrypt/internal/video"
)

// maxDownload bounds what a URL may pull in.
//
// Deliberately not maxUpload. That limit exists because an upload passes through memory;
// a download goes straight to disk, so the reason does not apply, and applying it anyway
// would refuse the videos this feature exists for. It matches the command line's ceiling
// on an input, which is where the number was already argued.
const maxDownload = 8 << 30 // 8 GiB

// The video routes are the only ones that touch the filesystem or another process.
//
// Both are forced on us by FFmpeg, which writes to a path rather than to a pipe we
// control and reads the same way. The alternative would be holding an entire video in
// memory, and a video is the one artefact this tool produces that is deliberately much
// larger than its input: at the social profile a mebibyte of payload is around five
// minutes of 1080x1920, which is not something to keep on the heap.
//
// Consequences worth stating rather than discovering. Every temporary file is created
// with os.CreateTemp, which is unpredictable by name and mode 0600, and removed on every
// path out including the error ones. And an encode is not a request that returns
// promptly: it runs for as long as the video is long, which is why the interface asks
// for an estimate first and shows it before offering the button.

func (s *Server) videoRoutes() {
	s.mux.HandleFunc("GET /api/tools", s.handleTools)
	s.mux.HandleFunc("POST /api/estimate", s.handleEstimate)
	s.mux.HandleFunc("POST /api/encode", s.handleEncode)
	s.mux.HandleFunc("POST /api/decode", s.handleDecode)
}

// handleTools reports whether FFmpeg is available.
//
// Asked up front so the page can say so plainly instead of offering a button that fails
// after the user has chosen a file, typed a passphrase and waited.
func (s *Server) handleTools(w http.ResponseWriter, r *http.Request) {
	answer := map[string]any{}

	// Reported separately from FFmpeg rather than folded into one "tools are ready"
	// flag: the two absences have nothing to do with each other. Without FFmpeg the
	// whole panel is dead; without yt-dlp everything works and only the URL field is
	// missing, which is a sentence the page can say instead of a section it has to hide.
	if ytdlp, err := fetch.Find(); err == nil {
		answer["ytdlp"] = true
		answer["ytdlpPath"] = ytdlp
	} else {
		answer["ytdlp"] = false
		answer["ytdlpReason"] = err.Error()
	}

	tools, err := video.Find()
	if err != nil {
		answer["ffmpeg"] = false
		answer["reason"] = err.Error()
		writeJSON(w, http.StatusOK, answer)
		return
	}
	answer["ffmpeg"] = true
	answer["ffmpegPath"] = tools.FFmpeg
	writeJSON(w, http.StatusOK, answer)
}

// handleEstimate answers "what will this cost" without spending anything.
//
// It is a separate route rather than a field on the encode response because the answer
// is only useful before the work, not after. A user who learns at the end that their
// file became three hours of video has already waited three hours.
func (s *Server) handleEstimate(w http.ResponseWriter, r *http.Request) {
	req, err := readSealRequest(r)
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	p, err := profile.Lookup(r.FormValue("profile"))
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}

	// Sealed for real rather than approximated: the overhead depends on the mode, the
	// KDF parameters and whether compression helped, and a guess here would be a
	// number presented as a measurement.
	sealed, code, err := seal(req)
	if err != nil {
		fail(w, code, err)
		return
	}

	e := p.Estimate(int64(len(req.data)), int64(len(sealed)))
	writeJSON(w, http.StatusOK, map[string]any{
		"profile":  p.Name,
		"frames":   e.Frames,
		"seconds":  e.Duration,
		"sealed":   len(sealed),
		"width":    p.Width,
		"height":   p.Height,
		"fps":      p.FPS,
		"evidence": p.Evidence.String(),
	})
}

func (s *Server) handleEncode(w http.ResponseWriter, r *http.Request) {
	req, err := readSealRequest(r)
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	p, err := profile.Lookup(r.FormValue("profile"))
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	c, err := codec.New(p)
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	tools, err := video.Find()
	if err != nil {
		fail(w, http.StatusServiceUnavailable, err)
		return
	}

	sealed, code, err := seal(req)
	if err != nil {
		fail(w, code, err)
		return
	}

	tmp, err := os.CreateTemp("", "noisecrypt-*.mp4")
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	path := tmp.Name()
	// Closed immediately: FFmpeg opens the path itself, and holding a handle to a file
	// another process is writing is a Windows sharing violation waiting to happen.
	_ = tmp.Close()
	defer os.Remove(path)

	if err := encodeToPath(r.Context(), c, tools, sealed, path); err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}

	f, err := os.Open(path)
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}

	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Content-Length", fmt.Sprint(info.Size()))
	w.Header().Set("Content-Disposition",
		fmt.Sprintf("attachment; filename=%q", req.name+".mp4"))
	// Streamed rather than read into memory, for the same reason the file exists.
	_, _ = io.Copy(w, f)
}

// encodeToPath bridges the push-shaped encoder and the pull-shaped muxer, exactly as the
// command line does: codec.EncodeTo calls a function per frame, video.Write asks for the
// next one. The buffer is deliberately tiny, because the point of streaming is not to
// hold frames.
func encodeToPath(ctx context.Context, c *codec.Codec, tools video.Tools, sealed []byte, path string) error {
	p := c.Profile()

	type item struct {
		img *image.Gray
		err error
	}
	ch := make(chan item, 2)

	go func() {
		defer close(ch)
		err := c.EncodeTo(sealed, func(_ int, img *image.Gray) error {
			// Without this the goroutine blocks forever on a client that gave up,
			// and the temporary file is never removed because the handler never
			// returns. A cancelled request has to reach the producer.
			select {
			case ch <- item{img: img}:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		if err != nil {
			select {
			case ch <- item{err: err}:
			case <-ctx.Done():
			}
		}
	}()

	return video.Write(ctx, tools, video.WriteOptions{
		Path: path, Width: p.Width, Height: p.Height, FPS: p.FPS,
		CRF: defaultCRF, Preset: defaultPreset,
	}, func() (*image.Gray, error) {
		it, ok := <-ch
		if !ok {
			return nil, nil
		}
		return it.img, it.err
	})
}

// The interface does not expose the encoder knobs. Someone tuning x264 is someone who
// has a command line open, and every extra field on this page is one more thing between
// a person and the thing they came to do.
const (
	defaultCRF    = 18
	defaultPreset = "medium"
)

// localCopy puts the video on disk, whichever way it arrived, and returns the path plus
// the function that removes it.
//
// FFmpeg reads a file and not a stream of form data, so an uploaded video was already
// being written to a temporary file. A downloaded one arrives on disk in the first place.
// The two converge here so that everything after this point in the decoding route is one
// path and not two, which is what keeps a fix to the decoder from being a fix to one half
// of the decoder.
func localCopy(ctx context.Context, req sealRequest) (path string, cleanup func(), code int, err error) {
	if req.url != "" {
		ytdlp, err := fetch.Find()
		if err != nil {
			return "", nil, http.StatusServiceUnavailable, err
		}
		dir, err := os.MkdirTemp("", "noisecrypt-dl-*")
		if err != nil {
			return "", nil, http.StatusInternalServerError, err
		}
		// The whole directory, not the file: yt-dlp decides the extension and may leave
		// more than one thing behind, and removing only what we predicted would leave
		// the rest of a several-gigabyte download in the temporary directory.
		remove := func() { _ = os.RemoveAll(dir) }

		// Bounded by the command line's ceiling on an input rather than the interface's
		// upload limit. Nothing here passes through memory, so the reason the upload
		// limit exists does not apply, and holding a download to 512 MiB would refuse
		// exactly the videos this feature is for: the toughest profile turns a few
		// mebibytes into hours of it.
		file, err := fetch.Get(ctx, ytdlp, req.url, dir, maxDownload)
		if err != nil {
			remove()
			if ctx.Err() != nil {
				return "", nil, http.StatusRequestTimeout, err
			}
			return "", nil, http.StatusBadRequest, err
		}
		return file, remove, 0, nil
	}

	// The extension is preserved because FFmpeg uses it to pick a demuxer, and a
	// platform hands back .mp4 or .webm depending on the rendition.
	ext := strings.ToLower(filepath.Ext(req.name))
	if ext == "" || len(ext) > 5 {
		ext = ".mp4"
	}
	tmp, err := os.CreateTemp("", "noisecrypt-in-*"+ext)
	if err != nil {
		return "", nil, http.StatusInternalServerError, err
	}
	name := tmp.Name()
	remove := func() { _ = os.Remove(name) }

	if _, err := tmp.Write(req.data); err != nil {
		_ = tmp.Close()
		remove()
		return "", nil, http.StatusInternalServerError, err
	}
	if err := tmp.Close(); err != nil {
		remove()
		return "", nil, http.StatusInternalServerError, err
	}
	return name, remove, 0, nil
}

func (s *Server) handleDecode(w http.ResponseWriter, r *http.Request) {
	req, err := readDecodeRequest(r)
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	p, err := profile.Lookup(r.FormValue("profile"))
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	c, err := codec.New(p)
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	tools, err := video.Find()
	if err != nil {
		fail(w, http.StatusServiceUnavailable, err)
		return
	}

	path, cleanup, code, err := localCopy(r.Context(), req)
	if err != nil {
		fail(w, code, err)
		return
	}
	defer cleanup()

	d := c.NewDecoder()
	info, err := video.Read(r.Context(), tools, path, func(img *image.Gray) error {
		d.Add(img)
		return nil
	})
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}

	sealed, err := d.Finish()
	if err != nil {
		// A rescaled video that will not decode is most often the wrong profile
		// rather than a damaged one, and saying so saves the user guessing. The
		// dimensions are the evidence, so they are quoted rather than asserted.
		if info.Width != p.Width || info.Height != p.Height {
			fail(w, http.StatusBadRequest, fmt.Errorf(
				"%w (this video is %dx%d and the %s profile encodes at %dx%d, so either it has been rescaled by a platform or the profile is wrong)",
				err, info.Width, info.Height, p.Name, p.Width, p.Height))
			return
		}
		fail(w, http.StatusBadRequest, err)
		return
	}

	opened, code, err := open(req, sealed)
	if err != nil {
		fail(w, code, err)
		return
	}

	w.Header().Set("X-NoiseCrypt-Frames", fmt.Sprint(d.Seen))
	w.Header().Set("X-NoiseCrypt-Unreadable", fmt.Sprint(d.Unreadable))
	// Frames located and sampled whose shard then failed its CRC. A different loss from
	// Unreadable, and one that used to be reported nowhere, so this header could say
	// zero on a video that had actually lost frames.
	w.Header().Set("X-NoiseCrypt-Discarded", fmt.Sprint(d.Discarded()))
	writeOpened(w, opened)
}
