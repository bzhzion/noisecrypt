package fec

import (
	"bytes"
	"testing"

	"github.com/bzhzion/noisecrypt/internal/modem"
)

// TestParityBuysErrorTolerance measures what a parity budget is actually worth, across
// the whole range rather than at the two points TestErrorRateEnvelope compares.
//
// It exists because of a result that looked like free payload and was not. A local
// re-encode sweep found that halving the social-hd parity budget cost nothing: the same
// qualities decoded, the same ones failed, and the payload grew by a third. The reason
// is that a local x264 re-encode fails off a cliff. Below the cliff the cells come back
// essentially clean and no parity is needed; above it the damage is far beyond any
// budget. Parity is invisible in that experiment because the experiment never produces
// the intermediate damage parity is for.
//
// A platform does produce it: a crop, an overlay, a rendition encoded to a bitrate
// target rather than a quality target, a frame rate conversion. So the honest way to
// price a parity cut is against raw error rate, which is what this measures.
//
// It asserts only the shape, not the numbers. Pinning the numbers would make an ordinary
// tuning change look like a regression, and the numbers are in the log for whoever is
// deciding.
func TestParityBuysErrorTolerance(t *testing.T) {
	m, err := modem.New(2)
	if err != nil {
		t.Fatalf("modem.New: %v", err)
	}

	// breakdown returns the highest raw byte error rate a layout recovered from, and
	// the payload it carries per frame, so the trade is visible in one line.
	breakdown := func(ratio float64, interParity int) (float64, int, float64) {
		l, err := NewLayout(263, ratio, 24, interParity)
		if err != nil {
			t.Fatalf("NewLayout(%v, %d): %v", ratio, interParity, err)
		}
		data := payload(t, l.BlockPayload())
		frames, err := Encode(data, l)
		if err != nil {
			t.Fatalf("Encode: %v", err)
		}

		best := 0.0
		for sigma := 20.0; sigma <= 75; sigma += 2.5 {
			read := channel(t, frames, l, m, sigma, 101)
			rate := rawByteErrorRate(t, frames, read)
			out, err := Decode(read, l)
			if err != nil || !bytes.Equal(out, data) {
				break
			}
			best = rate
		}
		perFrame := l.ShardSize() * l.InterData / l.FramesPerBlock()
		return best, perFrame, l.Overhead()
	}

	type point struct {
		ratio       float64
		interParity int
	}
	// The registered social geometry, the candidate the re-encode sweep suggested, and
	// the floor below it.
	points := []point{
		{0.25, 8}, // social and social-hd as they ship
		{0.12, 4}, // the candidate that looked free
		{0.04, 2}, // the floor
	}

	var tolerance []float64
	for _, p := range points {
		rate, perFrame, overhead := breakdown(p.ratio, p.interParity)
		tolerance = append(tolerance, rate)
		t.Logf("intra %.0f%% / inter %d: %d payload bytes per frame, %.0f%% overhead, survives up to %.2f%% raw byte errors",
			p.ratio*100, p.interParity, perFrame, overhead*100, rate*100)
	}

	// The property that makes the trade real rather than free: less parity is less
	// tolerance. If this ever stops holding, the parity ratio is not the knob this
	// package believes it is, and the profiles are tuned against a phantom.
	for i := 1; i < len(tolerance); i++ {
		if tolerance[i] >= tolerance[i-1] {
			t.Fatalf("cutting parity from %.0f%% to %.0f%% did not reduce the error envelope "+
				"(%.2f%% then %.2f%%); a parity cut would then be free, and nothing here is free",
				points[i-1].ratio*100, points[i].ratio*100, tolerance[i-1]*100, tolerance[i]*100)
		}
	}
}
