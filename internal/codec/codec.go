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
	if !d.read(img, area, false) {
		d.Unreadable++
		return
	}

	// Offer a second reading when the located rectangle is measurably the wrong shape.
	//
	// Every frame this codec discarded on a lossless channel turned out to be located
	// wrongly, and always the same way: a first or last line of data that happened to
	// be almost entirely dark was indistinguishable from more border and got absorbed
	// into it, leaving the rectangle exactly one cell short on one edge. Measured on
	// 1344 frames of `social`, that is seven frames; feeding the decoder the rendered
	// rectangle instead brought it to zero, which is what makes this the cause rather
	// than a correlation.
	//
	// It is not fixed by moving the 70% threshold. The border really does read at 97%
	// dark and an unlucky data line at 75%, so they are separable on a frame straight
	// out of Render, but a platform's blur closes that gap and the threshold exists to
	// survive the platform. So instead of deciding, offer both and let the CRC decide,
	// which is the arbiter this codec already trusts everywhere else.
	for _, alt := range d.reframings(area) {
		d.read(img, alt, true)
	}
}

// read samples one rectangle and queues it. Reports whether it produced anything.
func (d *Decoder) read(img *image.Gray, area image.Rectangle, speculative bool) bool {
	samples, err := geometry.Sample(img, area, d.c.cols, d.c.rows)
	if err != nil {
		return false
	}
	black, white := geometry.Calibrate(samples)
	data, confidence := d.c.modem.Demodulate(samples, modem.Calibration{Black: black, White: white})
	modem.Whiten(data) // self-inverse; confidences are per position and unaffected

	d.frames = append(d.frames, fec.ReadFrame{
		Bytes: data, Confidence: confidence, Speculative: speculative,
	})
	return true
}

// reframings proposes rectangles to try when the located one is the wrong shape.
//
// Cells are square in a rendered frame and a channel only ever rescales, crops and
// letterboxes, all axis-aligned, so a correct rectangle gives the same cell size measured
// across its width as across its height. One swallowed line breaks that by exactly one
// cell, and the dimension that comes up short says which. Which *edge* was swallowed is
// not knowable from the shape, so both are offered.
func (d *Decoder) reframings(area image.Rectangle) []image.Rectangle {
	cols, rows := d.c.cols, d.c.rows
	if cols == 0 || rows == 0 || area.Dx() <= 0 || area.Dy() <= 0 {
		return nil
	}

	cw := float64(area.Dx()) / float64(cols)
	ch := float64(area.Dy()) / float64(rows)

	// How short the smaller measurement is, counted in cells. One swallowed line makes
	// this land near 1; anything far from 1 is a differently shaped problem and not one
	// this repair understands, so it is left alone.
	const (
		near = 0.45 // how close to a whole cell the shortfall must be
		tiny = 0.08 // below this the two measurements agree and nothing is wrong
	)
	switch {
	case ch > cw:
		missing := (ch - cw) * float64(cols) / ch
		if missing < tiny || absf(missing-1) > near {
			return nil
		}
		w := int(ch + 0.5)
		return []image.Rectangle{
			image.Rect(area.Min.X-w, area.Min.Y, area.Max.X, area.Max.Y),
			image.Rect(area.Min.X, area.Min.Y, area.Max.X+w, area.Max.Y),
		}
	case cw > ch:
		missing := (cw - ch) * float64(rows) / cw
		if missing < tiny || absf(missing-1) > near {
			return nil
		}
		h := int(cw + 0.5)
		return []image.Rectangle{
			image.Rect(area.Min.X, area.Min.Y-h, area.Max.X, area.Max.Y),
			image.Rect(area.Min.X, area.Min.Y, area.Max.X, area.Max.Y+h),
		}
	}
	return nil
}

func absf(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
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
