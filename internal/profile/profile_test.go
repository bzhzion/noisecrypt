package profile

import (
	"math"
	"testing"
)

func TestBuiltInProfilesAreValid(t *testing.T) {
	for _, p := range All() {
		if err := p.Validate(); err != nil {
			t.Errorf("%s: %v", p.Name, err)
		}
		if p.PayloadBytesPerFrame() <= 0 {
			t.Errorf("%s: no payload capacity", p.Name)
		}
	}
}

// TestSocialCellSizeSurvivesPlatformScaling guards the one piece of tuning that is
// load-bearing rather than folklore: a cell size that is not a multiple of the
// scaling denominator lands on fractional pixel boundaries after the platform
// resizes the video, which smears every cell edge into its neighbour.
func TestSocialCellSizeSurvivesPlatformScaling(t *testing.T) {
	// The reference case: 1080 wide scaled to 576, a factor of 8/15.
	const num, den = 8, 15

	if Social.CellSize%den != 0 {
		t.Fatalf("social cell size %d is not a multiple of %d, so it lands on fractional pixels after a %d/%d resize",
			Social.CellSize, den, num, den)
	}

	scaled := Social.CellSize * num / den
	if scaled*den != Social.CellSize*num {
		t.Fatalf("social cell size %d does not scale to a whole number of pixels", Social.CellSize)
	}
	if scaled < 8 {
		t.Fatalf("social cell scales down to %d pixels, too few to average reliably through deblocking", scaled)
	}
}

func TestArchiveIsDenserThanSocial(t *testing.T) {
	a := Archive.PayloadBytesPerFrame()
	s := Social.PayloadBytesPerFrame()
	if a <= s {
		t.Fatalf("archive carries %d bytes per frame, social carries %d: the dense profile is not denser", a, s)
	}
}

func TestLookup(t *testing.T) {
	for _, name := range Names() {
		if _, err := Lookup(name); err != nil {
			t.Errorf("Lookup(%q): %v", name, err)
		}
	}
	if _, err := Lookup("  ARCHIVE "); err != nil {
		t.Errorf("Lookup should be case and space insensitive: %v", err)
	}
	if _, err := Lookup("nope"); err == nil {
		t.Error("Lookup accepted an unknown profile")
	}
}

func TestValidateRejectsBrokenProfiles(t *testing.T) {
	sane := func(p Profile) Profile {
		p.IntraParityRatio, p.InterData, p.InterParity = 0.2, 8, 2
		return p
	}

	cases := map[string]Profile{
		"odd level count":  sane(Profile{Name: "x", Width: 640, Height: 480, FPS: 30, CellSize: 8, Levels: 3}),
		"zero cell size":   sane(Profile{Name: "x", Width: 640, Height: 480, FPS: 30, CellSize: 0, Levels: 2}),
		"margins too wide": sane(Profile{Name: "x", Width: 64, Height: 64, FPS: 30, CellSize: 8, Levels: 2, Margin: 40}),
		"zero frame rate":  sane(Profile{Name: "x", Width: 640, Height: 480, FPS: 0, CellSize: 8, Levels: 2}),
		"absurd parity ratio": {Name: "x", Width: 640, Height: 480, FPS: 30, CellSize: 8, Levels: 2,
			IntraParityRatio: 1.5, InterData: 8, InterParity: 2},
		"no inter parity": {Name: "x", Width: 640, Height: 480, FPS: 30, CellSize: 8, Levels: 2,
			IntraParityRatio: 0.2, InterData: 8, InterParity: 0},
		"frame too small for a header": {Name: "x", Width: 64, Height: 64, FPS: 30, CellSize: 30, Levels: 2,
			IntraParityRatio: 0.2, InterData: 8, InterParity: 2},
	}

	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			if err := p.Validate(); err == nil {
				t.Fatalf("Validate accepted a broken profile: %s", name)
			}
		})
	}
}

func TestEstimate(t *testing.T) {
	const sealed = 40 * 1024 * 1024

	for _, p := range All() {
		l, err := p.Layout()
		if err != nil {
			t.Fatalf("%s: Layout: %v", p.Name, err)
		}
		e := p.Estimate(sealed, sealed)

		if e.Frames <= 0 {
			t.Fatalf("%s: %d frames", p.Name, e.Frames)
		}
		// Frames come in whole blocks, so the check is that the blocks carry the
		// payload and that dropping one block would not.
		if e.Frames%int64(l.FramesPerBlock()) != 0 {
			t.Fatalf("%s: %d frames is not a whole number of blocks of %d",
				p.Name, e.Frames, l.FramesPerBlock())
		}
		blocks := e.Frames / int64(l.FramesPerBlock())
		if carried := blocks * int64(l.BlockPayload()); carried < sealed {
			t.Fatalf("%s: %d blocks carry %d bytes, need %d", p.Name, blocks, carried, sealed)
		}
		if blocks > 1 && (blocks-1)*int64(l.BlockPayload()) >= sealed {
			t.Fatalf("%s: %d blocks is one more than needed", p.Name, blocks)
		}

		want := float64(e.Frames) / float64(p.FPS)
		if math.Abs(e.Duration-want) > 1e-9 {
			t.Fatalf("%s: duration %.3f, want %.3f", p.Name, e.Duration, want)
		}

		t.Logf("%s: %d payload bytes per frame, %.0f%% overhead, 40 MiB becomes %d frames (%.0f s)",
			p.Name, p.PayloadBytesPerFrame(), p.Redundancy()*100, e.Frames, e.Duration)
	}
}

// TestRedundancyIsDerivedNotDeclared is the guard against the failure this package
// was restructured to prevent: an advertised overhead that no longer matches the
// layout it describes.
func TestRedundancyIsDerivedNotDeclared(t *testing.T) {
	for _, p := range All() {
		l, err := p.Layout()
		if err != nil {
			t.Fatalf("%s: Layout: %v", p.Name, err)
		}
		if got, want := p.Redundancy(), l.Overhead(); got != want {
			t.Fatalf("%s: profile reports %.4f overhead, its layout costs %.4f", p.Name, got, want)
		}
		if p.Redundancy() <= 0 {
			t.Fatalf("%s: reported %.4f overhead with parity on both layers", p.Name, p.Redundancy())
		}
	}
}

func TestEstimateHandlesEmptyInput(t *testing.T) {
	l, err := Archive.Layout()
	if err != nil {
		t.Fatalf("Layout: %v", err)
	}
	e := Archive.Estimate(0, 0)
	if e.Frames != int64(l.FramesPerBlock()) {
		t.Fatalf("an empty payload should still produce one block of %d frames, got %d",
			l.FramesPerBlock(), e.Frames)
	}
}
