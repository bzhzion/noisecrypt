package webui

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bzhzion/noisecrypt/internal/video"
)

// post builds a multipart form the way the page does and sends it with the token.
func post(t *testing.T, s *Server, path string, file []byte, filename string, fields map[string]string) *http.Response {
	t.Helper()

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("building the form: %v", err)
	}
	if _, err := part.Write(file); err != nil {
		t.Fatalf("writing the file part: %v", err)
	}
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			t.Fatalf("writing field %s: %v", k, err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("closing the form: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, "http://"+s.Addr()+path, &body)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-NoiseCrypt-Token", s.token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("performing the request: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func requireFFmpeg(t *testing.T) {
	t.Helper()
	if _, err := video.Find(); err != nil {
		t.Skipf("FFmpeg is not installed: %v", err)
	}
}

// TestEstimateSpendsNothing checks the one property that makes the estimate worth having:
// it answers before the work rather than after.
func TestEstimateSpendsNothing(t *testing.T) {
	s := start(t)
	resp := post(t, s, "/api/estimate", []byte("a short payload"), "note.txt", map[string]string{
		"passphrase": "correcte-horse-battery",
		"profile":    "archive",
	})
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("estimate returned %d: %s", resp.StatusCode, body)
	}

	var got struct {
		Frames  int     `json:"frames"`
		Seconds float64 `json:"seconds"`
		Sealed  int     `json:"sealed"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decoding the estimate: %v", err)
	}
	if got.Frames <= 0 || got.Seconds <= 0 || got.Sealed <= 0 {
		t.Fatalf("estimate is not an estimate: %+v", got)
	}
}

// TestVideoRoundTrip is the whole point of the feature: a file goes in, a video comes
// out, and the same bytes come back. Anything less than byte equality is a failure, since
// the entire error-correction layer exists to make that the only acceptable result.
func TestVideoRoundTrip(t *testing.T) {
	requireFFmpeg(t)
	s := start(t)

	original := []byte("Carried as video and recovered, or the whole thing is pointless.\n")
	const pass = "correcte-horse-battery"

	enc := post(t, s, "/api/encode", original, "note.txt", map[string]string{
		"passphrase": pass, "profile": "archive",
	})
	if enc.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(enc.Body)
		t.Fatalf("encode returned %d: %s", enc.StatusCode, body)
	}
	if ct := enc.Header.Get("Content-Type"); ct != "video/mp4" {
		t.Errorf("encode served %q rather than video/mp4", ct)
	}
	mp4, err := io.ReadAll(enc.Body)
	if err != nil {
		t.Fatalf("reading the video: %v", err)
	}
	if len(mp4) == 0 {
		t.Fatal("encode served an empty video")
	}

	dec := post(t, s, "/api/decode", mp4, "note.txt.mp4", map[string]string{
		"passphrase": pass, "profile": "archive",
	})
	if dec.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(dec.Body)
		t.Fatalf("decode returned %d: %s", dec.StatusCode, body)
	}
	recovered, err := io.ReadAll(dec.Body)
	if err != nil {
		t.Fatalf("reading the recovered file: %v", err)
	}
	if !bytes.Equal(recovered, original) {
		t.Fatalf("recovered %d bytes, expected %d, and they differ", len(recovered), len(original))
	}

	// The name travels inside the container, so it survives the video and comes back.
	if d := dec.Header.Get("Content-Disposition"); !strings.Contains(d, `"note.txt"`) {
		t.Errorf("the original name did not survive the round trip: %q", d)
	}
	// Reported even on success: it is the one number that says how much margin was
	// left, and redundancy absorbing damage silently is exactly what hides a problem.
	if dec.Header.Get("X-NoiseCrypt-Unreadable") == "" {
		t.Error("no unreadable-frame count reported")
	}
}

// A wrong profile is the likeliest mistake a user of this interface can make, and the
// bare decoder error ("no readable frames") does not point at it at all. The dimensions
// are the evidence, so the message quotes them.
func TestWrongProfileSaysSo(t *testing.T) {
	requireFFmpeg(t)
	s := start(t)

	const pass = "correcte-horse-battery"
	enc := post(t, s, "/api/encode", []byte("encoded one way, read another"), "note.txt",
		map[string]string{"passphrase": pass, "profile": "archive"})
	mp4, _ := io.ReadAll(enc.Body)

	dec := post(t, s, "/api/decode", mp4, "note.txt.mp4",
		map[string]string{"passphrase": pass, "profile": "social"})
	if dec.StatusCode != http.StatusBadRequest {
		t.Fatalf("decoding with the wrong profile returned %d", dec.StatusCode)
	}
	body, _ := io.ReadAll(dec.Body)
	for _, want := range []string{"1920x1080", "1080x1920", "profile is wrong"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("the error does not mention %q, so it does not point at the cause:\n%s", want, body)
		}
	}
}

// TestNoTemporaryFilesSurvive guards the only routes in this package that touch the
// filesystem. A handler that returns without removing its scratch file leaves a copy of
// the user's video in the system temporary directory, readable for as long as the
// machine keeps it, which is a confidentiality failure and not an untidiness.
func TestNoTemporaryFilesSurvive(t *testing.T) {
	requireFFmpeg(t)
	s := start(t)

	before := scratchFiles(t)

	const pass = "correcte-horse-battery"
	enc := post(t, s, "/api/encode", []byte("leaves nothing behind"), "note.txt",
		map[string]string{"passphrase": pass, "profile": "archive"})
	mp4, _ := io.ReadAll(enc.Body)
	_ = post(t, s, "/api/decode", mp4, "note.txt.mp4",
		map[string]string{"passphrase": pass, "profile": "archive"})

	// And a failing decode, because the error paths are the ones that forget.
	_ = post(t, s, "/api/decode", []byte("not a video at all"), "broken.mp4",
		map[string]string{"passphrase": pass, "profile": "archive"})

	if after := scratchFiles(t); len(after) > len(before) {
		t.Fatalf("%d scratch files were left behind in %s:\n%v",
			len(after)-len(before), os.TempDir(), after)
	}
}

func scratchFiles(t *testing.T) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(os.TempDir(), "noisecrypt-*"))
	if err != nil {
		t.Fatalf("listing the temporary directory: %v", err)
	}
	return matches
}
