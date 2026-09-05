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

// TestSocialCellSizeSurvivesPlatformScaling checks that the toughest profile lands on
// whole pixels through the reference downscale.
//
// The rationale here was refined by measurement on 2026-09-05 and the original version
// of this comment overstated it. Divisibility is not always load-bearing: real
// renditions at 608x1080 and 480x854 put cells on fractional boundaries and decoded
// perfectly, because a 16 px cell has interior pixels to average even when its edges
// are shared.
//
// What actually matters is how many pixels a cell has left. Above roughly six, a
// fractional boundary is absorbed. Below about three it is fatal, and then divisibility
// decides everything: 15 px cells fail at 320p (2.5 px per cell, straddling) and succeed
// again at 256p (exactly 2 px, aligned), which is lower resolution and a better result.
//
// So this test is about the toughest profile keeping its margin at the reference scale,
// not about divisibility being sacred.
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

// TestProfilesAreOrderedByDensity pins the relationship the three profiles exist to
// express. Each step down in toughness has to buy real payload, or it has no reason to
// be offered as a choice.
func TestProfilesAreOrderedByDensity(t *testing.T) {
	tough := Social.PayloadBytesPerFrame()
	dense := SocialHD.PayloadBytesPerFrame()
	archive := Archive.PayloadBytesPerFrame()

	if !(archive > dense && dense > tough) {
		t.Fatalf("payload per frame should rise from social (%d) to social-hd (%d) to archive (%d)",
			tough, dense, archive)
	}

	// The measured gain from halving the cell size. Below three times, the extra
	// profile is not worth the choice it forces on the user.
	if gain := float64(dense) / float64(tough); gain < 3 {
		t.Fatalf("social-hd carries only %.1fx the payload of social; not worth a separate profile", gain)
	}

	t.Logf("payload per frame: social %d B, social-hd %d B (%.1fx), archive %d B",
		tough, dense, float64(dense)/float64(tough), archive)
}

// TestSocialHDStaysAboveTheFractionalCliff guards the finding that produced this
// profile. Cells below roughly three pixels after downscaling fail when the boundaries
// land on fractional pixels, and 15 px cells sit right at the edge of that: they clear a
// 426p floor at 3.3 px per cell and would not clear anything much lower.
//
// If someone shrinks this cell size further, this test is where they should be forced to
// justify it with a new measurement.
func TestSocialHDStaysAboveTheFractionalCliff(t *testing.T) {
	const floorHeight = 426 // the lowest rendition worth retrieving data from

	pxPerCell := float64(SocialHD.CellSize) * float64(floorHeight) / float64(SocialHD.Height)
	if pxPerCell < 3 {
		t.Fatalf("at %dp a %d px cell becomes %.1f px, below the ~3 px floor measured on 2026-09-05",
			floorHeight, SocialHD.CellSize, pxPerCell)
	}
	t.Logf("at %dp a %d px cell becomes %.1f px", floorHeight, SocialHD.CellSize, pxPerCell)
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

// TestVerifiedFlagMatchesWhatWasMeasured pins the one claim a user cannot check for
// themselves. `social` has been through a real YouTube round trip; `archive` has only
// ever been re-encoded locally, and its channel is one that does not re-encode at all,
// so there is nothing to verify it against. Flipping either flag without doing the
// measurement should break this test rather than quietly change what the tool claims.
func TestVerifiedFlagMatchesWhatWasMeasured(t *testing.T) {
	if !Social.Verified {
		t.Error("social was measured against every YouTube Shorts rendition on 2026-09-05; the flag should say so")
	}
	if Archive.Verified {
		t.Error("archive has only been re-encoded locally, never through a platform; it must not claim to be verified")
	}
	if SocialHD.Verified {
		t.Error("social-hd has only been through the local rescaling simulation; it must not claim a platform round trip")
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
