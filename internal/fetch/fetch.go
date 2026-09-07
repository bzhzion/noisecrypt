// Package fetch downloads a video from a URL, through yt-dlp, so a container that went
// up to a platform can be brought back without the round trip through a browser and a
// downloads folder.
//
// # Why yt-dlp and not an HTTP GET
//
// The URL of a video on a platform is a page, not a file. What has to happen between
// that page and an .mp4 is extractor logic for several hundred sites that changes
// whenever a platform changes, and reimplementing any part of it here would be signing
// up to chase that forever. yt-dlp already does it.
//
// # Why it is not bundled, downloaded or installed
//
// It is found if it is there, and its absence is reported rather than repaired, for the
// same reason FFmpeg is: this tool's argument is that it depends on nothing at rest, and
// a program that fetches and runs a binary at the moment it is needed has an update
// channel whether it admits to having one or not. So the field appears when the machine
// already has yt-dlp and explains itself when it does not.
//
// # What the guards here do and do not cover
//
// Every argument is fixed, the URL comes last behind a `--` separator, and the host is
// resolved and refused if it lands on a private address. That last check is real but
// partial, and worth being exact about rather than presenting as a wall: yt-dlp resolves
// the name again itself and follows redirects on its own, so a name that answers
// differently the second time, or a public host that redirects to a private one, is not
// caught here. It is defence in depth on a tool whose URL comes from the person sitting
// at the machine, not a filter standing between a stranger and an internal network.
package fetch

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/bzhzion/noisecrypt/internal/tools"
)

// ErrNotInstalled is returned when yt-dlp cannot be found.
var ErrNotInstalled = errors.New("fetch: yt-dlp not found")

// outputBase is the name every download is given, whatever the video is called.
//
// The obvious template is the title, and the title is attacker-supplied text going into
// a path. A fixed basename removes the entire question rather than answering it with an
// escaping rule: there is nothing to traverse out of and nothing to collide with, and
// only the extension is left to yt-dlp, which needs it to say what container it wrote.
const outputBase = "download"

// Find locates yt-dlp.
func Find() (string, error) {
	found, err := tools.Locate("yt-dlp", ytdlpPlaces())
	if err != nil {
		var missing *tools.NotFoundError
		if errors.As(err, &missing) {
			return "", fmt.Errorf("%w: looked in PATH and %s", ErrNotInstalled, missing.Where)
		}
		return "", err
	}
	return found, nil
}

// ytdlpPlaces lists where yt-dlp turns up when it is not on PATH.
//
// ⚠️ The Windows glob is NOT the FFmpeg one with the name changed, and copying that one
// would have failed in the quietest possible way: the field would simply have declared
// itself unavailable on a machine that has yt-dlp installed and working. FFmpeg's
// package unpacks an archive, so its binary sits at
// Packages\*FFmpeg*\<version>\bin\ffmpeg.exe, two levels down. yt-dlp ships the
// executable itself, so it lands directly in Packages\yt-dlp.yt-dlp_...\yt-dlp.exe with
// no version directory and no bin. Verified against a real install on 2026-09-07, which
// was also on no PATH at all: WinGet shims neither of these two.
func ytdlpPlaces() tools.Places {
	var dirs, globs []string
	switch runtime.GOOS {
	case "windows":
		local := os.Getenv("LOCALAPPDATA")
		dirs = []string{
			filepath.Join(local, `Microsoft\WinGet\Links`),
			filepath.Join(local, `Programs\yt-dlp`),
			filepath.Join(os.Getenv("USERPROFILE"), `scoop\shims`),
			`C:\ProgramData\chocolatey\bin`,
		}
		globs = []string{filepath.Join(local, `Microsoft\WinGet\Packages`, `*yt-dlp*`)}
	case "darwin":
		dirs = []string{"/opt/homebrew/bin", "/usr/local/bin", "/opt/local/bin", "/usr/bin"}
	default:
		dirs = []string{
			"/usr/bin", "/usr/local/bin", "/snap/bin",
			"/var/lib/flatpak/exports/bin",
			filepath.Join(os.Getenv("HOME"), ".local/bin"),
		}
	}
	return tools.Places{Dirs: dirs, Globs: globs}
}

// CheckURL rejects a URL this package will not hand to yt-dlp, and returns it parsed.
//
// Separated from Get so it can be tested without yt-dlp installed and called before any
// work starts, which is the difference between refusing a URL and refusing it after a
// download.
func CheckURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("no address given")
	}
	// Checked before parsing, because a string beginning with a dash is not a bad URL,
	// it is an option. The `--` separator in Get is what actually stops it being read as
	// one; this is the second lock, and it also gives a comprehensible message instead
	// of yt-dlp complaining about an unknown flag.
	if strings.HasPrefix(raw, "-") {
		return nil, errors.New("an address cannot begin with a dash")
	}
	if strings.ContainsAny(raw, "\x00\n\r") {
		return nil, errors.New("the address contains a control character")
	}

	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("that is not an address: %w", err)
	}
	// https only. Not for the confidentiality of a request for a public video, but
	// because the answer to that request decides what gets downloaded and run through a
	// decoder, and over plain http anyone on the path chooses it.
	if u.Scheme != "https" {
		return nil, fmt.Errorf("only https addresses are accepted, this one is %q", u.Scheme)
	}
	if u.Hostname() == "" {
		return nil, errors.New("the address has no host")
	}
	if err := resolvesOnlyPublic(u.Hostname()); err != nil {
		return nil, err
	}
	return u, nil
}

// resolvesOnlyPublic refuses a host that lands anywhere on a private network.
//
// Every address is checked rather than the first, because a name that answers with one
// public address and one loopback address is exactly the shape used to get past a check
// that stops at the first answer.
func resolvesOnlyPublic(host string) error {
	addrs, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("cannot resolve %q: %w", host, err)
	}
	if len(addrs) == 0 {
		return fmt.Errorf("%q resolves to nothing", host)
	}
	for _, ip := range addrs {
		if !isPublic(ip) {
			return fmt.Errorf("%q resolves to %s, which is not a public address", host, ip)
		}
	}
	return nil
}

func isPublic(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() {
		return false
	}
	// Carrier-grade NAT, 100.64.0.0/10. Not covered by IsPrivate, and on this parc it is
	// the Tailscale range: every machine reachable over the tailnet answers in it. A
	// check that stops at RFC 1918 lets the whole private network through here.
	if v4 := ip.To4(); v4 != nil && v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
		return false
	}
	return true
}

// Get downloads the video at raw into dir and returns the path to the file it wrote.
//
// dir is expected to be a directory this caller owns and will remove; nothing is written
// outside it.
func Get(ctx context.Context, ytdlp, raw, dir string, maxBytes int64) (string, error) {
	u, err := CheckURL(raw)
	if err != nil {
		return "", err
	}

	args := []string{
		// ⚠️ First and load-bearing. Without it yt-dlp reads its own configuration
		// files, and those can carry any option it accepts, `--exec` included: a
		// poisoned config in the user's profile would turn pasting a URL into running a
		// command. Every option this tool relies on below is also simply overridable
		// from a config file, so the fixed argument list only means anything with this
		// in front of it.
		"--no-config",
		"--no-playlist",
		// The highest-quality video stream, and no audio at all. Audio carries nothing
		// here, and asking for it means yt-dlp merges two streams through FFmpeg for a
		// track that gets thrown away. Quality is not a preference in this tool: a lower
		// rendition has fewer pixels per macropixel, and below the profile's tolerance
		// the file does not come back. `/b` is the fallback for a site that offers no
		// separate video stream.
		"-f", "bv*/b",
		// No re-encode, and no remux either unless the container demands it. The decoder
		// reads whatever FFmpeg can demux, so mp4 and webm are both fine, and forcing
		// one would add a pass over the file for nothing.
		"--no-part",
		"--no-mtime",
		"-o", outputBase + ".%(ext)s",
		"--paths", dir,
	}
	if maxBytes > 0 {
		// Bounds the download at the source rather than after it. yt-dlp checks the
		// declared size before starting and abandons a fragmented download that grows
		// past it, so this is a real ceiling and not a post-mortem.
		args = append(args, "--max-filesize", fmt.Sprint(maxBytes))
	}
	// The separator, and the reason the URL is last. Everything after `--` is a
	// positional argument however it is spelled.
	args = append(args, "--", u.String())

	cmd := exec.CommandContext(ctx, ytdlp, args...)
	cmd.Dir = dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("yt-dlp failed: %w: %s", err, tail(stderr.String()))
	}

	matches, err := filepath.Glob(filepath.Join(dir, outputBase+".*"))
	if err != nil || len(matches) == 0 {
		// A zero exit with nothing on disk is what `--max-filesize` does when the video
		// is too large: it reports the skip and succeeds. Read as "downloaded nothing",
		// which is true and the useful thing to say.
		return "", fmt.Errorf("yt-dlp downloaded nothing: %s", tail(stderr.String()))
	}

	// The largest, not the first. One stream was asked for and one is normally what
	// arrives, but a site that only offers fragments can leave per-format leftovers
	// beside the result, and alphabetical order picks between them by accident.
	path := matches[0]
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	for _, candidate := range matches[1:] {
		other, err := os.Stat(candidate)
		if err == nil && other.Size() > info.Size() {
			path, info = candidate, other
		}
	}
	if info.Size() == 0 {
		return "", errors.New("the downloaded file is empty")
	}
	// Checked again on disk. `--max-filesize` works from the size a site declares, and a
	// site that declares nothing, or declares wrongly, gets past it.
	if maxBytes > 0 && info.Size() > maxBytes {
		return "", fmt.Errorf("the downloaded video is %d bytes, over the %d byte limit",
			info.Size(), maxBytes)
	}
	return path, nil
}

// tail keeps the end of yt-dlp's diagnostics, which is where the reason is. The whole of
// it is progress lines.
func tail(s string) string {
	s = strings.TrimSpace(s)
	const keep = 500
	if len(s) <= keep {
		return s
	}
	return "..." + s[len(s)-keep:]
}
