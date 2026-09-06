// Package webui serves the graphical interface as a local web page.
//
// # Why a web page rather than a native window
//
// Every Go GUI toolkit that draws a real window links C code into the process: GLFW,
// X11, WebKitGTK, Cocoa. That process is the one holding the decrypted private
// identity, so a memory-corruption bug anywhere in a toolkit's image loader becomes a
// memory-corruption bug next to the keys. Go's memory safety is a real property of this
// codebase and linking C in gives it up precisely where it matters most.
//
// A browser is also C++, and far more of it. The difference is that it runs in its own
// process, behind its own sandbox, and never sees a key. C on the other side of a
// process boundary is not the same risk as C inside yours. The tool already shells out
// to FFmpeg on the same reasoning.
//
// It costs a native feel, and it buys: no C toolchain to install anywhere, the same six
// build targets as the command line, and the keys staying in a pure Go process.
//
// # Why there is a token even though this only listens on localhost
//
// "Only local" sounds like a boundary and is not one. Any web page you visit can send
// requests to 127.0.0.1, and port scanning from a page is trivial. The same-origin
// policy stops that page reading the response, but not the request arriving and having
// an effect, and a request that decrypts a file has its effect whether or not the
// attacker sees the reply. DNS rebinding removes even that limit: the attacker's domain
// resolves to their address, then re-resolves to 127.0.0.1, and the browser then treats
// their JavaScript as same-origin and lets it read everything.
//
// The token is the standard mitigation, which is why Jupyter, Syncthing and every other
// local service has one. Two honest limits go with it. It defends against the web, not
// against the machine: any process running as this user can read the token from the
// process's own command line or from the browser. And it costs the user nothing only
// because the binary opens the browser with the URL already carrying it.
package webui

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
)

// tokenBytes is the size of the session token. Sixteen bytes is well past guessing
// range for something that lives as long as one run of the program.
const tokenBytes = 16

// Server is a running interface.
type Server struct {
	listener net.Listener
	token    string
	mux      *http.ServeMux
}

// New binds a listener on the loopback interface and prepares the routes.
//
// The port is always chosen by the operating system. A fixed port would be one a
// hostile page could target without searching, and there is no reason to help.
func New() (*Server, error) {
	// Explicitly the loopback address, never ":0", which would bind every interface
	// and put the interface on the network.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("webui: listening on loopback: %w", err)
	}

	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		ln.Close()
		return nil, fmt.Errorf("webui: reading randomness: %w", err)
	}

	s := &Server{listener: ln, token: hex.EncodeToString(raw), mux: http.NewServeMux()}
	s.routes()
	return s, nil
}

// Addr is the host:port the interface listens on.
func (s *Server) Addr() string { return s.listener.Addr().String() }

// URL is the address to open, token included, so nobody ever types it.
func (s *Server) URL() string { return "http://" + s.Addr() + "/?token=" + s.token }

// Serve runs until the listener is closed.
func (s *Server) Serve() error {
	srv := &http.Server{Handler: s.guard(s.mux)}
	return srv.Serve(s.listener)
}

// Close stops listening.
func (s *Server) Close() error { return s.listener.Close() }

// guard is the whole security posture of this server, in one place so it cannot be
// forgotten on a route.
//
// Three checks, each closing something different:
//
//	Host        stops DNS rebinding. A rebinding attack has the browser believe the
//	            attacker's domain is same-origin, but the request still arrives with
//	            that domain in the Host header, and a server that insists on its own
//	            address rejects it before any handler runs.
//	Origin      stops an ordinary cross-site request from another page. Requests with
//	            no Origin at all are allowed, because that is what a plain navigation
//	            and a same-origin fetch look like.
//	token       is the fallback for anything the first two miss, and the only check
//	            that survives a client which sets headers freely.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// No caching and no framing, on every response including the refusals.
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'")
		w.Header().Set("Referrer-Policy", "no-referrer")

		if !s.hostAllowed(r.Host) {
			http.Error(w, "unexpected Host header", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && !s.originAllowed(origin) {
			http.Error(w, "cross-origin request refused", http.StatusForbidden)
			return
		}
		if !s.tokenValid(r) {
			http.Error(w, "missing or invalid token", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// hostAllowed accepts only this server's own address.
func (s *Server) hostAllowed(host string) bool {
	if host == s.Addr() {
		return true
	}
	// The listener reports 127.0.0.1; a browser may send localhost for the same
	// place, which is the same interface and equally safe.
	_, port, err := net.SplitHostPort(s.Addr())
	if err != nil {
		return false
	}
	return host == "localhost:"+port
}

func (s *Server) originAllowed(origin string) bool {
	trimmed := strings.TrimPrefix(origin, "http://")
	if trimmed == origin {
		// Anything not plain http on loopback: an https page, a browser extension
		// origin, or "null". None of them should be driving this.
		return false
	}
	return s.hostAllowed(trimmed)
}

// cookieName carries the token for subresource requests.
const cookieName = "noisecrypt_token"

// tokenValid accepts the token from a cookie, a header, or the query string.
//
// Three sources because three different kinds of request need it, which is not
// over-engineering but a bug found by looking at the rendered page rather than trusting
// the tests. The first navigation carries the token in the URL, since that is what the
// binary opens. The page's own fetches send it in a header. And **the browser's
// requests for app.css and app.js carry neither**: a stylesheet link does not inherit
// the query string of the page that referenced it, so those subresources arrived with
// no credential at all and the guard refused them. The page rendered unstyled and the
// tests were happy, because they only ever asked for the index.
//
// The cookie fixes that, and it is safe here for a specific reason: SameSite=Strict
// means a request originating from any other site does not carry it, so it adds no
// cross-site request forgery surface. It is also HttpOnly, so a scripting bug on the
// page cannot read it back out.
func (s *Server) tokenValid(r *http.Request) bool {
	if c, err := r.Cookie(cookieName); err == nil && s.matches(c.Value) {
		return true
	}
	if s.matches(r.Header.Get("X-NoiseCrypt-Token")) {
		return true
	}
	return s.matches(r.URL.Query().Get("token"))
}

func (s *Server) matches(got string) bool {
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) == 1
}

// setCookie hands the token to the browser on the first successful navigation, so
// everything the page loads afterwards is authenticated without a query string.
func (s *Server) setCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    s.token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		// No Expires: a session cookie dies with the browser, and the server it
		// authenticates dies with the terminal.
	})
}

// OpenBrowser asks the desktop to open the interface.
//
// Failure is not an error worth stopping for: the caller prints the URL anyway, and a
// machine with no default browser is a normal thing rather than a broken one.
func OpenBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		// rundll32 rather than "cmd /c start", which would interpret & in the URL.
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
