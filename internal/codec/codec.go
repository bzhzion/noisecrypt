// Package codec joins the four layers into one pipeline: payload bytes to frame
// images and back.
//
// The layers are deliberately unaware of each other. Package fec knows nothing about
// pixels, package geometry knows nothing about error correction, and package modem
// knows nothing about either. This one is where they meet, and it exists mainly so
// that meeting happens in exactly one place: the encode and decode paths have to
// agree on cell counts, frame sizes and orderings, and a disagreement between two
// copies of that arithmetic is the classic way a codec produces output that decodes
// to something almost right.
//
// # Streaming, on the encode side only
//
// Encoding hands frames to a callback instead of returning a slice. Forty megabytes
// through the archive profile is sixteen hundred frames of 1920 by 1080, which is
// over three gigabytes of image if held at once, for no reason: each frame is
// finished the moment it is drawn.
//
// Decoding does hold everything, because it must. The inter-frame code needs a whole
// block before it can repair anything, and frames arrive in whatever order the video
// gives them. What it holds is the demodulated bytes, not the images, so the same
// forty megabytes costs about fifty rather than three thousand.
package codec

import (
	"fmt"
	"image"

	"github.com/bzhzion/noisecrypt/internal/fec"
	"github.com/bzhzion/noisecrypt/internal/geometry"
	"github.com/bzhzion/noisecrypt/internal/modem"
	"github.com/bzhzion/noisecrypt/internal/profile"
)

// Codec encodes and decodes for one channel profile.
type Codec struct {
	profile  profile.Profile
	geometry geometry.Geometry
	modem    modem.Modem
	layout   fec.Layout
	cols     int
	rows     int
}

// New builds a codec for a profile.
func New(p profile.Profile) (*Codec, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}

	g := geometry.Geometry{
		Width:    p.Width,
		Height:   p.Height,
		Margin:   p.Margin,
		CellSize: p.CellSize,
	}
	if err := g.Validate(); err != nil {
		return nil, err
	}

	m, err := modem.New(p.Levels)
	if err != nil {
		return nil, err
	}

	l, err := p.Layout()
	if err != nil {
		return nil, err
	}

	// The profile and the geometry compute the grid independently, from the same
	// inputs. If they ever disagree, every frame would carry a different number of
	// bytes than the error correction expects, and the failure would look like
	// random corruption rather than an arithmetic bug. Checking it here costs one
	// comparison at startup.
	cols, rows := g.Grid()
	if got, want := m.BytesForCells(cols*rows), p.RawBytesPerFrame(); got != want {
		return nil, fmt.Errorf("codec: geometry gives %d bytes per frame, profile expects %d", got, want)
	}
	if l.FrameBytes != p.RawBytesPerFrame() {
		return nil, fmt.Errorf("codec: layout is built for %d byte frames, profile gives %d",
			l.FrameBytes, p.RawBytesPerFrame())
	}

	return &Codec{profile: p, geometry: g, modem: m, layout: l, cols: cols, rows: rows}, nil
}

// Profile returns the profile this codec was built for.
func (c *Codec) Profile() profile.Profile { return c.profile }

// Layout returns the error-correcting layout in use.
func (c *Codec) Layout() fec.Layout { return c.layout }

// FrameCount returns how many frames a payload of n bytes will produce.
func (c *Codec) FrameCount(n int) int { return c.layout.FrameCount(n) }

// EncodeTo renders the payload as frames, handing each to emit in order.
//
// An error from emit stops the encode and is returned unchanged, so a caller writing
// to a pipe can abort without unwinding a partial slice of images.
func (c *Codec) EncodeTo(payload []byte, emit func(index int, img *image.Gray) error) error {
	frames, err := fec.Encode(payload, c.layout)
	if err != nil {
		return err
	}

	cells := c.cols * c.rows
	for i, f := range frames {
		// Energy dispersal, before modulation and undone right after demodulation.
		// See modem.Whiten: without it a frame whose bytes start with zeros draws
		// solid black rows that the registration border cannot be told apart from.
		whitened := make([]byte, len(f.Bytes))
		copy(whitened, f.Bytes)
		modem.Whiten(whitened)

		symbols := c.modem.Modulate(whitened, cells)
		img, err := c.geometry.Render(symbols, c.modem.Amplitude)
		if err != nil {
			return fmt.Errorf("codec: rendering frame %d: %w", i, err)
		}
		if err := emit(i, img); err != nil {
			return err
		}
	}
	return nil
}

// Decoder accumulates frames read back from a channel.
type Decoder struct {
	c      *Codec
	frames []fec.ReadFrame

	// Seen and Unreadable count what went in, for reporting. A caller usually wants
	// to know that four hundred of five hundred frames were legible even when the
	// decode succeeds, because that is the difference between comfortable and lucky.
	Seen       int
	Unreadable int

	// Stats is what the error-correcting layer discarded, filled in by Finish.
	Stats fec.Stats
}

// NewDecoder starts a decode.
func (c *Codec) NewDecoder() *Decoder { return &Decoder{c: c} }

// Add reads one frame image.
//
// A frame that cannot be located or sampled is counted and dropped rather than
// reported as an error. On this channel unreadable frames are the expected case, and
// a decoder that stopped at the first one would never finish a real video.
func (d *Decoder) Add(img *image.Gray) {
	d.Seen++

	area, err := geometry.Locate(img)
	if err != nil {
		d.Unreadable++
		return
	}
	samples, err := geometry.Sample(img, area, d.c.cols, d.c.rows)
	if err != nil {
		d.Unreadable++
		return
	}

	black, white := geometry.Calibrate(samples)
	data, confidence := d.c.modem.Demodulate(samples, modem.Calibration{Black: black, White: white})
	modem.Whiten(data) // self-inverse; confidences are per position and unaffected

	d.frames = append(d.frames, fec.ReadFrame{Bytes: data, Confidence: confidence})
}

// Finish reconstructs the payload from everything added so far.
func (d *Decoder) Finish() ([]byte, error) {
	if len(d.frames) == 0 {
		return nil, fmt.Errorf("codec: none of the %d frames seen were readable", d.Seen)
	}
	payload, stats, err := fec.DecodeStats(d.frames, d.c.layout)
	d.Stats = stats
	return payload, err
}

// Discarded is every frame that reached the error-correcting layer and contributed
// nothing, which is a different loss from Unreadable and used to be reported nowhere.
//
// Unreadable counts frames the geometry could not locate. Discarded counts frames it
// located and sampled, whose shard then failed its CRC under every erasure combination
// the parity allowed. A mislocated frame lands here, and so does a badly damaged one.
// Both are erasures to the inter-frame code, which is the cheap kind of loss, so this is
// a margin indicator and not a failure. Reporting only Unreadable let a video that lost
// frames be described as losing none.
func (d *Decoder) Discarded() int { return d.Stats.Lost() }
