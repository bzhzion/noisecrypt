// Package modem turns bytes into cell amplitudes and back.
//
// The design decision that shapes everything else here is soft demodulation.
//
// The obvious way to read a cell back is to compare it against a threshold and
// emit a bit: `grey < 128 ? 0 : 1`. That works, and it throws away the single most
// valuable thing the channel gives you. A cell measured at 130 and a cell measured
// at 250 both become "one", even though the first is a coin flip and the second is
// a certainty. By the time the error correction layer sees the data, every byte
// looks equally trustworthy, so the only thing it can do is spend parity uniformly.
//
// This package instead returns a confidence alongside every cell: how far the
// measurement sat from the nearest decision boundary. The layer above uses it to
// mark the least trustworthy pieces as erasures rather than errors, and that
// distinction is not cosmetic. A Reed-Solomon code corrects twice as many erasures
// as errors, because an erasure comes with its position and an error does not. Soft
// demodulation is what turns unknown-position errors into known-position erasures,
// and it doubles the effective strength of the parity for free.
//
// # What confidence is not
//
// Confidence measures how cleanly a sample reads as *some* symbol, not how likely it
// is to be the symbol that was sent. Those come apart in a specific and important
// way: a cell pushed far past a decision boundary lands close to the neighbouring
// level and therefore reports *high* confidence while being wrong. Noise does not
// only blur cells, it sometimes carries them cleanly into the wrong bin.
//
// The measured consequence, from the test suite: at four levels under noise heavy
// enough to corrupt fourteen percent of bytes, the least confident ten percent of
// bytes contained thirty-six percent of the corruption. That is three and a half
// times better than chance, which makes it a good way to *rank* erasure candidates,
// and useless as a way to *certify* correctness.
//
// So the layer above must never trust confidence alone. It uses it to choose which
// shards to erase, and a checksum to decide whether the reconstruction actually
// worked. Anyone tempted to drop that checksum because "the confidences look fine"
// should read this paragraph again.
package modem

import (
	"errors"
	"fmt"
)

// Modem modulates and demodulates for one amplitude alphabet.
type Modem struct {
	levels      int
	bitsPerCell int
	mask        byte
}

// ErrUnsupportedLevels is returned for an alphabet size that is not a power of two
// between 2 and 16.
var ErrUnsupportedLevels = errors.New("modem: unsupported level count")

// New builds a modem for the given number of amplitude levels.
func New(levels int) (Modem, error) {
	var bits int
	switch levels {
	case 2:
		bits = 1
	case 4:
		bits = 2
	case 16:
		bits = 4
	default:
		return Modem{}, fmt.Errorf("%w: %d, expected 2, 4 or 16", ErrUnsupportedLevels, levels)
	}
	return Modem{levels: levels, bitsPerCell: bits, mask: byte(levels - 1)}, nil
}

// Levels returns the alphabet size.
func (m Modem) Levels() int { return m.levels }

// BitsPerCell returns how many bits one cell carries.
func (m Modem) BitsPerCell() int { return m.bitsPerCell }

// CellsForBytes returns how many cells are needed to carry n bytes.
func (m Modem) CellsForBytes(n int) int {
	return n * 8 / m.bitsPerCell
}

// BytesForCells returns how many whole bytes fit in n cells.
func (m Modem) BytesForCells(n int) int {
	return n * m.bitsPerCell / 8
}

// Amplitude maps a symbol to an 8-bit grey value.
//
// The levels are spread across the full range, so two levels give 0 and 255 rather
// than something more polite. Contrast is the whole budget: every step of margin
// between a level and the decision boundary is margin the video codec's blur has to
// eat through before a cell flips.
func (m Modem) Amplitude(symbol byte) uint8 {
	s := int(symbol & m.mask)
	return uint8((s*255 + (m.levels-1)/2) / (m.levels - 1))
}

// Modulate converts bytes into one symbol per cell, most significant bits first.
//
// Any cells beyond the data are filled with a deterministic pseudo-random pattern
// derived from the cell index. Leaving them flat would produce a large uniform
// region, which a video encoder compresses to almost nothing and then reproduces
// with ringing at its edges; filling them with noise keeps the frame's statistics
// uniform and costs nothing, because the decoder ignores those cells anyway.
func (m Modem) Modulate(data []byte, cells int) []byte {
	out := make([]byte, cells)

	used := min(m.CellsForBytes(len(data)), cells)
	for i := range used {
		bitOffset := i * m.bitsPerCell
		byteIdx := bitOffset / 8
		shift := 8 - m.bitsPerCell - (bitOffset % 8)
		out[i] = (data[byteIdx] >> shift) & m.mask
	}

	for i := used; i < cells; i++ {
		out[i] = byte(filler(i)) & m.mask
	}
	return out
}

// filler is a cheap deterministic scrambler. It does not need to be a good hash, it
// needs to avoid runs, and it must be identical on both sides so a decoder can tell
// filler from data if it ever needs to.
func filler(i int) uint32 {
	x := uint32(i)*2654435761 + 0x9E3779B9
	x ^= x >> 15
	x *= 0x85EBCA6B
	x ^= x >> 13
	return x
}

// Calibration describes how the channel mapped our amplitudes onto what came back.
//
// A video round trip does not preserve absolute levels: encoders clamp, players
// apply range conversion, and platforms adjust brightness. Black comes back as 12
// and white as 240 as often as not. Estimating the two endpoints per frame and
// rescaling is what lets the same decision boundaries work on a file that has been
// through three re-encodings.
type Calibration struct {
	Black float64
	White float64
}

// DefaultCalibration assumes an untouched channel.
func DefaultCalibration() Calibration { return Calibration{Black: 0, White: 255} }

// Normalise maps a measured sample onto [0, 1] using the calibration.
func (c Calibration) Normalise(sample float64) float64 {
	span := c.White - c.Black
	if span <= 0 {
		// A degenerate calibration means the frame carried no usable reference.
		// Falling back to the nominal range is better than dividing by zero and
		// better than refusing, because the caller still has confidences to judge
		// the result by.
		span = 255
		return clamp01(sample / span)
	}
	return clamp01((sample - c.Black) / span)
}

// Soft is one demodulated cell: the symbol it most likely carried, and how far the
// measurement sat from the nearest decision boundary.
//
// Confidence runs from 0 to 1. Zero means the sample landed exactly on a boundary,
// so the symbol is a coin flip. One means it landed exactly on a level.
type Soft struct {
	Symbol     byte
	Confidence float64
}

// DemodulateCells converts measured samples into symbols with confidences.
func (m Modem) DemodulateCells(samples []float64, cal Calibration) []Soft {
	out := make([]Soft, len(samples))
	steps := float64(m.levels - 1)

	for i, s := range samples {
		// Position on the symbol scale: 0 for the lowest level, steps for the
		// highest, with the integers landing on the levels themselves.
		pos := cal.Normalise(s) * steps
		sym := int(pos + 0.5)
		sym = max(0, min(sym, m.levels-1))

		// Distance to the nearest level, in symbol units. It cannot exceed 0.5,
		// which is exactly the boundary, so doubling it gives a 0..1 scale.
		dist := pos - float64(sym)
		if dist < 0 {
			dist = -dist
		}
		out[i] = Soft{Symbol: byte(sym), Confidence: clamp01(1 - 2*dist)}
	}
	return out
}

// Pack reassembles bytes from demodulated cells, and returns the confidence of each
// byte.
//
// A byte's confidence is the *minimum* over the cells that compose it, not the mean.
// A byte is wrong if any one of its cells is wrong, so averaging would let seven
// confident cells hide one that was a coin flip, which is precisely the byte the
// erasure marking above needs to hear about.
func (m Modem) Pack(cells []Soft) (data []byte, confidence []float64) {
	n := m.BytesForCells(len(cells))
	data = make([]byte, n)
	confidence = make([]float64, n)

	perByte := 8 / m.bitsPerCell
	for i := range n {
		var b byte
		worst := 1.0
		for j := range perByte {
			c := cells[i*perByte+j]
			b = b<<m.bitsPerCell | (c.Symbol & m.mask)
			worst = min(worst, c.Confidence)
		}
		data[i] = b
		confidence[i] = worst
	}
	return data, confidence
}

// Demodulate is DemodulateCells followed by Pack.
func (m Modem) Demodulate(samples []float64, cal Calibration) (data []byte, confidence []float64) {
	return m.Pack(m.DemodulateCells(samples, cal))
}

func clamp01(v float64) float64 {
	return max(0, min(v, 1))
}
