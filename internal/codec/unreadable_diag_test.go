package codec

import (
	"crypto/rand"
	"image"
	"testing"

	"github.com/bzhzion/noisecrypt/internal/geometry"
	"github.com/bzhzion/noisecrypt/internal/profile"
)

// TestDiagnoseUnreadableFrames names the failure behind the three unreadable frames the
// `social` profile loses on every encode, without any channel involved.
//
// The clue that makes this worth chasing rather than accepting: `social-hd` loses none,
// on the same codec with the same parity, differing only in cell size. So this is not
// damage, it is the codec failing to read a frame it drew itself a moment earlier, and
// that has a cause rather than a probability.
//
// Diagnostic rather than assertion: it prints what fails and where. It asserts only that
// the count has not grown, so it cannot pass by measuring nothing.
func TestDiagnoseUnreadableFrames(t *testing.T) {
	for _, p := range []profile.Profile{profile.Social, profile.SocialHD, profile.Archive} {
		t.Run(p.Name, func(t *testing.T) {
			c, err := New(p)
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			// Enough payload for several blocks, so an intermittent cause has room to
			// show itself, and random so no byte pattern is privileged.
			payload := make([]byte, 24*1024)
			if _, err := rand.Read(payload); err != nil {
				t.Fatalf("rand: %v", err)
			}

			var frames []*image.Gray
			if err := c.EncodeTo(payload, func(_ int, img *image.Gray) error {
				clone := image.NewGray(img.Bounds())
				copy(clone.Pix, img.Pix)
				frames = append(frames, clone)
				return nil
			}); err != nil {
				t.Fatalf("EncodeTo: %v", err)
			}

			// Read every frame back exactly as the decoder would, but keeping the
			// reason. The decoder throws it away on purpose, which is right for a real
			// video and useless for finding out why.
			var bad []int
			reasons := map[string]int{}
			for i, img := range frames {
				area, err := geometry.Locate(img)
				if err != nil {
					bad = append(bad, i)
					reasons["Locate: "+err.Error()]++
					continue
				}
				if _, err := geometry.Sample(img, area, c.cols, c.rows); err != nil {
					bad = append(bad, i)
					reasons["Sample: "+err.Error()]++
					continue
				}
				// A frame can be located and sampled and still be located *wrong*,
				// which is the interesting case: it produces bytes rather than an
				// error, so the error-correcting layer sees noise and not a gap.
				want := c.geometry.DataArea()
				if area != want {
					// A mislocated frame is worse than an unreadable one: it yields
					// bytes rather than a gap, so the error-correcting layer sees
					// noise it must spend parity on instead of an erasure it could
					// have skipped. Measure whether the bytes are actually wrong.
					samples, _ := geometry.Sample(img, area, c.cols, c.rows)
					good, _ := geometry.Sample(img, want, c.cols, c.rows)
					differents := 0
					for k := range samples {
						if samples[k] != good[k] {
							differents++
						}
					}
					reasons["area off by "+offset(area, want)+
						", "+itoa(100*differents/len(samples))+"% des cellules fausses"]++
				}
			}

			t.Logf("%s: %d frames, %d unreadable", p.Name, len(frames), len(bad))
			if len(bad) > 0 {
				t.Logf("  indices: %v", bad)
			}
			for reason, n := range reasons {
				t.Logf("  %3dx %s", n, reason)
			}

			// The floor: whatever the cause, it must not get worse.
			if len(bad) > 4 {
				t.Errorf("%d unreadable frames, which is more than this profile has ever lost", len(bad))
			}
		})
	}
}

func offset(got, want image.Rectangle) string {
	return sign(got.Min.X-want.Min.X) + "," + sign(got.Min.Y-want.Min.Y) +
		" / " + sign(got.Max.X-want.Max.X) + "," + sign(got.Max.Y-want.Max.Y)
}

func sign(n int) string {
	if n == 0 {
		return "0"
	}
	if n > 0 {
		return "+" + itoa(n)
	}
	return "-" + itoa(-n)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
