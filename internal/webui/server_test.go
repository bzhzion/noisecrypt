package webui

import (
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
)

// start runs a server for the duration of a test and returns it.
func start(t *testing.T) *Server {
	t.Helper()
	s, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	go func() { _ = s.Serve() }()
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// get issues a request with full control over the headers, which is the point: the
// checks below are about what a hostile page can and cannot make a browser send.
func get(t *testing.T, url string, headers map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	// Host is not an ordinary header in Go's client; it has its own field.
	if h, ok := headers["Host"]; ok {
		req.Host = h
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("performing the request: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// TestListensOnLoopbackOnly is the first line of the whole design. Binding every
// interface would put the interface on the network, where the token would be the only
// thing between a stranger and the user's keys.
func TestListensOnLoopbackOnly(t *testing.T) {
	s := start(t)

	host, _, err := net.SplitHostPort(s.Addr())
	if err != nil {
		t.Fatalf("parsing the address: %v", err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		t.Fatalf("the server listens on %s, which is not a loopback address", host)
	}
}

func TestPortIsNotFixed(t *testing.T) {
	a, b := start(t), start(t)
	if a.Addr() == b.Addr() {
		t.Fatal("two servers took the same port, so the port is predictable")
	}
}

func TestTokenIsRequired(t *testing.T) {
	s := start(t)

	t.Run("no token", func(t *testing.T) {
		resp := get(t, "http://"+s.Addr()+"/", nil)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("a request with no token returned %d", resp.StatusCode)
		}
	})

	t.Run("wrong token", func(t *testing.T) {
		resp := get(t, "http://"+s.Addr()+"/?token=deadbeef", nil)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("a request with a wrong token returned %d", resp.StatusCode)
		}
	})

	t.Run("token in the query string", func(t *testing.T) {
		resp := get(t, s.URL(), nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("the URL the binary opens returned %d", resp.StatusCode)
		}
	})

	t.Run("token in the header", func(t *testing.T) {
		resp := get(t, "http://"+s.Addr()+"/", map[string]string{"X-NoiseCrypt-Token": s.token})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("a request with the token in a header returned %d", resp.StatusCode)
		}
	})
}

// TestDNSRebindingIsRefused is the attack the Host check exists for, and the reason
// listening on loopback is not by itself enough.
//
// In a rebinding attack the browser has been convinced that the attacker's domain
// resolves to 127.0.0.1, so it treats their JavaScript as same-origin and will let it
// read responses. What it still does is send that domain in the Host header, which is
// what gives the server a way to refuse.
func TestDNSRebindingIsRefused(t *testing.T) {
	s := start(t)

	for _, host := range []string{"evil.example", "evil.example:1234", "attacker.test"} {
		resp := get(t, "http://"+s.Addr()+"/?token="+s.token, map[string]string{"Host": host})
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("Host %q returned %d, expected 403 even with a valid token", host, resp.StatusCode)
		}
	}

	// localhost is the same interface under another name and must keep working.
	_, port, _ := net.SplitHostPort(s.Addr())
	resp := get(t, "http://"+s.Addr()+"/?token="+s.token, map[string]string{"Host": "localhost:" + port})
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Host localhost:%s returned %d, it is the same interface", port, resp.StatusCode)
	}
}

// TestCrossOriginIsRefused covers the ordinary case: another page in the same browser
// making a request without any rebinding trickery.
func TestCrossOriginIsRefused(t *testing.T) {
	s := start(t)

	for _, origin := range []string{
		"http://evil.example",
		"https://evil.example",
		"null",
		"chrome-extension://abcdef",
		"https://" + s.Addr(), // right address, wrong scheme
	} {
		resp := get(t, s.URL(), map[string]string{"Origin": origin})
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("Origin %q returned %d, expected 403", origin, resp.StatusCode)
		}
	}

	// The page's own requests carry their own origin and must pass.
	resp := get(t, s.URL(), map[string]string{"Origin": "http://" + s.Addr()})
	if resp.StatusCode != http.StatusOK {
		t.Errorf("the page's own origin returned %d", resp.StatusCode)
	}
}

// TestRefusalsCarryTheSecurityHeaders checks that the headers are set before the
// checks rather than after, so an error page is as protected as a successful one.
func TestRefusalsCarryTheSecurityHeaders(t *testing.T) {
	s := start(t)
	resp := get(t, "http://"+s.Addr()+"/", nil)

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected a refusal, got %d", resp.StatusCode)
	}
	for _, h := range []string{"Cache-Control", "X-Content-Type-Options", "Content-Security-Policy"} {
		if resp.Header.Get(h) == "" {
			t.Errorf("the refusal is missing %s", h)
		}
	}
}

// TestURLCarriesTheToken pins the property that makes the token free for the user.
func TestURLCarriesTheToken(t *testing.T) {
	s := start(t)
	if !strings.Contains(s.URL(), "token=") {
		t.Fatalf("the opened URL does not carry the token: %s", s.URL())
	}
	if !strings.HasPrefix(s.URL(), "http://127.0.0.1:") {
		t.Fatalf("the opened URL is not loopback: %s", s.URL())
	}
}

// TestGuardRunsBeforeEveryHandler makes sure a future route cannot be added outside
// the checks, by exercising a path that does not exist: even a 404 has to be gated.
func TestGuardRunsBeforeEveryHandler(t *testing.T) {
	s := start(t)
	resp := get(t, "http://"+s.Addr()+"/does-not-exist", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("an unauthenticated request to an unknown path returned %d, "+
			"so the guard is not in front of the mux", resp.StatusCode)
	}
}

func TestServeIndex(t *testing.T) {
	s := start(t)
	resp := get(t, s.URL(), nil)
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("index returned %d", resp.StatusCode)
	}
	if !strings.Contains(string(body), "NoiseCrypt") {
		t.Fatalf("the served page does not look like the interface:\n%s", string(body)[:min(200, len(body))])
	}
}

// TestBrowserLoadsTheWholePage is the test that was missing, and its absence let a real
// defect ship: every test above asks for one URL with a token attached, which is not how
// a browser behaves. A browser loads the page, then goes back for the stylesheet and the
// script on its own, and those requests inherit nothing from the page's URL. They were
// refused, and the interface rendered as unstyled markup.
//
// Driving it through a cookie jar is what makes the test faithful, because the jar is
// the only part of a browser that matters here.
func TestBrowserLoadsTheWholePage(t *testing.T) {
	s := start(t)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar.New: %v", err)
	}
	browser := &http.Client{Jar: jar}

	// The navigation the binary opens, token in the URL.
	resp, err := browser.Get(s.URL())
	if err != nil {
		t.Fatalf("loading the page: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the page returned %d", resp.StatusCode)
	}

	// Then the subresources, as the browser asks for them: bare URLs, no token.
	for _, path := range []string{"/app.css", "/app.js"} {
		sub, err := browser.Get("http://" + s.Addr() + path)
		if err != nil {
			t.Fatalf("loading %s: %v", path, err)
		}
		body, _ := io.ReadAll(sub.Body)
		_ = sub.Body.Close()

		if sub.StatusCode != http.StatusOK {
			t.Fatalf("%s returned %d: the page would render unstyled", path, sub.StatusCode)
		}
		if len(body) == 0 {
			t.Fatalf("%s served an empty body", path)
		}
	}
}

// The cookie is only acceptable because of its attributes. SameSite=Strict is what stops
// it from turning the loopback interface into a cross-site request forgery target, and
// HttpOnly keeps it out of reach of the page's own scripts.
func TestCookieAttributes(t *testing.T) {
	s := start(t)
	resp := get(t, s.URL(), nil)

	var found *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == cookieName {
			found = c
		}
	}
	if found == nil {
		t.Fatal("the page set no token cookie, so its own assets cannot load")
	}
	if found.SameSite != http.SameSiteStrictMode {
		t.Errorf("SameSite is %v, must be Strict", found.SameSite)
	}
	if !found.HttpOnly {
		t.Error("the cookie is readable by scripts")
	}
}

// A cookie is a credential like any other, and is checked like any other.
func TestWrongCookieIsRefused(t *testing.T) {
	s := start(t)
	resp := get(t, "http://"+s.Addr()+"/app.css", map[string]string{
		"Cookie": cookieName + "=0000000000000000000000000000000000",
	})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a forged cookie returned %d, expected 401", resp.StatusCode)
	}
}

// Ensure the guard composes with an arbitrary handler, so the tests above are about
// the guard rather than about the routes behind it.
func TestGuardIsIndependentOfTheRoutes(t *testing.T) {
	s := start(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://"+s.Addr()+"/anything", nil)
	req.Host = s.Addr()

	s.guard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("the guard let an untokened request through to the handler: %d", rec.Code)
	}
}
