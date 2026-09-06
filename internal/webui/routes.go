package webui

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/bzhzion/noisecrypt/internal/container"
	"github.com/bzhzion/noisecrypt/internal/crypt"
	"github.com/bzhzion/noisecrypt/internal/profile"
)

//go:embed assets
var assets embed.FS

// maxUpload bounds what a single request may submit.
//
// The interface streams a whole file through memory, which is fine for the sizes this
// tool is used at and would not be for an arbitrary one. Without a bound, a local page
// that got past the guard could exhaust memory with one request; with it, the failure
// is a refusal.
const maxUpload = 512 << 20 // 512 MiB

func (s *Server) routes() {
	s.mux.HandleFunc("GET /{$}", s.handleIndex)
	s.mux.HandleFunc("GET /app.js", s.handleAsset("assets/app.js", "text/javascript; charset=utf-8"))
	s.mux.HandleFunc("GET /app.css", s.handleAsset("assets/app.css", "text/css; charset=utf-8"))
	s.mux.HandleFunc("GET /api/profiles", s.handleProfiles)
	s.mux.HandleFunc("POST /api/keygen", s.handleKeygen)
	s.mux.HandleFunc("POST /api/seal", s.handleSeal)
	s.mux.HandleFunc("POST /api/open", s.handleOpen)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	page, err := assets.ReadFile("assets/index.html")
	if err != nil {
		http.Error(w, "interface missing from the binary", http.StatusInternalServerError)
		return
	}
	// The page needs the token for its own requests, and putting it in the document
	// keeps it out of every later URL and therefore out of browser history.
	body := strings.Replace(string(page), "{{TOKEN}}", s.token, 1)
	// And the browser needs it for the stylesheet and the script, which it fetches on
	// its own and without inheriting anything from the page's URL.
	s.setCookie(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, body)
}

func (s *Server) handleAsset(name, contentType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, err := assets.ReadFile(name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write(b)
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

func (s *Server) handleProfiles(w http.ResponseWriter, r *http.Request) {
	type entry struct {
		Name      string  `json:"name"`
		Summary   string  `json:"summary"`
		PerFrame  int     `json:"perFrame"`
		Overhead  float64 `json:"overhead"`
		Evidence  string  `json:"evidence"`
		Note      string  `json:"evidenceNote"`
		BytesPerS int     `json:"bytesPerSecond"`
	}
	out := make([]entry, 0, 3)
	for _, p := range profile.All() {
		out = append(out, entry{
			Name: p.Name, Summary: p.Summary,
			PerFrame: p.PayloadBytesPerFrame(), Overhead: p.Redundancy(),
			Evidence: p.Evidence.String(), Note: p.EvidenceNote,
			BytesPerS: p.PayloadBytesPerFrame() * p.FPS,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleKeygen(w http.ResponseWriter, r *http.Request) {
	id, err := crypt.GenerateIdentity()
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	// The private half is returned once and never stored server side. The page hands
	// it straight to a download, so this process never writes a key to disk on its
	// own initiative.
	writeJSON(w, http.StatusOK, map[string]string{
		"private": id.String(),
		"public":  id.Public.String(),
		"short":   id.Public.Short(),
	})
}

// sealRequest is the multipart form the page submits.
type sealRequest struct {
	data       []byte
	name       string
	passphrase string
	to         string
	signWith   string
	noCompress bool
}

func readSealRequest(r *http.Request) (sealRequest, error) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		return sealRequest{}, fmt.Errorf("reading the form: %w", err)
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		return sealRequest{}, fmt.Errorf("no file submitted: %w", err)
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxUpload+1))
	if err != nil {
		return sealRequest{}, fmt.Errorf("reading the file: %w", err)
	}
	if len(data) > maxUpload {
		return sealRequest{}, fmt.Errorf("file exceeds the %d MiB limit of the interface", maxUpload>>20)
	}

	return sealRequest{
		data:       data,
		name:       filepath.Base(header.Filename),
		passphrase: r.FormValue("passphrase"),
		to:         strings.TrimSpace(r.FormValue("to")),
		signWith:   strings.TrimSpace(r.FormValue("sign")),
		noCompress: r.FormValue("noCompress") == "1",
	}, nil
}

func (s *Server) handleSeal(w http.ResponseWriter, r *http.Request) {
	req, err := readSealRequest(r)
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}

	compression := container.CompressionGzip
	if req.noCompress {
		compression = container.CompressionNone
	}
	packOpts := container.PackOptions{
		Metadata: container.Metadata{Name: req.name, Compression: compression},
	}
	sealOpts := crypt.SealOptions{}

	switch {
	case req.to != "":
		recipient, err := crypt.ParsePublicIdentity(req.to)
		if err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		sealOpts.Recipient = &recipient
		packOpts.Recipient = recipient.Fingerprint()
	case req.passphrase != "":
		// The same floor as the command line. Enforcing it in one place and not the
		// other would make the weaker path the convenient one.
		if len(req.passphrase) < 8 {
			fail(w, http.StatusBadRequest,
				fmt.Errorf("passphrase is %d bytes, the minimum for sealing is 8", len(req.passphrase)))
			return
		}
		sealOpts.Passphrase = []byte(req.passphrase)
	default:
		fail(w, http.StatusBadRequest, fmt.Errorf("supply a passphrase or a recipient"))
		return
	}

	if req.signWith != "" {
		signer, err := crypt.ParsePrivateIdentity(req.signWith)
		if err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		packOpts.Signer = signer
	}

	packed, err := container.PackWith(packOpts, req.data)
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	sealed, err := crypt.Seal(packed, sealOpts)
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf("attachment; filename=%q", req.name+".ncry"))
	_, _ = w.Write(sealed)
}

func (s *Server) handleOpen(w http.ResponseWriter, r *http.Request) {
	req, err := readSealRequest(r)
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}

	header, _, err := crypt.ParseHeader(req.data)
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}

	var opts crypt.OpenOptions
	var recipient [crypt.FingerprintSize]byte

	if header.Mode == crypt.ModeHybrid {
		if req.signWith == "" {
			fail(w, http.StatusBadRequest,
				fmt.Errorf("this container is sealed to an identity; supply the private identity"))
			return
		}
		id, err := crypt.ParsePrivateIdentity(req.signWith)
		if err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		opts.Identity = id
		recipient = id.Public.Fingerprint()
	} else {
		if req.passphrase == "" {
			fail(w, http.StatusBadRequest, fmt.Errorf("this container needs a passphrase"))
			return
		}
		opts.Passphrase = []byte(req.passphrase)
	}

	payload, err := crypt.Open(req.data, opts)
	if err != nil {
		// Deliberately passed through as it stands: wrong passphrase, wrong
		// recipient and tampering are one indistinguishable failure by design.
		fail(w, http.StatusBadRequest, err)
		return
	}

	opened, err := container.Open(payload, recipient)
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}

	signer := ""
	if opened.Signer != nil {
		signer = opened.Signer.Short()
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", opened.Name))
	// Read by the page so it can tell the user what the signature proved, which a
	// download alone cannot say.
	w.Header().Set("X-NoiseCrypt-Signer", signer)
	_, _ = w.Write(opened.Data)
}
