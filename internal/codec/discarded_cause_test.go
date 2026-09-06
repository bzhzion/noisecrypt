package codec

import (
	"bytes"
	"image"
	"math/rand/v2"
	"testing"

	"github.com/bzhzion/noisecrypt/internal/fec"
	"github.com/bzhzion/noisecrypt/internal/geometry"
	"github.com/bzhzion/noisecrypt/internal/modem"
	"github.com/bzhzion/noisecrypt/internal/profile"
)

// TestWhatTheDiscardedFramesActuallyAre answers a question the loss counter raised the
// moment it existed, and then proves the repair.
//
// `social` discarded seven frames of 1344 on a channel that had lost nothing: no video,
// no re-encode, the frames handed straight back to the decoder. The payload still came
// out identical, so the parity absorbed it, but seven shards of the inter-frame budget
// were spent before a platform had touched anything.
//
// The experiment is decisive rather than suggestive: decode the same frames twice, once
// letting the geometry find the data area and once handing it the exact rectangle that
// was rendered. Everything else is identical, so any difference belongs to the location
// step and to nothing else.
func TestWhatTheDiscardedFramesActuallyAre(t *testing.T) {
	// `social` only. On `social-hd` the same event sits 4.5 standard deviations out
	// rather than 3.3, one line in 250,000 instead of one in two thousand, so no payload
	// this test could afford contains one and the subtest skipped every single time. A
	// permanent skip costs CI time and teaches nothing; the asymmetry itself is the
	// finding, and it is recorded in geometry's edge_test.go where the mechanism lives.
	for _, p := range []profile.Profile{profile.Social} {
		t.Run(p.Name, func(t *testing.T) {
			c, err := New(p)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			rendered := c.geometry.DataArea()

			// The event happens about once in two thousand lines, so the frames have to
			// contain one for any of this to mean anything.
			//
			// The first version reached that with a 160 KiB payload, 1344 frames on
			// `social`, and that plus the rest of this package blew the ten minute
			// budget of the Windows runner: local timings are not CI timings, and a
			// diagnostic that stops the build is worth nothing. So a small payload, and
			// seeds tried until one contains the event. Seeded and not random, because
			// shrinking a random payload is exactly how a test quietly stops testing.
			payload, images, offsets := findFramesWithTheDefect(t, c, rendered)
			if images == nil {
				t.Skip("aucune frame mal localisee sur les graines essayees, le cas ne se reproduit pas ici")
			}

			var located, forced []fec.ReadFrame
			for _, img := range images {
				area, err := geometry.Locate(img)
				if err != nil {
					continue
				}
				located = append(located, sample(c, img, area))
				forced = append(forced, sample(c, img, rendered))
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
			t.Logf("  localise automatiquement : %d perdues (%d irreparables)",
				statsLocated.Lost(), statsLocated.Unrepairable)
			t.Logf("  rectangle impose         : %d perdues (%d irreparables)",
				statsForced.Lost(), statsForced.Unrepairable)
			for delta, n := range offsets {
				t.Logf("  %d frames decalees de min(%+d,%+d) max(%+d,%+d)",
					n, delta.Min.X, delta.Min.Y, delta.Max.X, delta.Max.Y)
			}

			// The claim under test. Handing the decoder the true rectangle must leave no
			// losses behind: if it does, location is not the cause and the diagnosis has
			// to start again somewhere else.
			if statsForced.Lost() != 0 {
				t.Errorf("%d frames restent perdues avec le rectangle exact, la mauvaise "+
					"localisation n'est donc pas toute la cause", statsForced.Lost())
			}

			// And the repair, through the decoder that ships rather than through this
			// test's own sampling. The loop above bypasses Decoder.Add on purpose, so on
			// its own it would measure the defect and say nothing about the fix.
			d := c.NewDecoder()
			for _, img := range images {
				d.Add(img)
			}
			got, err := d.Finish()
			if err != nil {
				t.Fatalf("Finish: %v", err)
			}
			if !bytes.Equal(got, payload) {
				t.Fatal("le decodeur complet ne rend pas la charge utile a l'identique")
			}
			t.Logf("  decodeur complet         : %d perdues, %d recuperees par seconde lecture",
				d.Discarded(), d.Stats.Recovered)

			if d.Discarded() != 0 {
				t.Errorf("le decodeur livre perd encore %d frames sur un canal sans perte", d.Discarded())
			}
			if statsLocated.Lost() > 0 && d.Stats.Recovered == 0 {
				t.Error("des frames etaient perdues sans que la reparation se declenche, elle ne fait donc pas le travail")
			}
		})
	}
}

// findFramesWithTheDefect returns the first seeded payload whose frames contain at least
// one mislocation, with those frames and a census of the offsets.
func findFramesWithTheDefect(t *testing.T, c *Codec, rendered image.Rectangle) ([]byte, []*image.Gray, map[image.Rectangle]int) {
	t.Helper()

	for seed := uint64(1); seed <= 12; seed++ {
		rng := rand.New(rand.NewPCG(20260906, seed))
		payload := make([]byte, 48<<10)
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

		offsets := map[image.Rectangle]int{}
		for _, img := range images {
			area, err := geometry.Locate(img)
			if err != nil {
				// Unlocatable is a different defect and not the one under test here.
				continue
			}
			if area != rendered {
				// image.Rect canonicalises its arguments, which silently reorders a
				// delta and reports the wrong sign. Built field by field instead.
				offsets[image.Rectangle{
					Min: image.Pt(area.Min.X-rendered.Min.X, area.Min.Y-rendered.Min.Y),
					Max: image.Pt(area.Max.X-rendered.Max.X, area.Max.Y-rendered.Max.Y),
				}]++
			}
		}
		if len(offsets) > 0 {
			t.Logf("graine %d contient le cas", seed)
			return payload, images, offsets
		}
	}
	return nil, nil, nil
}

// sample reads one rectangle exactly as Decoder.Add does.
func sample(c *Codec, img *image.Gray, area image.Rectangle) fec.ReadFrame {
	samples, err := geometry.Sample(img, area, c.cols, c.rows)
	if err != nil {
		return fec.ReadFrame{}
	}
	black, white := geometry.Calibrate(samples)
	data, confidence := c.modem.Demodulate(samples, modem.Calibration{Black: black, White: white})
	modem.Whiten(data)
	return fec.ReadFrame{Bytes: data, Confidence: confidence}
}
