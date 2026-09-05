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
	cases := map[string]Profile{
		"odd level count":  {Name: "x", Width: 640, Height: 480, FPS: 30, CellSize: 8, Levels: 3},
		"zero cell size":   {Name: "x", Width: 640, Height: 480, FPS: 30, CellSize: 0, Levels: 2},
		"margins too wide": {Name: "x", Width: 64, Height: 64, FPS: 30, CellSize: 8, Levels: 2, Margin: 40},
		"zero frame rate":  {Name: "x", Width: 640, Height: 480, FPS: 0, CellSize: 8, Levels: 2},
		"absurd parity":    {Name: "x", Width: 640, Height: 480, FPS: 30, CellSize: 8, Levels: 2, Redundancy: 99},
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
		e := p.Estimate(sealed, sealed)

		if e.Frames <= 0 {
			t.Fatalf("%s: %d frames", p.Name, e.Frames)
		}
		// Frames must be enough to carry the payload, and no more than one frame
		// of slack beyond it.
		carried := e.Frames * int64(p.PayloadBytesPerFrame())
		if carried < sealed {
			t.Fatalf("%s: %d frames carry %d bytes, need %d", p.Name, e.Frames, carried, sealed)
		}
		if carried-int64(p.PayloadBytesPerFrame()) >= sealed {
			t.Fatalf("%s: %d frames is one more than needed", p.Name, e.Frames)
		}

		want := float64(e.Frames) / float64(p.FPS)
		if math.Abs(e.Duration-want) > 1e-9 {
			t.Fatalf("%s: duration %.3f, want %.3f", p.Name, e.Duration, want)
		}
	}
}

func TestEstimateHandlesEmptyInput(t *testing.T) {
	e := Archive.Estimate(0, 0)
	if e.Frames != 1 {
		t.Fatalf("an empty payload should still produce one frame, got %d", e.Frames)
	}
}
