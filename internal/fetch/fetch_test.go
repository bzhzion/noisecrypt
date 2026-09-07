package fetch

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// RequireEnv turns a missing yt-dlp from a skipped test into a failing one, for the same
// reason video.RequireEnv exists: a suite that skips silently is how a project ends up
// green while testing nothing.
const RequireEnv = "NOISECRYPT_REQUIRE_YTDLP"

func TestCheckURLRefusesWhatItShould(t *testing.T) {
	// Each of these is a real way in rather than a category of bad string, which is why
	// the reason is asserted and not merely the refusal: a URL rejected by the wrong
	// rule is a rule that will let the right attack through the day the wording changes.
	cases := []struct {
		name, raw, because string
	}{
		{"empty", "", "no address"},
		// The one the `--` separator exists for. Without both, `--exec` in the address
		// field is an option and not an address.
		{"option", "--exec=calc.exe", "cannot begin with a dash"},
		{"short option", "-o/tmp/x", "cannot begin with a dash"},
		{"newline", "https://example.com/\n--exec=calc", "control character"},
		{"nul", "https://example.com/\x00", "control character"},
		{"plain http", "http://example.com/video", "only https"},
		{"file", "file:///etc/passwd", "only https"},
		// yt-dlp accepts a bare name and guesses; there is nothing to guess from here.
		{"no host", "https:///video", "no host"},
		{"loopback", "https://127.0.0.1/video", "not a public address"},
		{"loopback v6", "https://[::1]/video", "not a public address"},
		{"private", "https://192.168.1.1/video", "not a public address"},
		{"private 10", "https://10.0.0.1/video", "not a public address"},
		// 100.64.0.0/10 is not covered by net.IP.IsPrivate, and on this parc it is the
		// whole tailnet. A check that stopped at RFC 1918 would wave this through.
		{"carrier-grade nat", "https://100.100.0.1/video", "not a public address"},
		{"link local", "https://169.254.169.254/latest/meta-data/", "not a public address"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := CheckURL(c.raw)
			if err == nil {
				t.Fatalf("CheckURL(%q) was accepted", c.raw)
			}
			if !strings.Contains(err.Error(), c.because) {
				t.Errorf("CheckURL(%q) refused for %q, expected %q",
					c.raw, err, c.because)
			}
		})
	}
}

// The guard has to let a real address through, or it is not a guard but an outage. Pinned
// separately because every refusal above would still pass if CheckURL rejected
// everything, which is the failure mode of a validator nobody tests positively.
func TestCheckURLAcceptsAPublicAddress(t *testing.T) {
	u, err := CheckURL("  https://www.youtube.com/watch?v=dQw4w9WgXcQ  ")
	if err != nil {
		t.Skipf("cannot resolve a public name here, so this proves nothing: %v", err)
	}
	if u.Host != "www.youtube.com" {
		t.Errorf("host is %q", u.Host)
	}
	// The query has to survive. It is the whole of the video identifier on several
	// platforms, and a validator that normalised it away would hand yt-dlp a channel.
	if u.Query().Get("v") != "dQw4w9WgXcQ" {
		t.Errorf("the video identifier was lost: %q", u.String())
	}
}

// Not "does yt-dlp exist on this machine" but "does the search know where to look".
// The Windows glob is the part worth pinning: it is deliberately a different shape from
// the FFmpeg one, and getting it wrong makes the feature declare itself unavailable on a
// machine that has the tool, which is a failure nothing reports.
func TestFindReportsAbsenceAsAbsence(t *testing.T) {
	path, err := Find()
	if err == nil {
		if path == "" {
			t.Fatal("Find returned no error and no path")
		}
		if _, statErr := os.Stat(path); statErr != nil {
			t.Errorf("Find returned %q, which is not there: %v", path, statErr)
		}
		return
	}
	if os.Getenv(RequireEnv) != "" {
		t.Fatalf("%s is set but yt-dlp was not found: %v", RequireEnv, err)
	}
	if !errors.Is(err, ErrNotInstalled) {
		t.Errorf("absence was reported as %v, which callers cannot tell from a failure", err)
	}
	// The message has to say where it looked, or "not found" is unactionable.
	if !strings.Contains(err.Error(), "looked in PATH") {
		t.Errorf("the error does not say where it searched: %q", err)
	}
}
