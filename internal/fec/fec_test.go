package fec

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/bzhzion/noisecrypt/internal/modem"
)

// testLayout mirrors the shape of the social profile: a small frame, generous
// parity on both layers.
func testLayout() Layout {
	l, err := NewLayout(263, 0.25, 24, 8)
	if err != nil {
		panic(err)
	}
	return l
}

func payload(t *testing.T, n int) []byte {
	t.Helper()
	rng := rand.New(rand.NewPCG(3, 5))
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(rng.UintN(256))
	}
	return b
}

func clean(frames []Frame) []ReadFrame {
	out := make([]ReadFrame, len(frames))
	for i, f := range frames {
		out[i] = ReadFrame{Bytes: bytes.Clone(f.Bytes)}
	}
	return out
}

func TestLayoutValidation(t *testing.T) {
	good := testLayout()
	if err := good.Validate(); err != nil {
		t.Fatalf("the reference layout is invalid: %v", err)
	}

	cases := map[string]Layout{
		"no intra parity":      {FrameBytes: 263, IntraData: 14, IntraParity: 0, InterData: 24, InterParity: 8},
		"no inter parity":      {FrameBytes: 263, IntraData: 14, IntraParity: 4, InterData: 24, InterParity: 0},
		"intra past GF(256)":   {FrameBytes: 100000, IntraData: 250, IntraParity: 10, InterData: 24, InterParity: 8},
		"inter past GF(256)":   {FrameBytes: 263, IntraData: 14, IntraParity: 4, InterData: 250, InterParity: 10},
		"frame too small":      {FrameBytes: 40, IntraData: 14, IntraParity: 4, InterData: 24, InterParity: 8},
		"no room for checksum": {FrameBytes: 42, IntraData: 1, IntraParity: 1, InterData: 2, InterParity: 1},
	}
	for name, l := range cases {
		t.Run(name, func(t *testing.T) {
			if err := l.Validate(); !errors.Is(err, ErrInvalidLayout) {
				t.Fatalf("expected ErrInvalidLayout, got %v", err)
			}
		})
	}
}

func TestRoundTripClean(t *testing.T) {
	l := testLayout()

	for _, size := range []int{1, 100, l.ShardSize(), l.BlockPayload() - 1, l.BlockPayload(), l.BlockPayload()*3 + 7} {
		t.Run(fmt.Sprintf("%d bytes", size), func(t *testing.T) {
			data := payload(t, size)

			frames, err := Encode(data, l)
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			if len(frames)%l.FramesPerBlock() != 0 {
				t.Fatalf("%d frames is not a whole number of blocks of %d", len(frames), l.FramesPerBlock())
			}
			if got := l.FrameCount(size); got != len(frames) {
				t.Fatalf("FrameCount predicted %d frames, Encode produced %d", got, len(frames))
			}

			out, err := Decode(clean(frames), l)
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			if !bytes.Equal(out, data) {
				t.Fatalf("round trip mismatch: got %d bytes, want %d", len(out), size)
			}
		})
	}
}

// TestFrameLossUpToTheParityBudget checks the inter-frame layer does what it exists
// for, and that it fails cleanly one frame past its budget rather than returning
// plausible garbage.
func TestFrameLossUpToTheParityBudget(t *testing.T) {
	l := testLayout()
	data := payload(t, l.BlockPayload()*2)

	frames, err := Encode(data, l)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	t.Run("at the budget", func(t *testing.T) {
		var kept []ReadFrame
		dropped := 0
		for _, f := range clean(frames) {
			h, _ := parseHeader(f.Bytes)
			if h.block == 0 && dropped < l.InterParity {
				dropped++
				continue
			}
			kept = append(kept, f)
		}

		out, err := Decode(kept, l)
		if err != nil {
			t.Fatalf("losing exactly %d frames should be recoverable: %v", l.InterParity, err)
		}
		if !bytes.Equal(out, data) {
			t.Fatal("recovered data does not match")
		}
	})

	t.Run("one past the budget", func(t *testing.T) {
		var kept []ReadFrame
		dropped := 0
		for _, f := range clean(frames) {
			h, _ := parseHeader(f.Bytes)
			if h.block == 0 && dropped < l.InterParity+1 {
				dropped++
				continue
			}
			kept = append(kept, f)
		}

		if _, err := Decode(kept, l); !errors.Is(err, ErrTooDamaged) {
			t.Fatalf("expected ErrTooDamaged, got %v", err)
		}
	})
}

// TestFramesMayArriveInAnyOrderWithDuplicates is not a nicety. A video decoder
// returns frames in presentation order after rate conversion, which duplicates some
// and drops others, so the decoder cannot assume position means anything.
func TestFramesMayArriveInAnyOrderWithDuplicates(t *testing.T) {
	l := testLayout()
	data := payload(t, l.BlockPayload()*2)

	frames, err := Encode(data, l)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	scrambled := clean(frames)
	rng := rand.New(rand.NewPCG(9, 13))
	rng.Shuffle(len(scrambled), func(i, j int) {
		scrambled[i], scrambled[j] = scrambled[j], scrambled[i]
	})
	// Duplicate a third of them, as a 24 to 30 fps conversion would.
	for i := 0; i < len(scrambled); i += 3 {
		scrambled = append(scrambled, scrambled[i])
	}

	out, err := Decode(scrambled, l)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !bytes.Equal(out, data) {
		t.Fatal("recovered data does not match")
	}
}

// TestHeaderSurvivesTwoDestroyedCopies checks the repetition is worth its 39 bytes.
// The header cannot be protected by the intra-frame code, because reading it is what
// tells the decoder where the frame belongs.
func TestHeaderSurvivesTwoDestroyedCopies(t *testing.T) {
	l := testLayout()
	data := payload(t, l.BlockPayload())

	frames, err := Encode(data, l)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	read := clean(frames)
	for i := range read {
		// Destroy the first two copies entirely, leaving the third.
		for j := range 2 * headerSize {
			read[i].Bytes[j] = 0xFF
		}
	}

	out, err := Decode(read, l)
	if err != nil {
		t.Fatalf("Decode with two destroyed header copies: %v", err)
	}
	if !bytes.Equal(out, data) {
		t.Fatal("recovered data does not match")
	}
}

// TestHeaderMajorityVoteRebuildsFromThreeDamagedCopies covers the case a per-copy
// checksum alone cannot: every copy is damaged, but in different bytes.
func TestHeaderMajorityVoteRebuildsFromThreeDamagedCopies(t *testing.T) {
	l := testLayout()
	data := payload(t, l.BlockPayload())

	frames, err := Encode(data, l)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	read := clean(frames)
	for i := range read {
		read[i].Bytes[0*headerSize+2] ^= 0xFF
		read[i].Bytes[1*headerSize+5] ^= 0xFF
		read[i].Bytes[2*headerSize+9] ^= 0xFF
	}

	// Every individual copy now fails its checksum, so only the byte-wise vote can
	// recover this.
	for i := range headerCopies {
		if _, ok := decodeHeaderCopy(read[0].Bytes[i*headerSize : (i+1)*headerSize]); ok {
			t.Fatalf("copy %d still passes its checksum; the test is not exercising the vote", i)
		}
	}

	out, err := Decode(read, l)
	if err != nil {
		t.Fatalf("Decode with three damaged header copies: %v", err)
	}
	if !bytes.Equal(out, data) {
		t.Fatal("recovered data does not match")
	}
}

// channel pushes encoded frames through modulation, additive noise and soft
// demodulation. It is the closest thing to a real round trip that does not need
// FFmpeg, and it is what the codec's claims are measured against.
func channel(t *testing.T, frames []Frame, l Layout, m modem.Modem, sigma float64, seed uint64) []ReadFrame {
	t.Helper()
	rng := rand.New(rand.NewPCG(seed, seed*2+1))

	out := make([]ReadFrame, len(frames))
	cellCount := m.CellsForBytes(l.FrameBytes)

	for i, f := range frames {
		cells := m.Modulate(f.Bytes, cellCount)
		samples := make([]float64, cellCount)
		for j, s := range cells {
			samples[j] = float64(m.Amplitude(s)) + rng.NormFloat64()*sigma
		}
		data, conf := m.Demodulate(samples, modem.DefaultCalibration())
		out[i] = ReadFrame{Bytes: data, Confidence: conf}
	}
	return out
}

func rawByteErrorRate(t *testing.T, frames []Frame, read []ReadFrame) float64 {
	t.Helper()
	wrong, total := 0, 0
	for i := range frames {
		for j := range frames[i].Bytes {
			total++
			if read[i].Bytes[j] != frames[i].Bytes[j] {
				wrong++
			}
		}
	}
	return float64(wrong) / float64(total)
}

// TestSurvivesANoisyChannel is the end-to-end claim of the codec: bytes go in, the
// channel damages them, and the same bytes come out.
func TestSurvivesANoisyChannel(t *testing.T) {
	l := testLayout()
	m, err := modem.New(2)
	if err != nil {
		t.Fatalf("modem.New: %v", err)
	}
	data := payload(t, l.BlockPayload()*2)

	frames, err := Encode(data, l)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	for _, sigma := range []float64{30, 40, 45} {
		t.Run(fmt.Sprintf("sigma %.0f", sigma), func(t *testing.T) {
			read := channel(t, frames, l, m, sigma, 42)
			rate := rawByteErrorRate(t, frames, read)

			out, err := Decode(read, l)
			if err != nil {
				t.Fatalf("sigma %.0f corrupted %.2f%% of raw bytes and the codec gave up: %v",
					sigma, rate*100, err)
			}
			if !bytes.Equal(out, data) {
				t.Fatalf("sigma %.0f: decode succeeded but returned the wrong bytes", sigma)
			}
			t.Logf("sigma %.0f: %.2f%% of raw bytes corrupted, payload fully recovered", sigma, rate*100)
		})
	}
}

// TestConfidenceEarnsItsKeep is the measurement that justifies soft demodulation.
//
// The same damaged frames are decoded twice: once with the per-byte confidences, and
// once with them discarded, which forces the intra-frame layer to erase sub-shards
// blindly in index order. If the two performed the same, the whole soft demodulation
// path would be complexity for nothing.
func TestConfidenceEarnsItsKeep(t *testing.T) {
	l := testLayout()
	m, err := modem.New(2)
	if err != nil {
		t.Fatalf("modem.New: %v", err)
	}
	data := payload(t, l.BlockPayload())

	frames, err := Encode(data, l)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	countRepaired := func(read []ReadFrame) int {
		enc, err := reedSolomonFor(l)
		if err != nil {
			t.Fatalf("reedsolomon: %v", err)
		}
		ok := 0
		for _, f := range read {
			if _, good := recoverShard(f, l, enc); good {
				ok++
			}
		}
		return ok
	}

	for _, sigma := range []float64{45, 55, 65} {
		soft := channel(t, frames, l, m, sigma, 7)

		hard := make([]ReadFrame, len(soft))
		for i, f := range soft {
			hard[i] = ReadFrame{Bytes: f.Bytes} // confidence deliberately dropped
		}

		withConf := countRepaired(soft)
		withoutConf := countRepaired(hard)
		rate := rawByteErrorRate(t, frames, soft)

		t.Logf("sigma %.0f (%.2f%% raw byte errors): %d of %d frames repaired with confidence, %d without",
			sigma, rate*100, withConf, len(soft), withoutConf)

		if withConf < withoutConf {
			t.Fatalf("sigma %.0f: confidence made frame repair worse (%d vs %d)", sigma, withConf, withoutConf)
		}
	}
}

// TestErrorRateEnvelope measures where the codec actually stops working, and checks
// that the intra parity ratio is the knob that moves it.
//
// This exists because the alternative is a tuning constant nobody can check, which
// is the thing this project set out not to repeat. The numbers it logs are the
// honest operating envelope and belong in the documentation; the assertions only pin
// the two facts that must not silently regress.
//
// A note on reading them: the limiting factor is not the parity budget, it is that
// confidence ranks corruption without detecting it. At a four percent byte error
// rate a frame holds roughly nine damaged shards out of two hundred and twenty-four,
// and fifty-six erasures are available, so the budget is nowhere near exhausted.
// What fails is that two or three of those nine damaged shards rank outside the
// least-confident fifty-six and survive into the reconstruction. More parity helps
// only because it widens the net, not because the code was running out of room.
func TestErrorRateEnvelope(t *testing.T) {
	m, err := modem.New(2)
	if err != nil {
		t.Fatalf("modem.New: %v", err)
	}

	// breakdown returns the highest raw byte error rate this layout recovered from.
	breakdown := func(t *testing.T, ratio float64) (float64, Layout) {
		t.Helper()
		l, err := NewLayout(263, ratio, 24, 8)
		if err != nil {
			t.Fatalf("NewLayout: %v", err)
		}
		data := payload(t, l.BlockPayload())
		frames, err := Encode(data, l)
		if err != nil {
			t.Fatalf("Encode: %v", err)
		}

		best := 0.0
		for sigma := 20.0; sigma <= 75; sigma += 2.5 {
			read := channel(t, frames, l, m, sigma, 101)
			rate := rawByteErrorRate(t, frames, read)

			out, err := Decode(read, l)
			if err != nil || !bytes.Equal(out, data) {
				break
			}
			best = rate
		}
		return best, l
	}

	lean, leanLayout := breakdown(t, 0.25)
	rich, richLayout := breakdown(t, 0.50)

	t.Logf("intra parity 25%%: %d payload bytes per frame, %.0f%% overhead, survives up to %.2f%% raw byte errors",
		leanLayout.ShardSize()*leanLayout.InterData/leanLayout.FramesPerBlock(),
		leanLayout.Overhead()*100, lean*100)
	t.Logf("intra parity 50%%: %d payload bytes per frame, %.0f%% overhead, survives up to %.2f%% raw byte errors",
		richLayout.ShardSize()*richLayout.InterData/richLayout.FramesPerBlock(),
		richLayout.Overhead()*100, rich*100)

	if lean < 0.015 {
		t.Fatalf("the lean layout gave up at %.2f%% raw byte errors; it used to handle 1.5%%", lean*100)
	}
	if rich <= lean {
		t.Fatalf("doubling the intra parity did not extend the envelope (%.2f%% vs %.2f%%); "+
			"the parity ratio is not the knob this code thinks it is", rich*100, lean*100)
	}
}

// TestDecodeRejectsMixedPayloads guards against two different encodes being decoded
// together. Trusting the first total length seen would silently truncate or
// over-read, which is the kind of corruption that looks like a successful decode.
func TestDecodeRejectsMixedPayloads(t *testing.T) {
	l := testLayout()

	a := payload(t, l.BlockPayload())
	b := make([]byte, l.BlockPayload()/2)
	copy(b, a)

	fa, err := Encode(a, l)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	fb, err := Encode(b, l)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	mixed := append(clean(fa), clean(fb)...)
	out, err := Decode(mixed, l)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !bytes.Equal(out, a) {
		t.Fatal("mixing a second payload in changed the result")
	}
}

func TestDecodeWithNothingUsable(t *testing.T) {
	l := testLayout()

	if _, err := Decode(nil, l); !errors.Is(err, ErrNoFrames) {
		t.Fatalf("expected ErrNoFrames, got %v", err)
	}

	junk := []ReadFrame{{Bytes: make([]byte, l.FrameBytes)}}
	if _, err := Decode(junk, l); !errors.Is(err, ErrNoFrames) {
		t.Fatalf("expected ErrNoFrames for unreadable frames, got %v", err)
	}
}

func TestOverheadIsDerivedFromTheLayout(t *testing.T) {
	l := testLayout()

	got := l.Overhead()
	want := float64(l.FrameBytes*l.FramesPerBlock())/float64(l.ShardSize()*l.InterData) - 1
	if got != want {
		t.Fatalf("Overhead reported %.4f, layout arithmetic gives %.4f", got, want)
	}
	if got <= 0 {
		t.Fatalf("a layout with parity on both layers reported %.4f overhead", got)
	}
	t.Logf("reference layout: %d payload bytes per frame, %.0f%% overhead",
		l.ShardSize()*l.InterData/l.FramesPerBlock(), got*100)
}
