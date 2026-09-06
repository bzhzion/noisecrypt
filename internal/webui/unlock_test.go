package webui

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/bzhzion/noisecrypt/internal/crypt"
)

// An identity stored by `noisecrypt keygen` is locked under a passphrase unless someone
// asked for it not to be. Without this, the command line and the interface disagreed
// about what a key file is: one wrote a shape the other could not read.
func TestTheInterfaceOpensALockedIdentity(t *testing.T) {
	s := start(t)

	id, err := crypt.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	const keyPass = "mon chat dort sur le radiateur"
	locked, err := crypt.LockIdentity(id, []byte(keyPass),
		crypt.KDFParams{Time: 1, Memory: 8 << 10, Lanes: 1})
	if err != nil {
		t.Fatalf("LockIdentity: %v", err)
	}

	plain := []byte("addressed to a locked identity")
	sealedResp := post(t, s, "/api/seal", plain, "note.txt", map[string]string{
		"to": id.Public.String(),
	})
	if sealedResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(sealedResp.Body)
		t.Fatalf("seal returned %d: %s", sealedResp.StatusCode, body)
	}
	container, _ := io.ReadAll(sealedResp.Body)

	cases := []struct {
		name   string
		fields map[string]string
		want   int
	}{
		{
			// The case that used to be impossible to get past: the identity is right,
			// and the interface had no way to ask for what unlocks it.
			"locked identity, no passphrase",
			map[string]string{"sign": locked},
			http.StatusBadRequest,
		},
		{
			"locked identity, wrong passphrase",
			map[string]string{"sign": locked, "identityPassphrase": "pas la bonne du tout"},
			http.StatusBadRequest,
		},
		{
			"locked identity, right passphrase",
			map[string]string{"sign": locked, "identityPassphrase": keyPass},
			http.StatusOK,
		},
		{
			// A plain identity still works, and works whether or not a passphrase comes
			// with it. A tool that cannot read what it wrote last week eats keys.
			"plain identity, no passphrase",
			map[string]string{"sign": id.String()},
			http.StatusOK,
		},
		{
			"plain identity, stray passphrase",
			map[string]string{"sign": id.String(), "identityPassphrase": "sans objet"},
			http.StatusOK,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp := post(t, s, "/api/open", container, "note.ncry", c.fields)
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != c.want {
				t.Fatalf("got %d, want %d: %s", resp.StatusCode, c.want, body)
			}
			if c.want == http.StatusOK && string(body) != string(plain) {
				t.Fatalf("recovered %q", body)
			}
		})
	}
}

// The field is built in JavaScript, so nothing else here would notice it going away, and
// its whole point is that it appears only when it is needed.
func TestTheUnlockFieldIsConditional(t *testing.T) {
	script := asset(t, "app.js")

	if !strings.Contains(script, "noisecrypt-locked-v1:") {
		t.Error("nothing recognises a locked identity, so the passphrase field can never appear")
	}
	if !strings.Contains(script, `name = 'identityPassphrase'`) &&
		!strings.Contains(script, `input.name = 'identityPassphrase'`) {
		t.Error("the field does not submit under the name the server reads")
	}
	// Hidden is not enough on its own: a hidden field still submits, so an identity
	// swapped after typing would send a passphrase belonging to the previous one.
	if !strings.Contains(script, "input.disabled") {
		t.Error("the field is only hidden, so it can still submit a stale passphrase")
	}
	// Setting a textarea from code fires no input event, so a key arriving from a file
	// would look unlocked and the field would never appear. That is the exact case this
	// exists for.
	if !strings.Contains(script, "reviewLock()") {
		t.Error("loading an identity from a file does not re-check whether it is locked")
	}
	// A public identity is never locked, and offering a passphrase box under public
	// data teaches people to type secrets where none belong.
	if !strings.Contains(script, "noisecrypt-secret") {
		t.Error("the unlock field is not restricted to private identity fields")
	}
}
