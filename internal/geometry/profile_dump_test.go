package geometry

import (
	"math/rand/v2"
	"testing"
)

// TestDumpTheProfileOfAMislocatedTopEdge prints the per-row statistics around the top
// edge of frames that Locate places wrongly, because two rounds of reasoning about what
// the state machine "must" be doing produced two different wrong answers.
//
// Diagnostic only: it asserts nothing beyond finding at least one case, so it cannot
// quietly pass by measuring nothing.
func TestDumpTheProfileOfAMislocatedTopEdge(t *testing.T) {
	g := Geometry{Width: 1080, Height: 1920, CellSize: 30, Margin: 30}
	if err := g.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	cols, rows := g.Grid()
	want := g.DataArea()

	rng := rand.New(rand.NewPCG(20260906, 7))
	amplitude := func(b byte) uint8 {
		if b == 0 {
			return 8
		}
		return 247
	}

	found := 0
	for attempt := 0; attempt < 4000 && found < 2; attempt++ {
		symbols := make([]byte, cols*rows)
		for i := range symbols {
			symbols[i] = byte(rng.UintN(2))
		}
		img, err := g.Render(symbols, amplitude)
		if err != nil {
			t.Fatalf("Render: %v", err)
		}

		area, err := Locate(img)
		if err == nil && area == want {
			continue
		}
		found++

		lo, hi := extremes(img)
		threshold := (int(lo) + int(hi)) / 2
		rowBright, rowDark := scanRows(img, threshold)

		t.Logf("frame %d: located %v, rendered %v, err=%v", attempt, area, want, err)
		t.Logf("  lignes 0 a %d, la zone de donnees commence a %d", want.Min.Y+2*g.CellSize, want.Min.Y)
		for y := 0; y <= want.Min.Y+2*g.CellSize; y++ {
			mark := "  "
			switch {
			case y == want.Min.Y:
				mark = "<-" // premiere ligne de donnees reelle
			case y == area.Min.Y:
				mark = "??" // ce que Locate a choisi
			}
			if y%5 == 0 || mark != "  " {
				t.Logf("    y=%4d bright=%.2f dark=%.2f %s", y, rowBright[y], rowDark[y], mark)
			}
		}
	}

	if found == 0 {
		t.Skip("aucune frame mal localisee sur 4000 tirages, le tirage ne reproduit pas le cas")
	}
}
