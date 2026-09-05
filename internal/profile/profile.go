// Package profile holds the channel profiles: the parameters that decide how many
// bytes fit in a frame and how much abuse that frame survives.
//
// Every number in this file is a hypothesis until `noisecrypt simulate` measures
// it. That is stated here rather than buried, because the tool this project grew
// out of shipped a hand-tuned constant with no way for anyone to check it, and a
// constant nobody can check is indistinguishable from a guess.
package profile

import (
	"fmt"
	"sort"
	"strings"
)

// Profile describes one point on the density-versus-survival curve.
type Profile struct {
	// Name is the value accepted by the --profile flag.
	Name string

	// Summary is the one-line description shown by `noisecrypt profiles`.
	Summary string

	// Width and Height are the encoded frame dimensions in pixels.
	Width, Height int

	// FPS is the frame rate of the produced video.
	FPS int

	// CellSize is the edge length, in pixels, of one macro cell. Larger cells
	// survive blur and downscaling; smaller cells carry more data.
	CellSize int

	// Levels is the number of distinguishable amplitude levels per cell. Two
	// levels means one bit per cell, four means two bits, sixteen means four.
	Levels int

	// Margin is the quiet border, in pixels, left on each side. Platforms crop and
	// letterbox; a border gives the geometry recovery something to lose.
	Margin int

	// Redundancy is the fraction of parity symbols the erasure layer adds on top
	// of the payload. 0.15 means fifteen percent overhead.
	Redundancy float64

	// Verified records whether these parameters have been measured against a real
	// re-encoding pass, or are still an educated guess.
	Verified bool
}

// Archive targets channels we control: a file on a drive, in object storage, on a
// USB stick, in a torrent. Nothing re-encodes it, so density can be pushed hard.
//
// Four-pixel cells at four levels give two bits per cell. That is roughly seventy
// times the throughput of a one-bit-per-cell scheme at a comparable resolution, and
// it is only defensible because the channel is lossless: the same settings pushed
// through a social platform would not decode at all.
var Archive = Profile{
	Name:       "archive",
	Summary:    "dense, for channels that do not re-encode (disk, object storage, USB, torrent)",
	Width:      1920,
	Height:     1080,
	FPS:        30,
	CellSize:   4,
	Levels:     4,
	Margin:     16,
	Redundancy: 0.15,
	Verified:   false,
}

// Social targets platforms that re-encode aggressively: vertical video, heavy
// downscaling, deblocking, variable bitrate.
//
// The cell size is not arbitrary. A platform that scales 1080 wide down to 576 is
// applying a factor of 8/15, and a cell size that is not a multiple of 15 lands on
// fractional pixel boundaries after the resize, smearing every cell edge into its
// neighbour. 30 gives 30 x 8/15 = 16 pixels exactly. This is the one piece of
// tuning from prior art in this space that is genuinely load-bearing rather than
// folklore, and it generalises: pick a cell size divisible by the denominator of
// the platform's scaling factor.
//
// Redundancy is high on purpose. On a hostile channel, spending forty percent on
// parity to decode at all beats spending fifteen and decoding nothing.
var Social = Profile{
	Name:       "social",
	Summary:    "robust, for platforms that re-encode (vertical 9:16, heavy downscaling)",
	Width:      1080,
	Height:     1920,
	FPS:        30,
	CellSize:   30,
	Levels:     2,
	Margin:     30,
	Redundancy: 0.40,
	Verified:   false,
}

var registry = map[string]Profile{
	Archive.Name: Archive,
	Social.Name:  Social,
}

// Names returns the available profile names in a stable order.
func Names() []string {
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// All returns every profile in a stable order.
func All() []Profile {
	out := make([]Profile, 0, len(registry))
	for _, n := range Names() {
		out = append(out, registry[n])
	}
	return out
}

// Lookup resolves a profile by name.
func Lookup(name string) (Profile, error) {
	p, ok := registry[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		return Profile{}, fmt.Errorf("unknown profile %q, available: %s", name, strings.Join(Names(), ", "))
	}
	return p, nil
}

// BitsPerCell returns how many bits one cell carries.
func (p Profile) BitsPerCell() int {
	switch p.Levels {
	case 2:
		return 1
	case 4:
		return 2
	case 16:
		return 4
	default:
		return 0
	}
}

// Grid returns the number of cell columns and rows that fit in a frame.
func (p Profile) Grid() (cols, rows int) {
	usableW := p.Width - 2*p.Margin
	usableH := p.Height - 2*p.Margin
	if usableW <= 0 || usableH <= 0 {
		return 0, 0
	}
	return usableW / p.CellSize, usableH / p.CellSize
}

// RawBytesPerFrame is the modulation capacity of one frame, before the erasure
// layer takes its share.
func (p Profile) RawBytesPerFrame() int {
	cols, rows := p.Grid()
	return cols * rows * p.BitsPerCell() / 8
}

// PayloadBytesPerFrame is what actually reaches the container layer, after parity.
func (p Profile) PayloadBytesPerFrame() int {
	raw := float64(p.RawBytesPerFrame())
	return int(raw / (1 + p.Redundancy))
}

// Validate reports parameters that cannot produce a working codec.
func (p Profile) Validate() error {
	if p.BitsPerCell() == 0 {
		return fmt.Errorf("profile %q: %d amplitude levels, expected 2, 4 or 16", p.Name, p.Levels)
	}
	if p.CellSize <= 0 {
		return fmt.Errorf("profile %q: cell size must be positive", p.Name)
	}
	cols, rows := p.Grid()
	if cols <= 0 || rows <= 0 {
		return fmt.Errorf("profile %q: margins leave no room for cells in %dx%d", p.Name, p.Width, p.Height)
	}
	if p.FPS <= 0 {
		return fmt.Errorf("profile %q: frame rate must be positive", p.Name)
	}
	if p.Redundancy < 0 || p.Redundancy > 4 {
		return fmt.Errorf("profile %q: redundancy %.2f is outside [0, 4]", p.Name, p.Redundancy)
	}
	if p.PayloadBytesPerFrame() <= 0 {
		return fmt.Errorf("profile %q: no payload capacity left after parity", p.Name)
	}
	return nil
}

// Estimate describes what encoding a given number of bytes will cost.
//
// This is the answer to "what am I about to get into", which the tool should give
// before spending an hour rendering rather than after. On this channel the ratio
// between input size and video duration is the single most important property, and
// it is not something a user can guess.
type Estimate struct {
	Profile     Profile
	InputBytes  int64
	SealedBytes int64
	Frames      int64
	Duration    float64 // seconds
	BytesPerSec float64
	Expansion   float64 // sealed payload bytes per input byte
}

// Estimate computes the cost of encoding sealedBytes through this profile.
//
// sealedBytes is the size *after* packing and sealing, not the raw file size: the
// caller knows the compression outcome and this package should not guess at it.
func (p Profile) Estimate(inputBytes, sealedBytes int64) Estimate {
	perFrame := int64(p.PayloadBytesPerFrame())
	frames := int64(0)
	if perFrame > 0 {
		frames = (sealedBytes + perFrame - 1) / perFrame
		if frames == 0 {
			frames = 1
		}
	}

	e := Estimate{
		Profile:     p,
		InputBytes:  inputBytes,
		SealedBytes: sealedBytes,
		Frames:      frames,
		BytesPerSec: float64(perFrame) * float64(p.FPS),
	}
	if p.FPS > 0 {
		e.Duration = float64(frames) / float64(p.FPS)
	}
	if inputBytes > 0 {
		e.Expansion = float64(sealedBytes) / float64(inputBytes)
	}
	return e
}
