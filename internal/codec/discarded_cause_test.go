package codec

import (
	"image"
	"math/rand/v2"
	"testing"

	"github.com/bzhzion/noisecrypt/internal/fec"
	"github.com/bzhzion/noisecrypt/internal/geometry"
	"github.com/bzhzion/noisecrypt/internal/modem"
	"github.com/bzhzion/noisecrypt/internal/profile"
)

// TestWhatTheDiscardedFramesActuallyAre answers a question the new loss counter raised
// the moment it existed.
//
// A real 160 KiB payload encoded with `social` produces 1344 frames, and seven of them
// are discarded by the error-correcting layer on a channel that has lost nothing at all:
// no video, no re-encode, the frames handed straight back to the decoder. The payload
// still comes out identical, so the parity absorbs it, but seven shards of the
// inter-frame budget are spent before a platform has touched anything. `social-hd`
// discards none.
//
// The experiment is decisive rather than suggestive: decode the same frames twice, once
// letting the geometry find the data area and once handing it the exact rectangle that
// was rendered. Everything else is identical, so any difference is caused by the
// location step and nothing else.
func TestWhatTheDiscardedFramesActuallyAre(t *testing.T) {
	if testing.Short() {
		t.Skip("encodes 1344 frames")
	}

	for _, p := range []profile.Profile{profile.Social, profile.SocialHD} {
		t.Run(p.Name, func(t *testing.T) {
			c, err := New(p)
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			// Seeded rather than crypto-random: a defect that shows up once in two
			// hundred frames is worthless if it cannot be reproduced on demand.
			rng := rand.New(rand.NewPCG(20260906, 7))
			payload := make([]byte, 160<<10)
			for i := range payload {
				payload[i] = byte(rng.UintN(256))
			}

			var images []*image.Gray
			if err := c.EncodeTo(payload, func(_ int, img *image.Gray) error {
				clone := image.NewGray(img.Bounds())
				copy(clone.Pix, img.Pix)
				images = append(images, clone)
				return nil
			}); err != nil {
				t.Fatalf("EncodeTo: %v", err)
			}

			rendered := c.geometry.DataArea()

			// read samples a frame from a given area, exactly as Decoder.Add does.
			read := func(img *image.Gray, area image.Rectangle) fec.ReadFrame {
				samples, err := geometry.Sample(img, area, c.cols, c.rows)
				if err != nil {
					return fec.ReadFrame{}
				}
				black, white := geometry.Calibrate(samples)
				data, confidence := c.modem.Demodulate(samples,
					modem.Calibration{Black: black, White: white})
				modem.Whiten(data)
				return fec.ReadFrame{Bytes: data, Confidence: confidence}
			}

			var located, forced []fec.ReadFrame
			offsets := map[image.Rectangle]int{}
			for _, img := range images {
				area, err := geometry.Locate(img)
				if err != nil {
					continue
				}
				if area != rendered {
					offsets[image.Rect(
						area.Min.X-rendered.Min.X, area.Min.Y-rendered.Min.Y,
						area.Max.X-rendered.Max.X, area.Max.Y-rendered.Max.Y,
					)]++
				}
				located = append(located, read(img, area))
				forced = append(forced, read(img, rendered))
			}

			_, statsLocated, err := fec.DecodeStats(located, c.layout)
			if err != nil {
				t.Fatalf("decode with located areas: %v", err)
			}
			_, statsForced, err := fec.DecodeStats(forced, c.layout)
			if err != nil {
				t.Fatalf("decode with the rendered area: %v", err)
			}

			t.Logf("%s: %d frames", p.Name, len(images))
			t.Logf("  located automatically : %d discarded (%d unrepairable)",
				statsLocated.Lost(), statsLocated.Unrepairable)
			t.Logf("  rectangle imposed     : %d discarded (%d unrepairable)",
				statsForced.Lost(), statsForced.Unrepairable)
			for delta, n := range offsets {
				t.Logf("  %d frames located off by min(%+d,%+d) max(%+d,%+d)",
					n, delta.Min.X, delta.Min.Y, delta.Max.X, delta.Max.Y)
			}

			// The claim under test. Handing the decoder the true rectangle must not
			// leave losses behind: if it does, the location step is not the cause and
			// the diagnosis has to start again somewhere else.
			if statsForced.Lost() != 0 {
				t.Errorf("%d frames are still lost with the exact rectangle, so mislocation "+
					"is not the whole cause", statsForced.Lost())
			}

			// And the repair, through the decoder that ships rather than through this
			// test's own sampling loop. The loop above deliberately bypasses
			// Decoder.Add, so on its own it would have measured the defect and said
			// nothing at all about the fix.
			d := c.NewDecoder()
			for _, img := range images {
				d.Add(img)
			}
			got, err := d.Finish()
			if err != nil {
				t.Fatalf("Finish: %v", err)
			}
			if len(got) != len(payload) {
				t.Fatalf("recovered %d bytes, expected %d", len(got), len(payload))
			}
			for i := range got {
				if got[i] != payload[i] {
					t.Fatalf("payload differs at byte %d", i)
				}
			}
			t.Logf("  decodeur complet      : %d discarded, %d recuperees par seconde lecture",
				d.Discarded(), d.Stats.Recovered)
			if d.Discarded() != 0 {
				t.Errorf("the shipping decoder still loses %d frames on a lossless channel", d.Discarded())
			}
			if statsLocated.Lost() > 0 && d.Stats.Recovered == 0 {
				t.Error("frames were lost without the repair firing, so it is not doing the work")
			}
		})
	}
}
