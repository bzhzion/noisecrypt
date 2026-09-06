// Package profile holds the channel profiles: the parameters that decide how many
// bytes fit in a frame and how much abuse that frame survives.
//
// Nothing here declares its own overhead. Frame capacity, payload size and
// redundancy are all derived from the actual error-correcting layout, so a tuning
// change cannot quietly leave the advertised figure behind. A profile that states
// its redundancy as a constant is stating an intention, not a fact.
//
// The geometry itself is still a hypothesis until `noisecrypt simulate` measures it
// against a real re-encoding pass, and every profile says so through Verified. That
// is stated rather than buried, because the tool this project grew out of shipped a
// hand-tuned constant with no way for anyone to check it, and a constant nobody can
// check is indistinguishable from a guess.
package profile

import (
	"fmt"
	"sort"
	"strings"

	"github.com/bzhzion/noisecrypt/internal/fec"
)

// Profile describes one point on the density-versus-survival curve.
type Profile struct {
	// Name is the value accepted by the -profile flag.
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

	// IntraParityRatio is the fraction of a frame's sub-shards spent on parity
	// against cell-level damage. Measured effect, on the social geometry: 25
	// percent survives up to 2.8 percent raw byte errors, 50 percent survives up
	// to 4.3 percent and costs a third of the payload.
	IntraParityRatio float64

	// InterData and InterParity size the code across frames, against whole frames
	// being dropped by rate conversion or cuts.
	InterData, InterParity int

	// Evidence records what this geometry has actually been measured against.
	//
	// It used to be a boolean named Verified, and the boolean was quietly wrong about
	// Archive. A platform round trip was the only thing it counted, so Archive read as
	// false, which alongside two profiles reading true says "not tested yet" when the
	// truth is that Archive has no platform to be tested against: it targets channels
	// nobody re-encodes, and it *was* measured, locally, against real H.264.
	//
	// Two states cannot express three situations. A boolean here forces "measured
	// elsewhere" and "not measured" into the same value, which is the shape of claim
	// this file exists to avoid making.
	Evidence Evidence

	// EvidenceNote is the one-line detail behind Evidence: which platform, or how far
	// the local re-encode was pushed. Displayed as-is, so it stays short.
	EvidenceNote string
}

// Evidence is how far a profile's geometry has been carried against something real.
type Evidence int

const (
	// EvidenceNone means the geometry is still an educated guess.
	EvidenceNone Evidence = iota

	// EvidenceLocal means it survived a real encoder locally, but no platform. For a
	// profile whose whole premise is a channel nobody re-encodes, this is the ceiling
	// of what can be measured, not a step short of it.
	EvidenceLocal

	// EvidencePlatform means a container went up to a real platform, came back through
	// its rendition ladder, and decoded byte for byte.
	EvidencePlatform
)

// Verified reports whether the geometry has been measured against a real platform.
// Kept because that is a genuinely different question from "has it been measured".
func (e Evidence) Verified() bool { return e == EvidencePlatform }

func (e Evidence) String() string {
	switch e {
	case EvidencePlatform:
		return "platform"
	case EvidenceLocal:
		return "local"
	default:
		return "unmeasured"
	}
}

// Archive targets channels we control: a file on a drive, in object storage, on a
// USB stick, in a torrent. Nothing re-encodes it, so density can be pushed hard.
//
// Four-pixel cells at four levels give two bits per cell, which is only defensible
// because the channel is lossless: the same settings pushed through a social
// platform would not decode at all. Parity is correspondingly thin, and exists for
// bit rot and partial downloads rather than for compression damage.
var Archive = Profile{
	Name:             "archive",
	Summary:          "dense, for channels that do not re-encode (disk, object storage, USB, torrent)",
	Width:            1920,
	Height:           1080,
	FPS:              30,
	CellSize:         4,
	Levels:           4,
	Margin:           16,
	IntraParityRatio: 0.07,
	InterData:        64,
	InterParity:      6,
	// No platform round trip, and there never will be one: this profile exists for
	// channels that do not re-encode. Measured against a real H.264 pass instead,
	// which is the strongest evidence its premise admits.
	Evidence:     EvidenceLocal,
	EvidenceNote: "H.264 to CRF 23",
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
// Parity is heavy on both layers on purpose. On a hostile channel, spending most of
// the frame on redundancy and decoding at all beats spending a little and decoding
// nothing.
//
// # Measured, 2026-09-05
//
// A 45 second, 160 KiB payload was uploaded to YouTube as an unlisted Short and every
// rendition YouTube produced was downloaded and decoded. All of them recovered the
// payload byte for byte: 1080x1920 in H.264, VP9 and AV1, then 720x1280, 608x1080,
// 480x854, 360x640, 240x426 and 144x256.
//
// Two things that came out of it and should shape the next change here.
//
// YouTube cost nothing. Three frames of 1344 were unreadable, and they were the same
// three that failed in the local decode before the upload, so they come from this
// codec's own registration and not from the platform.
//
// This profile is heavily overbuilt. It was designed to survive a scaling factor of
// 8/15 and it survived 1/7.5, with cells reduced to four pixels and AV1 at 427 kbit/s,
// a twentieth of the source bitrate. The 114 percent overhead buys far more margin
// than the channel demands, so there is room for a much denser social profile. That
// is a change to make on measurements, not on this observation alone: one upload of
// one video is not a survey of the platform.
var Social = Profile{
	Name:             "social",
	Summary:          "toughest, for platforms, readable even from the bottom of the rendition ladder",
	Width:            1080,
	Height:           1920,
	FPS:              30,
	CellSize:         30,
	Levels:           2,
	Margin:           30,
	IntraParityRatio: 0.25,
	InterData:        24,
	InterParity:      8,
	Evidence:         EvidencePlatform, // 2026-09-05
	EvidenceNote:     "YouTube, 9 renditions",
}

// SocialHD trades the very bottom of a platform's rendition ladder for four and a half
// times the payload.
//
// # The measurement this profile exists because of
//
// `simulate` was extended to downscale as well as re-compress, which is what a platform
// actually does, and cell sizes were swept against the ladder. Everything survived far
// lower than expected, and the useful figure turned out not to be the resolution but the
// pixels left per cell:
//
//	30 px cells hold down to 256p (4.0 px per cell)
//	20 px cells hold down to 426p (4.4 px per cell)
//	15 px cells hold down to 426p (3.3 px per cell)
//	12 px cells fail at   426p (2.7 px per cell)
//	10 px cells fail at   426p (2.2 px per cell)
//
// So 15 px is the densest geometry that still clears a 426p floor, which is the lowest
// rendition anyone would deliberately retrieve data from.
//
// # The non-monotonic result, which is the interesting part
//
// 15 px cells fail at 320p and then succeed again at 256p. Lower resolution, better
// outcome. The cause is fractional cell boundaries: 1920/320 is 6 and 15/6 is 2.5, so
// every cell straddles a pixel; 1920/256 is 7.5 and 15/7.5 is exactly 2, so they line up.
//
// The rule that falls out is more useful than "pick a multiple of the denominator":
// above roughly 6 pixels per cell a fractional boundary is absorbed, because there are
// interior pixels to average; below about 3 it is fatal, because there are none.
//
// # The platform round trip, 2026-09-05
//
// A 46 second Short carrying 750 KiB was uploaded unlisted and every rendition pulled
// back. All ten recovered the payload byte for byte, 1080x1920 down to 144x256 across
// H.264, VP9 and AV1, with **zero unreadable frames anywhere**.
//
// Two results worth carrying forward.
//
// The simulation was pessimistic, not optimistic, which is the safe direction to be
// wrong in. It predicted failure at 320p; YouTube does not produce a 320p rendition, so
// the one geometry this profile dislikes never arises there. Do not read that as the
// simulation being wrong: read it as this profile having a real weak point that this
// particular platform happens not to hit.
//
// And the three frames that were always unreadable under the 30 px profile are gone.
// They came from the border detector treating a mostly dark first row of data as more
// border, and halving the cell size doubles the cells per row, which makes such a row
// far less likely. So the denser profile is not merely faster, it is cleaner.
//
// On YouTube specifically this profile is therefore strictly better than Social: same
// survival, four and a half times the payload, fewer lost frames. Social remains the
// right answer for a platform nobody has measured.
var SocialHD = Profile{
	Name:             "social-hd",
	Summary:          "denser, for platforms, when you can retrieve at 426p or better",
	Width:            1080,
	Height:           1920,
	FPS:              30,
	CellSize:         15,
	Levels:           2,
	Margin:           30,
	IntraParityRatio: 0.25,
	InterData:        24,
	InterParity:      8,
	Evidence:         EvidencePlatform, // zero lost frames, 2026-09-05
	EvidenceNote:     "YouTube, 10 renditions",
}

var registry = map[string]Profile{
	Archive.Name:  Archive,
	Social.Name:   Social,
	SocialHD.Name: SocialHD,
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

// RawBytesPerFrame is the modulation capacity of one frame, before any coding.
func (p Profile) RawBytesPerFrame() int {
	cols, rows := p.Grid()
	return cols * rows * p.BitsPerCell() / 8
}

// Layout builds the error-correcting layout this profile implies.
func (p Profile) Layout() (fec.Layout, error) {
	return fec.NewLayout(p.RawBytesPerFrame(), p.IntraParityRatio, p.InterData, p.InterParity)
}

// PayloadBytesPerFrame is what actually reaches the container layer, averaged over
// a block so that the parity frames are accounted for.
func (p Profile) PayloadBytesPerFrame() int {
	l, err := p.Layout()
	if err != nil {
		return 0
	}
	return l.ShardSize() * l.InterData / l.FramesPerBlock()
}

// Redundancy is the measured overhead of the layout, not a declared constant.
func (p Profile) Redundancy() float64 {
	l, err := p.Layout()
	if err != nil {
		return 0
	}
	return l.Overhead()
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
	if _, err := p.Layout(); err != nil {
		return fmt.Errorf("profile %q: %w", p.Name, err)
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
	e := Estimate{
		Profile:     p,
		InputBytes:  inputBytes,
		SealedBytes: sealedBytes,
		BytesPerSec: float64(p.PayloadBytesPerFrame()) * float64(p.FPS),
	}

	l, err := p.Layout()
	if err != nil {
		return e
	}
	e.Frames = int64(l.FrameCount(int(sealedBytes)))
	if p.FPS > 0 {
		e.Duration = float64(e.Frames) / float64(p.FPS)
	}
	if inputBytes > 0 {
		e.Expansion = float64(sealedBytes) / float64(inputBytes)
	}
	return e
}
