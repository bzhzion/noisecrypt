package fec

import (
	"bytes"
	"testing"
)

// Stats exists because the figure that was supposed to reveal a channel getting worse
// was quietly optimistic. Frames the geometry could not locate were counted; frames it
// located and the error-correcting layer could not repair were dropped and counted
// nowhere. A video that lost several frames could be reported as losing none.
//
// So this proves the counter non-zero when it should be. A margin indicator that cannot
// be shown to move is not an indicator.

func TestStatsCountsWhatWasDiscarded(t *testing.T) {
	l, err := NewLayout(263, 0.25, 24, 8)
	if err != nil {
		t.Fatalf("NewLayout: %v", err)
	}
	data := payload(t, l.BlockPayload())
	frames, err := Encode(data, l)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	read := func(fs []Frame) []ReadFrame {
		out := make([]ReadFrame, 0, len(fs))
		for _, f := range fs {
			c := make([]float64, len(f.Bytes))
			for i := range c {
				c[i] = 1
			}
			out = append(out, ReadFrame{Bytes: append([]byte(nil), f.Bytes...), Confidence: c})
		}
		return out
	}

	t.Run("a clean decode discards nothing", func(t *testing.T) {
		got, stats, err := DecodeStats(read(frames), l)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("clean decode failed: %v", err)
		}
		if stats.Lost() != 0 {
			t.Errorf("a clean decode reported %d lost frames: %+v", stats.Lost(), stats)
		}
		if stats.Accepted != len(frames) {
			t.Errorf("accepted %d of %d frames", stats.Accepted, len(frames))
		}
	})

	t.Run("a frame beyond repair is counted, not hidden", func(t *testing.T) {
		// Destroy the payload region of three frames while leaving their headers
		// intact, which is what a mislocated frame looks like: it parses, and then
		// nothing in it passes the CRC.
		damaged := read(frames)
		for i := range 3 {
			for j := HeaderRegion; j < len(damaged[i].Bytes); j++ {
				damaged[i].Bytes[j] ^= 0xFF
			}
		}

		got, stats, err := DecodeStats(damaged, l)
		if err != nil {
			t.Fatalf("the parity should have covered three lost frames: %v", err)
		}
		if !bytes.Equal(got, data) {
			t.Fatal("payload recovered incorrectly")
		}
		// The point of the whole change: the loss is visible.
		if stats.Unrepairable != 3 {
			t.Errorf("Unrepairable is %d, expected 3: %+v", stats.Unrepairable, stats)
		}
		if stats.Lost() != 3 {
			t.Errorf("Lost() is %d, expected 3", stats.Lost())
		}
	})

	t.Run("a frame with no readable header is counted separately", func(t *testing.T) {
		// A different event from the one above, and worth telling apart: this frame
		// was sampled from something that is not a frame of this format at all.
		damaged := read(frames)
		for j := range damaged[0].Bytes {
			damaged[0].Bytes[j] = 0x5A
		}

		if _, stats, err := DecodeStats(damaged, l); err != nil {
			t.Fatalf("DecodeStats: %v", err)
		} else if stats.Unparseable != 1 {
			t.Errorf("Unparseable is %d, expected 1: %+v", stats.Unparseable, stats)
		}
	})

	t.Run("Decode keeps its signature", func(t *testing.T) {
		// The old entry point still exists, so nothing had to be touched to gain the
		// counter.
		got, err := Decode(read(frames), l)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("Decode: %v", err)
		}
	})
}
