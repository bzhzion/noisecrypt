package geometry

import (
	"image"
	"math/rand/v2"
	"testing"
)

func socialGeometry() Geometry {
	return Geometry{Width: 1080, Height: 1920, Margin: 30, CellSize: 30}
}

func archiveGeometry() Geometry {
	return Geometry{Width: 1920, Height: 1080, Margin: 16, CellSize: 4}
}

// binary maps a symbol to full contrast, the way a two-level modem does.
func binary(s byte) uint8 {
	if s&1 == 1 {
		return 255
	}
	return 0
}

func randomSymbols(n int, seed uint64) []byte {
	rng := rand.New(rand.NewPCG(seed, seed+1))
	out := make([]byte, n)
	for i := range out {
		out[i] = byte(rng.UintN(2))
	}
	return out
}

func TestValidate(t *testing.T) {
	for _, g := range []Geometry{socialGeometry(), archiveGeometry()} {
		if err := g.Validate(); err != nil {
			t.Errorf("%dx%d: %v", g.Width, g.Height, err)
		}
	}

	bad := map[string]Geometry{
		"zero size":         {Width: 0, Height: 0, Margin: 10, CellSize: 4},
		"zero cell":         {Width: 100, Height: 100, Margin: 10, CellSize: 0},
		"margin too thin":   {Width: 100, Height: 100, Margin: 2, CellSize: 4},
		"no room for cells": {Width: 40, Height: 40, Margin: 18, CellSize: 30},
	}
	for name, g := range bad {
		t.Run(name, func(t *testing.T) {
			if err := g.Validate(); err == nil {
				t.Fatalf("Validate accepted %s", name)
			}
		})
	}
}

// TestRenderAndReadCleanFrame is the baseline: with no channel at all, every cell
// must come back exactly.
func TestRenderAndReadCleanFrame(t *testing.T) {
	for _, g := range []Geometry{socialGeometry(), archiveGeometry()} {
		cols, rows := g.Grid()
		symbols := randomSymbols(cols*rows, 1)

		img, err := g.Render(symbols, binary)
		if err != nil {
			t.Fatalf("Render: %v", err)
		}

		area, err := Locate(img)
		if err != nil {
			t.Fatalf("Locate: %v", err)
		}
		if area != g.DataArea() {
			t.Fatalf("Locate returned %v, the data area is %v", area, g.DataArea())
		}

		samples, err := Sample(img, area, cols, rows)
		if err != nil {
			t.Fatalf("Sample: %v", err)
		}
		black, white := Calibrate(samples)

		wrong := 0
		for i, s := range samples {
			got := byte(0)
			if s > (black+white)/2 {
				got = 1
			}
			if got != symbols[i] {
				wrong++
			}
		}
		if wrong != 0 {
			t.Fatalf("%dx%d: %d of %d cells wrong on a clean frame", g.Width, g.Height, wrong, len(symbols))
		}
	}
}

// channelOptions describes what a hostile transport does to a frame. Each field is
// something a real platform actually does, not a synthetic stress.
type channelOptions struct {
	scaleNum, scaleDen int     // downscale, e.g. 8/15 for 1080 to 576
	letterboxTop       int     // black bars added after scaling
	letterboxLeft      int     //
	blurRadius         int     // box blur, standing in for deblocking and resampling
	black, white       float64 // level crush
	noise              float64 // additive gaussian
	seed               uint64
}

func defaultChannel() channelOptions {
	return channelOptions{scaleNum: 1, scaleDen: 1, black: 0, white: 255, seed: 5}
}

func degrade(src *image.Gray, o channelOptions) *image.Gray {
	rng := rand.New(rand.NewPCG(o.seed, o.seed*3+1))

	// Area-average downscale, which is what a sane resampler does.
	sw := src.Bounds().Dx() * o.scaleNum / o.scaleDen
	sh := src.Bounds().Dy() * o.scaleNum / o.scaleDen
	scaled := image.NewGray(image.Rect(0, 0, sw, sh))
	for y := range sh {
		for x := range sw {
			x0 := x * o.scaleDen / o.scaleNum
			x1 := max(x0+1, (x+1)*o.scaleDen/o.scaleNum)
			y0 := y * o.scaleDen / o.scaleNum
			y1 := max(y0+1, (y+1)*o.scaleDen/o.scaleNum)
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

	// Box blur, standing in for deblocking and interpolation.
	blurred := scaled
	if o.blurRadius > 0 {
		blurred = image.NewGray(scaled.Bounds())
		r := o.blurRadius
		for y := range sh {
			for x := range sw {
				sum, n := 0, 0
				for dy := -r; dy <= r; dy++ {
					for dx := -r; dx <= r; dx++ {
						yy, xx := y+dy, x+dx
						if yy < 0 || yy >= sh || xx < 0 || xx >= sw {
							continue
						}
						sum += int(scaled.Pix[yy*scaled.Stride+xx])
						n++
					}
				}
				blurred.Pix[y*blurred.Stride+x] = uint8(sum / n)
			}
		}
	}

	// Letterbox, then crush the levels and add noise.
	out := image.NewGray(image.Rect(0, 0, sw+2*o.letterboxLeft, sh+2*o.letterboxTop))
	for y := range out.Bounds().Dy() {
		for x := range out.Bounds().Dx() {
			sx, sy := x-o.letterboxLeft, y-o.letterboxTop
			if sx < 0 || sy < 0 || sx >= sw || sy >= sh {
				out.Pix[y*out.Stride+x] = 0 // letterbox bars are black
				continue
			}
			v := float64(blurred.Pix[sy*blurred.Stride+sx]) / 255
			v = o.black + v*(o.white-o.black) + rng.NormFloat64()*o.noise
			out.Pix[y*out.Stride+x] = uint8(max(0, min(v, 255)))
		}
	}
	return out
}

func readBack(t *testing.T, g Geometry, symbols []byte, o channelOptions) (wrong int, total int) {
	t.Helper()
	cols, rows := g.Grid()

	img, err := g.Render(symbols, binary)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	got := degrade(img, o)

	area, err := Locate(got)
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	samples, err := Sample(got, area, cols, rows)
	if err != nil {
		t.Fatalf("Sample: %v", err)
	}
	black, white := Calibrate(samples)

	for i, s := range samples {
		bit := byte(0)
		if s > (black+white)/2 {
			bit = 1
		}
		if bit != symbols[i] {
			wrong++
		}
	}
	return wrong, len(symbols)
}

// TestSurvivesAPlatformResize is the reason the social cell size is 30. A platform
// scaling 1080 to 576 applies 8/15; 30 x 8/15 is 16 pixels exactly, so every cell
// still lands on whole pixels.
func TestSurvivesAPlatformResize(t *testing.T) {
	g := socialGeometry()
	cols, rows := g.Grid()
	symbols := randomSymbols(cols*rows, 2)

	o := defaultChannel()
	o.scaleNum, o.scaleDen = 8, 15
	o.blurRadius = 1
	o.black, o.white = 16, 235
	o.noise = 4

	wrong, total := readBack(t, g, symbols, o)
	t.Logf("1080 to 576, blurred, crushed to 16..235, noise 4: %d of %d cells wrong", wrong, total)
	if wrong != 0 {
		t.Fatalf("a resize the cell size was chosen for lost %d of %d cells", wrong, total)
	}
}

// TestSurvivesLetterboxing checks the detector walks through the black bars instead
// of locking onto them. A detector looking for the outermost dark rectangle finds
// the letterbox and reads the entire frame off by however many bars there were.
func TestSurvivesLetterboxing(t *testing.T) {
	g := socialGeometry()
	cols, rows := g.Grid()
	symbols := randomSymbols(cols*rows, 3)

	o := defaultChannel()
	o.scaleNum, o.scaleDen = 8, 15
	o.letterboxTop, o.letterboxLeft = 40, 24
	o.blurRadius = 1
	o.black, o.white = 12, 240

	wrong, total := readBack(t, g, symbols, o)
	t.Logf("with 40 and 24 pixel letterbox bars: %d of %d cells wrong", wrong, total)
	if wrong != 0 {
		t.Fatalf("letterboxing lost %d of %d cells", wrong, total)
	}
}

// TestSurvivesAnAwkwardResize uses a scaling factor the cell size was *not* chosen
// for, so cells land on fractional pixel boundaries. Some loss is expected here; the
// point is that it degrades rather than collapsing, because the error correction
// above is what absorbs the rest.
func TestSurvivesAnAwkwardResize(t *testing.T) {
	g := socialGeometry()
	cols, rows := g.Grid()
	symbols := randomSymbols(cols*rows, 4)

	o := defaultChannel()
	o.scaleNum, o.scaleDen = 7, 13 // 30 x 7/13 is 16.15 pixels, deliberately not whole
	o.blurRadius = 1
	o.black, o.white = 16, 235
	o.noise = 4

	wrong, total := readBack(t, g, symbols, o)
	rate := float64(wrong) / float64(total)
	t.Logf("an awkward 7/13 resize: %d of %d cells wrong (%.2f%%)", wrong, total, rate*100)

	if rate > 0.02 {
		t.Fatalf("an unplanned resize cost %.2f%% of cells, past what the codec can absorb", rate*100)
	}
}

// TestCalibrationHandlesASevereCrush pushes the level range far enough that a fixed
// threshold at 128 would fail outright.
func TestCalibrationHandlesASevereCrush(t *testing.T) {
	g := socialGeometry()
	cols, rows := g.Grid()
	symbols := randomSymbols(cols*rows, 5)

	o := defaultChannel()
	o.black, o.white = 90, 170 // everything squeezed into the middle third
	o.blurRadius = 1

	wrong, total := readBack(t, g, symbols, o)
	t.Logf("levels crushed to 90..170: %d of %d cells wrong", wrong, total)
	if wrong != 0 {
		t.Fatalf("a severe level crush lost %d of %d cells", wrong, total)
	}
}

func TestLocateRejectsUnusableFrames(t *testing.T) {
	cases := map[string]*image.Gray{
		"too small": image.NewGray(image.Rect(0, 0, 4, 4)),
		"flat":      image.NewGray(image.Rect(0, 0, 200, 200)),
	}

	noise := image.NewGray(image.Rect(0, 0, 200, 200))
	rng := rand.New(rand.NewPCG(1, 1))
	for i := range noise.Pix {
		noise.Pix[i] = uint8(rng.UintN(256))
	}
	cases["pure noise"] = noise

	for name, img := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Locate(img); err == nil {
				t.Fatalf("Locate accepted %s", name)
			}
		})
	}
}

func TestCalibrateIsRobustToOutliers(t *testing.T) {
	// A bimodal set with a handful of wild samples, which is what a compression
	// artefact looks like. Taking the extremes would set the scale from the noise.
	samples := make([]float64, 0, 1000)
	for range 500 {
		samples = append(samples, 30)
	}
	for range 500 {
		samples = append(samples, 210)
	}
	samples[0], samples[1] = 0, 255

	black, white := Calibrate(samples)
	if black < 25 || black > 35 {
		t.Errorf("black point %.1f, expected it near 30", black)
	}
	if white < 205 || white > 215 {
		t.Errorf("white point %.1f, expected it near 210", white)
	}
}

func TestCalibrateFallsBackWhenThereIsNoSpread(t *testing.T) {
	flat := make([]float64, 100)
	for i := range flat {
		flat[i] = 128
	}
	black, white := Calibrate(flat)
	if black != 0 || white != 255 {
		t.Fatalf("a flat frame calibrated to %.0f..%.0f, expected the nominal range", black, white)
	}
}
