package modem

import (
	"bytes"
	"math"
	"math/rand/v2"
	"testing"
)

func levels() []int { return []int{2, 4, 16} }

func TestNewRejectsUnsupportedLevels(t *testing.T) {
	for _, l := range []int{0, 1, 3, 5, 8, 17, 256} {
		if _, err := New(l); err == nil {
			t.Errorf("New(%d) was accepted", l)
		}
	}
}

func TestRoundTripCleanChannel(t *testing.T) {
	for _, l := range levels() {
		m, err := New(l)
		if err != nil {
			t.Fatalf("New(%d): %v", l, err)
		}

		data := make([]byte, 256)
		for i := range data {
			data[i] = byte(i)
		}

		cells := m.Modulate(data, m.CellsForBytes(len(data)))
		samples := make([]float64, len(cells))
		for i, s := range cells {
			samples[i] = float64(m.Amplitude(s))
		}

		got, conf := m.Demodulate(samples, DefaultCalibration())
		if !bytes.Equal(got, data) {
			t.Errorf("%d levels: round trip mismatch", l)
		}
		for i, c := range conf {
			if c < 0.999 {
				t.Errorf("%d levels: byte %d came back at confidence %.3f on a clean channel", l, i, c)
			}
		}
	}
}

// TestConfidenceCollapsesAtTheBoundary is the property the whole package exists for.
// A sample sitting exactly between two levels must report a confidence near zero,
// because that is the signal the erasure marking above depends on.
func TestConfidenceCollapsesAtTheBoundary(t *testing.T) {
	for _, l := range levels() {
		m, _ := New(l)
		step := 255.0 / float64(l-1)

		// Exactly halfway between level 0 and level 1.
		boundary := []float64{step / 2}
		soft := m.DemodulateCells(boundary, DefaultCalibration())
		if soft[0].Confidence > 0.05 {
			t.Errorf("%d levels: a sample on the decision boundary reported confidence %.3f",
				l, soft[0].Confidence)
		}

		// Exactly on a level.
		onLevel := []float64{step}
		soft = m.DemodulateCells(onLevel, DefaultCalibration())
		if soft[0].Confidence < 0.95 {
			t.Errorf("%d levels: a sample on a level reported confidence %.3f", l, soft[0].Confidence)
		}
	}
}

// TestConfidenceIsMonotonic checks that confidence falls as a sample drifts from a
// level towards a boundary, with no local bumps a caller could be misled by.
func TestConfidenceIsMonotonic(t *testing.T) {
	m, _ := New(4)
	step := 255.0 / 3.0

	prev := math.Inf(1)
	for d := 0.0; d <= step/2; d += step / 40 {
		soft := m.DemodulateCells([]float64{step + d}, DefaultCalibration())
		c := soft[0].Confidence
		if c > prev+1e-9 {
			t.Fatalf("confidence rose from %.4f to %.4f while drifting further from the level", prev, c)
		}
		prev = c
	}
}

// TestByteConfidenceIsTheWorstCell pins the choice of minimum over mean. Averaging
// would let one coin-flip cell hide behind seven certain ones, and that byte is
// exactly the one the layer above needs to hear about.
func TestByteConfidenceIsTheWorstCell(t *testing.T) {
	m, _ := New(2)

	cells := make([]Soft, 8)
	for i := range cells {
		cells[i] = Soft{Symbol: 1, Confidence: 1}
	}
	cells[5] = Soft{Symbol: 1, Confidence: 0.02}

	_, conf := m.Pack(cells)
	if len(conf) != 1 {
		t.Fatalf("expected one byte, got %d", len(conf))
	}
	if conf[0] > 0.03 {
		t.Fatalf("byte confidence %.3f, expected it to follow the worst cell (0.02)", conf[0])
	}
}

// TestCalibrationRecoversACrushedRange simulates what a real channel does: black
// comes back as 20 and white as 235. Without calibration the highest level would
// read low and, at four or more levels, decode to the wrong symbol.
func TestCalibrationRecoversACrushedRange(t *testing.T) {
	for _, l := range levels() {
		m, _ := New(l)

		data := []byte{0x00, 0xFF, 0xA5, 0x5A, 0x0F, 0xF0}
		cells := m.Modulate(data, m.CellsForBytes(len(data)))

		// The crush has to be severe enough to actually break the uncalibrated
		// decode. A gentle one does not: at four levels the boundaries sit at 42,
		// 128 and 212, so a white crushed to 235 still reads as the top level and
		// the test would pass while proving nothing. 40 to 200 pushes the top
		// level below its boundary, which is the failure calibration exists for.
		const black, white = 40.0, 200.0
		samples := make([]float64, len(cells))
		for i, s := range cells {
			samples[i] = black + float64(m.Amplitude(s))*(white-black)/255
		}

		if got, _ := m.Demodulate(samples, DefaultCalibration()); l > 2 && bytes.Equal(got, data) {
			t.Errorf("%d levels: a crushed range decoded correctly without calibration, "+
				"so this test is no longer proving anything", l)
		}

		got, conf := m.Demodulate(samples, Calibration{Black: black, White: white})
		if !bytes.Equal(got, data) {
			t.Errorf("%d levels: calibration did not recover a crushed range", l)
		}
		for _, c := range conf {
			if c < 0.99 {
				t.Errorf("%d levels: calibrated decode reported confidence %.3f", l, c)
			}
		}
	}
}

func TestDegenerateCalibrationDoesNotPanic(t *testing.T) {
	m, _ := New(2)
	for _, cal := range []Calibration{
		{Black: 0, White: 0},
		{Black: 200, White: 100},
		{Black: 255, White: 255},
	} {
		got := m.DemodulateCells([]float64{0, 128, 255}, cal)
		for _, s := range got {
			if s.Confidence < 0 || s.Confidence > 1 {
				t.Fatalf("confidence %.3f is outside [0, 1] for calibration %+v", s.Confidence, cal)
			}
		}
	}
}

// TestNoiseDegradesConfidenceBeforeCorrectness is the measurement that justifies the
// design: as noise rises, confidence must start falling before bytes start flipping.
// If confidence collapsed only once bytes were already wrong, it would be useless as
// an early warning and the erasure marking above would have nothing to work with.
func TestNoiseDegradesConfidenceBeforeCorrectness(t *testing.T) {
	m, _ := New(2)
	rng := rand.New(rand.NewPCG(1, 2))

	data := make([]byte, 4096)
	for i := range data {
		data[i] = byte(rng.UintN(256))
	}
	cells := m.Modulate(data, m.CellsForBytes(len(data)))

	measure := func(sigma float64) (wrongBytes int, meanConfidence float64) {
		samples := make([]float64, len(cells))
		for i, s := range cells {
			samples[i] = float64(m.Amplitude(s)) + rng.NormFloat64()*sigma
		}
		got, conf := m.Demodulate(samples, DefaultCalibration())

		for i := range got {
			if got[i] != data[i] {
				wrongBytes++
			}
		}
		total := 0.0
		for _, c := range conf {
			total += c
		}
		return wrongBytes, total / float64(len(conf))
	}

	// A noise level well below the decision margin: nothing should be wrong yet,
	// but confidence must already have moved off 1.
	wrong, conf := measure(20)
	if wrong != 0 {
		t.Fatalf("sigma 20 flipped %d bytes; the margin should still be intact", wrong)
	}
	if conf > 0.95 {
		t.Fatalf("sigma 20 left mean confidence at %.3f; it is not reacting to noise", conf)
	}

	// Heavier noise: confidence must have fallen further still.
	wrongHigh, confHigh := measure(60)
	if confHigh >= conf {
		t.Fatalf("mean confidence did not fall between sigma 20 (%.3f) and sigma 60 (%.3f)", conf, confHigh)
	}
	t.Logf("sigma 20: %d wrong bytes, mean confidence %.3f", wrong, conf)
	t.Logf("sigma 60: %d wrong bytes, mean confidence %.3f", wrongHigh, confHigh)
}

// TestLowConfidenceRanksCorruptionAboveChance pins what confidence can and cannot
// do, because the difference decides whether the layer above needs a checksum.
//
// It can rank: corrupted bytes must be substantially over-represented in the low
// confidence tail, otherwise erasure marking would spend parity on healthy data.
//
// It cannot certify. A cell pushed far past a decision boundary lands near the
// neighbouring level and reports high confidence while being wrong, so a good chunk
// of the corruption is invisible to it by construction. The first version of this
// test demanded that the worst decile catch eighty percent of the damage; it caught
// thirty-six. The test was wrong, not the code, and the correction is the reason the
// erasure layer above validates with a CRC instead of trusting confidence.
func TestLowConfidenceRanksCorruptionAboveChance(t *testing.T) {
	m, _ := New(4)
	rng := rand.New(rand.NewPCG(7, 11))

	data := make([]byte, 2048)
	for i := range data {
		data[i] = byte(rng.UintN(256))
	}
	cells := m.Modulate(data, m.CellsForBytes(len(data)))

	samples := make([]float64, len(cells))
	for i, s := range cells {
		samples[i] = float64(m.Amplitude(s)) + rng.NormFloat64()*22
	}
	got, conf := m.Demodulate(samples, DefaultCalibration())

	var wrong []int
	for i := range got {
		if got[i] != data[i] {
			wrong = append(wrong, i)
		}
	}
	if len(wrong) == 0 {
		t.Skip("no corruption at this noise level; nothing to locate")
	}

	// Take the worst decile by confidence and see how much of the damage it covers.
	type scored struct {
		index int
		conf  float64
	}
	all := make([]scored, len(conf))
	for i, c := range conf {
		all[i] = scored{i, c}
	}
	for i := 1; i < len(all); i++ {
		for j := i; j > 0 && all[j].conf < all[j-1].conf; j-- {
			all[j], all[j-1] = all[j-1], all[j]
		}
	}

	suspectCount := len(all) / 10
	suspect := make(map[int]bool, suspectCount)
	for _, s := range all[:suspectCount] {
		suspect[s.index] = true
	}

	caught := 0
	for _, i := range wrong {
		if suspect[i] {
			caught++
		}
	}
	// Chance would put a tenth of the corruption in a tenth of the bytes, so the
	// lift over chance is the honest figure of merit.
	rate := float64(caught) / float64(len(wrong))
	chance := float64(suspectCount) / float64(len(all))
	lift := rate / chance

	t.Logf("%d corrupted bytes, the worst %.0f%% by confidence caught %d of them (%.0f%%), "+
		"a lift of %.1fx over chance", len(wrong), chance*100, caught, rate*100, lift)

	if lift < 2.5 {
		t.Fatalf("confidence ranked corruption only %.1fx better than chance; "+
			"erasure marking would be spending parity on healthy data", lift)
	}
}
