package webui

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

// The interface used to report a signature and never enforce one. It said who signed a
// container, or that nobody had, and handed the contents over either way. That makes
// removing a signature exactly as effective as forging one, which is the failure the
// whole signing design exists to prevent, and it made the graphical interface strictly
// weaker than the command line rather than merely smaller.
//
// These pin the five outcomes, including the two that must be refusals.

func identity(t *testing.T, s *Server) (public, private string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, "http://"+s.Addr()+"/api/keygen", nil)
	req.Header.Set("X-NoiseCrypt-Token", s.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	defer resp.Body.Close()

	var id struct{ Public, Private string }
	if err := json.NewDecoder(resp.Body).Decode(&id); err != nil {
		t.Fatalf("decoding the identity: %v", err)
	}
	return id.Public, id.Private
}

func TestSignatureCanBeDemanded(t *testing.T) {
	s := start(t)

	pub, priv := identity(t, s)
	otherPub, _ := identity(t, s)

	const pass = "correcte-horse-battery"
	plain := []byte("who produced this?")

	sealOne := func(fields map[string]string) []byte {
		resp := post(t, s, "/api/seal", plain, "note.txt", fields)
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("seal returned %d: %s", resp.StatusCode, body)
		}
		b, _ := io.ReadAll(resp.Body)
		return b
	}
	unsigned := sealOne(map[string]string{"passphrase": pass})
	signed := sealOne(map[string]string{"passphrase": pass, "sign": priv})

	cases := []struct {
		name      string
		container []byte
		fields    map[string]string
		want      int
	}{
		{
			"unsigned, nothing demanded",
			unsigned,
			map[string]string{"passphrase": pass},
			http.StatusOK,
		},
		{
			// The one that mattered: this used to succeed.
			"unsigned, a signature demanded",
			unsigned,
			map[string]string{"passphrase": pass, "requireSignature": "1"},
			http.StatusBadRequest,
		},
		{
			"signed, a signature demanded",
			signed,
			map[string]string{"passphrase": pass, "requireSignature": "1"},
			http.StatusOK,
		},
		{
			"signed, the right signer demanded",
			signed,
			map[string]string{"passphrase": pass, "from": pub},
			http.StatusOK,
		},
		{
			// A valid signature by the wrong person is still the wrong container, and
			// it is the case a check on "is it signed" alone would wave through.
			"signed, a different signer demanded",
			signed,
			map[string]string{"passphrase": pass, "from": otherPub},
			http.StatusBadRequest,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp := post(t, s, "/api/open", c.container, "note.ncry", c.fields)
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != c.want {
				t.Fatalf("got %d, want %d: %s", resp.StatusCode, c.want, body)
			}
			// A refusal must not carry the plaintext with it. The signature travels
			// inside the ciphertext, so by the time the check runs the contents are
			// already decrypted in this process; the only thing that protects the
			// caller is that they are never written to the response.
			if c.want != http.StatusOK && len(body) > 0 {
				if string(body[:1]) != "{" {
					t.Fatalf("a refusal returned something other than an error: %q", body)
				}
			}
		})
	}
}

// The version has to reach the page. Someone reporting a problem from the interface
// could not say which build they were looking at, and "Local instance" told them
// something they already knew.
func TestPageCarriesTheVersion(t *testing.T) {
	previous := Version
	Version = "9.9.9-test"
	t.Cleanup(func() { Version = previous })

	s := start(t)
	resp := get(t, s.URL(), nil)
	body, _ := io.ReadAll(resp.Body)

	if !contains(string(body), "9.9.9-test") {
		t.Error("the page does not show the version it is running")
	}
	if contains(string(body), "{{VERSION}}") {
		t.Error("the version placeholder was served unsubstituted")
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	}()
}
