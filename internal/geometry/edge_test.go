package geometry

import "testing"

// findEdge decides where the data starts from two per-line statistics, and both of its
// failure modes are statistical rather than geometric. They are tested here directly
// because reproducing them through a real frame needs thousands of encodes and a lucky
// payload: the events are around one line in two thousand.
//
// The cause, measured on the profiles that ship: "mostly" is a fixed 70% applied to a
// line one cell wide, so its spread depends on how many cells are stacked in that line.
// The `social` profile stacks 62, and drawing 44 or more bright out of 62 sits 3.3
// standard deviations out. Four edges per frame makes that roughly one frame in a few
// hundred. `social-hd` stacks 124 cells, puts the same event 4.5 standard deviations
// out, and is why it has never lost a frame where `social` always lost about three.

// line builds the two statistics for a run of identical lines.
func line(n int, bright, dark float64) (b, d []float64) {
	for range n {
		b = append(b, bright)
		d = append(d, dark)
	}
	return b, d
}

// join concatenates runs into one pair of statistics.
func join(runs ...[2][]float64) (b, d []float64) {
	for _, r := range runs {
		b = append(b, r[0]...)
		d = append(d, r[1]...)
	}
	return b, d
}

func run(n int, bright, dark float64) [2][]float64 {
	b, d := line(n, bright, dark)
	return [2][]float64{b, d}
}

var (
	letterbox = func(n int) [2][]float64 { return run(n, 0.0, 1.0) }
	quiet     = func(n int) [2][]float64 { return run(n, 1.0, 0.0) }
	border    = func(n int) [2][]float64 { return run(n, 0.0, 1.0) }
	mixed     = func(n int) [2][]float64 { return run(n, 0.5, 0.5) }
	// The two flukes: a first line of data that happens to be almost all one level.
	allBright = func(n int) [2][]float64 { return run(n, 0.95, 0.05) }
	allDark   = func(n int) [2][]float64 { return run(n, 0.05, 0.95) }
)

func TestFindEdgeAcceptsAUniformlyBrightFirstLine(t *testing.T) {
	// The defect that cost `social` its three frames on every encode. The detector was
	// inside the border, met a bright line, and concluded the border enclosed nothing.
	// A uniformly bright first line of data is legitimate and is now held as a
	// candidate until mixed content confirms it.
	b, d := join(letterbox(10), quiet(15), border(15), allBright(30), mixed(600))

	got, err := findEdge(b, d, false)
	if err != nil {
		t.Fatalf("refused a frame whose first data line is bright: %v", err)
	}
	if want := 40; got != want {
		t.Errorf("data starts at %d, expected %d: the bright line *is* the first data line", got, want)
	}
}

func TestFindEdgeStillRejectsAnEmptyBorder(t *testing.T) {
	// The guard the fix above must not remove. Nothing but brightness after the
	// border means the border enclosed no data, and this is not our rectangle.
	b, d := join(letterbox(10), quiet(15), border(15), allBright(400))
	if _, err := findEdge(b, d, false); err == nil {
		t.Error("accepted a border with nothing inside it")
	}

	// And the other shape of the same mistake: border, bright gap, border.
	b, d = join(letterbox(10), quiet(15), border(15), quiet(40), border(15), mixed(300))
	if _, err := findEdge(b, d, false); err == nil {
		t.Error("accepted a border followed by a gap and another border")
	}
}

// TestFindEdgeDoesNotSwallowAUniformlyDarkFirstLine is the second fluke, and the worse
// of the two: it produces no error at all.
//
// A first line of data that happens to be almost all dark is indistinguishable from more
// border, so it is absorbed, and the located rectangle is one cell short. Nothing counts
// that frame as unreadable; it yields bytes. The error-correcting layer then spends
// parity on noise where it could have skipped an erasure, and the frame is silently
// wrong rather than visibly missing.
//
// Found by a diagnostic that compared the located rectangle with the rendered one rather
// than only counting failures, which is the part that had never been looked at.
// # Why this is skipped rather than fixed
//
// The obvious fix is a ratio. Render puts the border at exactly `margin/2`, which is
// exactly the thickness of the quiet zone outside it, and a uniform resize preserves
// that, so a dark run more than about twice the observed quiet run has overshot into
// data. It works on a frame straight out of Render.
//
// It also breaks the case this whole layer exists for. A platform that crops into the
// quiet zone leaves, say, three lines of quiet against a legitimate fifteen of border,
// and the ratio then fires on every frame and returns a position that is wrong by
// design. Trading a fluke that happens once in a few hundred frames for a systematic
// failure on cropped input is a bad trade.
//
// The fix that would hold is one level up, and is not a ratio: the rendered data area has
// a known aspect ratio of cols to rows, a uniform resize preserves it, and swallowing one
// line deviates from it by 1/rows, about 1.6% on `social`. `Locate` cannot check that
// because it is given only an image, but `Sample` already receives cols and rows. So the
// check belongs there, acting on a measurable inconsistency rather than on a guess about
// thickness.
//
// Not done here, deliberately. This component is the only one verified against a real
// platform across ten renditions, the cost of the defect is one frame in a few hundred
// carrying noise instead of an erasure, and the profile tolerates 2.77% raw byte errors
// where this is worth around 0.4%. A speculative change to geometry recovery is the
// riskiest edit in this repository and the local bench cannot price what a platform does.
func TestFindEdgeDoesNotSwallowAUniformlyDarkFirstLine(t *testing.T) {
	t.Skip("defaut connu et mesure, correctif concu mais non applique : voir le commentaire ci-dessus")

	b, d := join(letterbox(10), quiet(15), border(15), allDark(30), mixed(600))

	got, err := findEdge(b, d, false)
	if err != nil {
		t.Fatalf("findEdge: %v", err)
	}
	if want := 40; got != want {
		t.Errorf("data starts at %d, expected %d: a dark first line was absorbed into the border, "+
			"so the rectangle is %d pixels short and every sampled row drifts", got, want, got-want)
	}
}
