package codec

import (
	"bytes"
	"image"
	"math/rand/v2"
	"testing"

	"github.com/bzhzion/noisecrypt/internal/container"
	"github.com/bzhzion/noisecrypt/internal/crypt"
	"github.com/bzhzion/noisecrypt/internal/profile"
)

func payload(t *testing.T, n int) []byte {
	t.Helper()
	rng := rand.New(rand.NewPCG(11, 17))
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(rng.UintN(256))
	}
	return b
}

func collect(t *testing.T, c *Codec, data []byte) []*image.Gray {
	t.Helper()
	var out []*image.Gray
	if err := c.EncodeTo(data, func(_ int, img *image.Gray) error {
		out = append(out, img)
		return nil
	}); err != nil {
		t.Fatalf("EncodeTo: %v", err)
	}
	return out
}

func decode(t *testing.T, c *Codec, frames []*image.Gray) ([]byte, *Decoder) {
	t.Helper()
	d := c.NewDecoder()
	for _, f := range frames {
		d.Add(f)
	}
	out, err := d.Finish()
	if err != nil {
		t.Fatalf("Finish: %v (%d seen, %d unreadable)", err, d.Seen, d.Unreadable)
	}
	return out, d
}

func TestNewRejectsBrokenProfiles(t *testing.T) {
	p := profile.Social
	p.Levels = 3
	if _, err := New(p); err == nil {
		t.Fatal("New accepted a profile with three amplitude levels")
	}

	p = profile.Social
	p.Margin = 2
	if _, err := New(p); err == nil {
		t.Fatal("New accepted a margin too thin to carry a border")
	}
}

// TestRoundTripEveryProfile is the whole pipeline on a clean channel: payload in,
// frames out, frames in, payload out.
func TestRoundTripEveryProfile(t *testing.T) {
	for _, p := range profile.All() {
		t.Run(p.Name, func(t *testing.T) {
			c, err := New(p)
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			// One block's worth, so both layers of coding are exercised without
			// rendering hundreds of large frames.
			data := payload(t, c.Layout().BlockPayload())

			frames := collect(t, c, data)
			if want := c.FrameCount(len(data)); len(frames) != want {
				t.Fatalf("FrameCount predicted %d frames, EncodeTo produced %d", want, len(frames))
			}

			got, _ := decode(t, c, frames)
			if !bytes.Equal(got, data) {
				t.Fatalf("round trip mismatch: got %d bytes, want %d", len(got), len(data))
			}
		})
	}
}

// TestRoundTripWithFramesDropped simulates rate conversion: whole frames vanish,
// others are duplicated, and the order is no longer the order they were made in.
func TestRoundTripWithFramesDropped(t *testing.T) {
	c, err := New(profile.Social)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	data := payload(t, c.Layout().BlockPayload())
	frames := collect(t, c, data)

	l := c.Layout()
	rng := rand.New(rand.NewPCG(23, 29))

	// Drop exactly the inter-frame parity budget, then shuffle and duplicate.
	kept := make([]*image.Gray, 0, len(frames))
	for i, f := range frames {
		if i < l.InterParity {
			continue
		}
		kept = append(kept, f)
	}
	rng.Shuffle(len(kept), func(i, j int) { kept[i], kept[j] = kept[j], kept[i] })
	kept = append(kept, kept[0], kept[1], kept[2])

	got, d := decode(t, c, kept)
	if !bytes.Equal(got, data) {
		t.Fatal("dropping the parity budget lost the payload")
	}
	t.Logf("%d frames seen, %d unreadable, payload recovered", d.Seen, d.Unreadable)
}

// scaleAndBlur applies the damage a platform round trip actually does: an area
// average downscale, a box blur standing in for deblocking, a crushed level range,
// and additive noise.
func scaleAndBlur(src *image.Gray, num, den, blur int, black, white, noise float64, rng *rand.Rand) *image.Gray {
	sw := src.Bounds().Dx() * num / den
	sh := src.Bounds().Dy() * num / den
	scaled := image.NewGray(image.Rect(0, 0, sw, sh))
	for y := range sh {
		for x := range sw {
			x0, x1 := x*den/num, max(x*den/num+1, (x+1)*den/num)
			y0, y1 := y*den/num, max(y*den/num+1, (y+1)*den/num)
			sum, n := 0, 0
			for yy := y0; yy < min(y1, src.Bounds().Dy()); yy++ {
				for xx := x0; xx < min(x1, src.Bounds().Dx()); xx++ {
					sum += int(src.Pix[yy*src.Stride+xx])
					n++
				}
			}
			if n > 0 {
				scaled.Pix[y*scaled.Stride+x] = uint8(sum / n)
			}
		}
	}

	out := image.NewGray(scaled.Bounds())
	for y := range sh {
		for x := range sw {
			sum, n := 0, 0
			for dy := -blur; dy <= blur; dy++ {
				for dx := -blur; dx <= blur; dx++ {
					yy, xx := y+dy, x+dx
					if yy < 0 || yy >= sh || xx < 0 || xx >= sw {
						continue
					}
					sum += int(scaled.Pix[yy*scaled.Stride+xx])
					n++
				}
			}
			v := float64(sum) / float64(n) / 255
			v = black + v*(white-black) + rng.NormFloat64()*noise
			out.Pix[y*out.Stride+x] = uint8(max(0, min(v, 255)))
		}
	}
	return out
}

// TestSurvivesAPlatformRoundTrip is the claim the social profile exists to make.
func TestSurvivesAPlatformRoundTrip(t *testing.T) {
	c, err := New(profile.Social)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	data := payload(t, c.Layout().BlockPayload())
	frames := collect(t, c, data)

	rng := rand.New(rand.NewPCG(31, 37))
	damaged := make([]*image.Gray, len(frames))
	for i, f := range frames {
		damaged[i] = scaleAndBlur(f, 8, 15, 1, 16, 235, 6, rng)
	}

	got, d := decode(t, c, damaged)
	if !bytes.Equal(got, data) {
		t.Fatal("the payload did not survive a platform round trip")
	}
	t.Logf("1080 to 576 with blur, level crush and noise: %d frames, %d unreadable, payload recovered",
		d.Seen, d.Unreadable)
}

// TestEncryptedRoundTrip runs the real stack end to end, which is the only version
// that proves the layers agree: a file is packed, sealed, carried as frames through a
// damaged channel, and opened again.
func TestEncryptedRoundTrip(t *testing.T) {
	c, err := New(profile.Social)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	original := bytes.Repeat([]byte("the quick brown fox jumps over the lazy dog. "), 40)
	packed, err := container.Pack(container.Metadata{
		Name:        "secret.txt",
		Compression: container.CompressionGzip,
	}, original)
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}

	id, err := crypt.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	sealed, err := crypt.Seal(packed, crypt.SealOptions{Recipient: &id.Public})
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	frames := collect(t, c, sealed)

	rng := rand.New(rand.NewPCG(41, 43))
	damaged := make([]*image.Gray, 0, len(frames))
	for i, f := range frames {
		if i%17 == 0 {
			continue // drop roughly one frame in seventeen
		}
		damaged = append(damaged, scaleAndBlur(f, 8, 15, 1, 16, 235, 5, rng))
	}

	recovered, d := decode(t, c, damaged)
	if !bytes.Equal(recovered, sealed) {
		t.Fatal("the sealed container did not survive the channel")
	}

	opened, err := crypt.Open(recovered, crypt.OpenOptions{Identity: id})
	if err != nil {
		t.Fatalf("Open after the channel: %v", err)
	}
	meta, out, err := container.Unpack(opened)
	if err != nil {
		t.Fatalf("Unpack: %v", err)
	}
	if !bytes.Equal(out, original) {
		t.Fatal("the recovered file differs from the original")
	}
	if meta.Name != "secret.txt" {
		t.Fatalf("recovered name %q", meta.Name)
	}

	t.Logf("%d bytes sealed into %d frames, %d delivered, %d unreadable, file recovered intact",
		len(sealed), len(frames), d.Seen, d.Unreadable)
}

// TestUnreadableFramesAreCountedNotFatal checks that junk mixed into the stream is
// skipped. A real capture starts and ends on frames that are not ours.
func TestUnreadableFramesAreCountedNotFatal(t *testing.T) {
	c, err := New(profile.Social)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	data := payload(t, c.Layout().BlockPayload())
	frames := collect(t, c, data)

	junk := image.NewGray(image.Rect(0, 0, profile.Social.Width, profile.Social.Height))
	mixed := append([]*image.Gray{junk, junk}, frames...)
	mixed = append(mixed, junk)

	got, d := decode(t, c, mixed)
	if !bytes.Equal(got, data) {
		t.Fatal("junk frames broke the decode")
	}
	if d.Unreadable != 3 {
		t.Fatalf("expected 3 unreadable frames, counted %d", d.Unreadable)
	}
}

func TestFinishWithNothingReadable(t *testing.T) {
	c, err := New(profile.Social)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	d := c.NewDecoder()
	d.Add(image.NewGray(image.Rect(0, 0, profile.Social.Width, profile.Social.Height)))

	if _, err := d.Finish(); err == nil {
		t.Fatal("Finish succeeded with no readable frames")
	}
}
