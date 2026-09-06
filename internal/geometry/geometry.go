// Package geometry draws a frame and finds the data grid again afterwards.
//
// # Why not a homography
//
// The textbook answer to "find the code in the picture" is four fiducial markers and
// a projective transform. That is the right tool when the picture was taken with a
// camera pointed at a screen, because then the grid really is skewed.
//
// This channel is not that. A video that has been through a platform has been
// scaled, cropped and letterboxed, and all three of those are axis-aligned. Nothing
// in a re-encoding pipeline introduces rotation or perspective. Solving for eight
// degrees of freedom when the channel only uses four means eight numbers to estimate
// from noisy data instead of four, which is strictly worse: more parameters, more
// variance, more ways to converge on a plausible wrong answer.
//
// So the registration here is a rectangle: a white quiet zone with a black border
// inside it, and detection is four edge scans. If someone later wants to decode a
// video of a screen, that is a new profile with real fiducials, not a change here.
//
// # Why the border is black inside white and not the other way round
//
// Letterboxing pads a frame with black bars. A detector that looks for the outermost
// dark rectangle finds the letterbox, locks onto it, and reads the whole frame off by
// however many bars there were. Looking for a bright band first and the dark border
// inside it makes the letterbox something the detector passes through rather than
// something it mistakes for the target.
//
// # Calibration comes from the data
//
// A video round trip does not preserve levels: black comes back at 12 and white at
// 240. Rather than spend cells on a calibration strip, the black and white points are
// estimated from robust percentiles of the sampled cells themselves. The cell values
// are strongly multi-modal by construction, so the fifth and ninety-fifth percentiles
// land on the extreme levels, and it costs no frame real estate at all.
package geometry

import (
	"errors"
	"fmt"
	"image"
	"sort"
)

// Levels used for the registration marks. Full contrast, because these are what
// everything else is measured against.
const (
	quietLevel  = 255 // the outer band
	borderLevel = 0   // the rectangle enclosing the data
)

var (
	// ErrNotFound is returned when a frame carries no recognisable data area.
	ErrNotFound = errors.New("geometry: no data area found in frame")

	// ErrInvalidGeometry is returned for parameters that cannot describe a frame.
	ErrInvalidGeometry = errors.New("geometry: invalid parameters")
)

// Geometry describes how a frame is laid out.
//
// The margin is split evenly between the white quiet zone on the outside and the
// black border on the inside, so a margin of 30 gives 15 pixels of each.
type Geometry struct {
	Width, Height int
	Margin        int
	CellSize      int
}

// Validate reports a geometry that cannot be drawn or read.
func (g Geometry) Validate() error {
	if g.Width <= 0 || g.Height <= 0 {
		return fmt.Errorf("%w: frame is %dx%d", ErrInvalidGeometry, g.Width, g.Height)
	}
	if g.CellSize <= 0 {
		return fmt.Errorf("%w: cell size must be positive", ErrInvalidGeometry)
	}
	if g.Margin < 4 {
		// Below this the border is one or two pixels and does not survive being
		// scaled down, which is the one thing it exists to do.
		return fmt.Errorf("%w: margin %d is too thin to carry a border", ErrInvalidGeometry, g.Margin)
	}
	cols, rows := g.Grid()
	if cols <= 0 || rows <= 0 {
		return fmt.Errorf("%w: margins leave no room for cells in %dx%d",
			ErrInvalidGeometry, g.Width, g.Height)
	}
	return nil
}

// Grid returns the cell columns and rows that fit inside the margins.
func (g Geometry) Grid() (cols, rows int) {
	w := g.Width - 2*g.Margin
	h := g.Height - 2*g.Margin
	if w <= 0 || h <= 0 {
		return 0, 0
	}
	return w / g.CellSize, h / g.CellSize
}

// Cells returns the total number of data cells in a frame.
func (g Geometry) Cells() int {
	cols, rows := g.Grid()
	return cols * rows
}

// borderWidth is how many pixels of the margin the black rectangle occupies.
func (g Geometry) borderWidth() int { return max(2, g.Margin/2) }

// DataArea is the pixel rectangle the cells occupy in a rendered frame.
func (g Geometry) DataArea() image.Rectangle {
	cols, rows := g.Grid()
	return image.Rect(g.Margin, g.Margin, g.Margin+cols*g.CellSize, g.Margin+rows*g.CellSize)
}

// Render draws a frame. amplitude maps a cell symbol to a grey level.
func (g Geometry) Render(symbols []byte, amplitude func(byte) uint8) (*image.Gray, error) {
	if err := g.Validate(); err != nil {
		return nil, err
	}
	cols, rows := g.Grid()
	if len(symbols) < cols*rows {
		return nil, fmt.Errorf("%w: %d symbols for a %dx%d grid", ErrInvalidGeometry, len(symbols), cols, rows)
	}

	img := image.NewGray(image.Rect(0, 0, g.Width, g.Height))
	fill(img, img.Bounds(), quietLevel)

	// The black rectangle sits just outside the data area.
	bw := g.borderWidth()
	area := g.DataArea()
	outer := image.Rect(area.Min.X-bw, area.Min.Y-bw, area.Max.X+bw, area.Max.Y+bw)
	fill(img, outer, borderLevel)

	for r := range rows {
		for c := range cols {
			v := amplitude(symbols[r*cols+c])
			fill(img, image.Rect(
				area.Min.X+c*g.CellSize,
				area.Min.Y+r*g.CellSize,
				area.Min.X+(c+1)*g.CellSize,
				area.Min.Y+(r+1)*g.CellSize,
			), v)
		}
	}
	return img, nil
}

func fill(img *image.Gray, r image.Rectangle, v uint8) {
	r = r.Intersect(img.Bounds())
	for y := r.Min.Y; y < r.Max.Y; y++ {
		row := img.Pix[y*img.Stride+r.Min.X : y*img.Stride+r.Max.X]
		for i := range row {
			row[i] = v
		}
	}
}

// Locate finds the data area in a frame that has been through a channel.
//
// It returns the rectangle the cells occupy, in the coordinates of the image it was
// given, which may be a different size from the one that was rendered.
func Locate(img *image.Gray) (image.Rectangle, error) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w < 8 || h < 8 {
		return image.Rectangle{}, fmt.Errorf("%w: frame is %dx%d", ErrNotFound, w, h)
	}

	// A midpoint threshold rather than a fixed 128. The whole frame may have been
	// darkened or lifted, and the two things being told apart here are the brightest
	// and darkest regions in it, so the midpoint of the observed range is the right
	// split even when the range has moved.
	lo, hi := extremes(img)
	if hi-lo < 32 {
		return image.Rectangle{}, fmt.Errorf("%w: frame has almost no contrast (%d to %d)", ErrNotFound, lo, hi)
	}
	threshold := (int(lo) + int(hi)) / 2

	rowBright, rowDark := scanRows(img, threshold)
	colBright, colDark := scanCols(img, threshold)

	top, err := findEdge(rowBright, rowDark, false)
	if err != nil {
		return image.Rectangle{}, fmt.Errorf("%w: top edge: %v", ErrNotFound, err)
	}
	bottom, err := findEdge(rowBright, rowDark, true)
	if err != nil {
		return image.Rectangle{}, fmt.Errorf("%w: bottom edge: %v", ErrNotFound, err)
	}
	left, err := findEdge(colBright, colDark, false)
	if err != nil {
		return image.Rectangle{}, fmt.Errorf("%w: left edge: %v", ErrNotFound, err)
	}
	right, err := findEdge(colBright, colDark, true)
	if err != nil {
		return image.Rectangle{}, fmt.Errorf("%w: right edge: %v", ErrNotFound, err)
	}

	area := image.Rect(b.Min.X+left, b.Min.Y+top, b.Min.X+right+1, b.Min.Y+bottom+1)
	if area.Dx() < 8 || area.Dy() < 8 {
		return image.Rectangle{}, fmt.Errorf("%w: located area is %dx%d", ErrNotFound, area.Dx(), area.Dy())
	}
	return area, nil
}

// findEdge walks in from one end: through anything (letterbox), into the bright quiet
// zone, then through the dark border, and stops at the first line that is neither.
// That line is the first row or column of data.
func findEdge(bright, dark []float64, fromEnd bool) (int, error) {
	n := len(bright)
	idx := func(i int) int {
		if fromEnd {
			return n - 1 - i
		}
		return i
	}

	const (
		mostly       = 0.70
		stateSeeking = iota
		stateInQuiet
		stateInBorder
		stateMaybeData
	)

	state := stateSeeking
	candidate := 0

	for i := range n {
		j := idx(i)
		switch state {
		case stateSeeking:
			// Skip the letterbox, whatever it is, until the quiet zone appears.
			if bright[j] >= mostly {
				state = stateInQuiet
			}
		case stateInQuiet:
			if dark[j] >= mostly {
				state = stateInBorder
			}
		case stateInBorder:
			if dark[j] < mostly && bright[j] < mostly {
				// Mixed content: data.
				return j, nil
			}
			if bright[j] >= mostly {
				// Brightness right after the border. This used to fail here, and
				// failing here was wrong often enough to cost frames.
				//
				// The reason is statistical rather than geometric. "Mostly bright"
				// is a fixed 70% applied to a line whose content is one cell wide,
				// so its spread depends on how many cells are stacked in that line.
				// The `social` profile has 62 cells per column: drawing 44 or more
				// bright out of 62 sits 3.3 standard deviations out, about one line
				// in two thousand, four edges per frame, so roughly one frame in a
				// few hundred had a genuinely-first-data-line that read as quiet.
				// `social-hd` stacks 124 cells and the same event is 4.5 standard
				// deviations out, one in 250,000, which is exactly why it never
				// lost a frame and `social` always lost about three.
				//
				// A uniformly bright first line of data is legitimate, so it is now
				// held as a candidate rather than treated as proof of absence. The
				// guard it used to provide is kept: the candidate is only accepted
				// once mixed content actually appears.
				state = stateMaybeData
				candidate = j
			}
		case stateMaybeData:
			if dark[j] < mostly && bright[j] < mostly {
				// Data does follow, so the bright line was its first line.
				return candidate, nil
			}
			if dark[j] >= mostly {
				// Border, bright gap, border. That is not one rectangle with data
				// in it, which is the case the original guard existed for.
				return 0, errors.New("border encloses no data")
			}
		}
	}

	switch state {
	case stateSeeking:
		return 0, errors.New("no quiet zone found")
	case stateInQuiet:
		return 0, errors.New("quiet zone found but no border inside it")
	case stateMaybeData:
		// Bright all the way to the far edge: the border really did enclose nothing.
		return 0, errors.New("border encloses no data")
	default:
		return 0, errors.New("border found but it never opens onto data")
	}
}

func scanRows(img *image.Gray, threshold int) (bright, dark []float64) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	bright, dark = make([]float64, h), make([]float64, h)

	for y := range h {
		row := img.Pix[(b.Min.Y+y)*img.Stride+b.Min.X:][:w]
		nb, nd := 0, 0
		for _, v := range row {
			if int(v) > threshold {
				nb++
			} else {
				nd++
			}
		}
		bright[y] = float64(nb) / float64(w)
		dark[y] = float64(nd) / float64(w)
	}
	return bright, dark
}

func scanCols(img *image.Gray, threshold int) (bright, dark []float64) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	bright, dark = make([]float64, w), make([]float64, w)

	for x := range w {
		nb, nd := 0, 0
		for y := range h {
			if int(img.Pix[(b.Min.Y+y)*img.Stride+b.Min.X+x]) > threshold {
				nb++
			} else {
				nd++
			}
		}
		bright[x] = float64(nb) / float64(h)
		dark[x] = float64(nd) / float64(h)
	}
	return bright, dark
}

func extremes(img *image.Gray) (lo, hi uint8) {
	lo, hi = 255, 0
	for _, v := range img.Pix {
		lo = min(lo, v)
		hi = max(hi, v)
	}
	return lo, hi
}

// Sample reads one value per cell from a located data area.
//
// Only the middle of each cell is averaged, never the whole thing. A cell's edge is
// where the video codec's ringing and the resampler's interpolation put a
// neighbour's value, so including it mixes in exactly the pixels that carry someone
// else's data. The inset costs a little averaging area and buys a lot of margin.
func Sample(img *image.Gray, area image.Rectangle, cols, rows int) ([]float64, error) {
	if cols <= 0 || rows <= 0 {
		return nil, fmt.Errorf("%w: %dx%d grid", ErrInvalidGeometry, cols, rows)
	}
	if area.Dx() < cols || area.Dy() < rows {
		return nil, fmt.Errorf("%w: a %dx%d area cannot hold a %dx%d grid",
			ErrNotFound, area.Dx(), area.Dy(), cols, rows)
	}

	cw := float64(area.Dx()) / float64(cols)
	ch := float64(area.Dy()) / float64(rows)

	// Average the central half of each cell in each direction, so a quarter of its
	// area, clamped to at least one pixel for very small cells.
	insetX := max(0.0, cw*0.25)
	insetY := max(0.0, ch*0.25)

	out := make([]float64, cols*rows)
	for r := range rows {
		for c := range cols {
			x0 := float64(area.Min.X) + float64(c)*cw + insetX
			x1 := float64(area.Min.X) + float64(c+1)*cw - insetX
			y0 := float64(area.Min.Y) + float64(r)*ch + insetY
			y1 := float64(area.Min.Y) + float64(r+1)*ch - insetY
			out[r*cols+c] = meanRegion(img, x0, y0, x1, y1)
		}
	}
	return out, nil
}

func meanRegion(img *image.Gray, x0, y0, x1, y1 float64) float64 {
	b := img.Bounds()
	ix0 := max(b.Min.X, int(x0))
	iy0 := max(b.Min.Y, int(y0))
	ix1 := min(b.Max.X, max(int(x1+0.5), ix0+1))
	iy1 := min(b.Max.Y, max(int(y1+0.5), iy0+1))

	sum, n := 0, 0
	for y := iy0; y < iy1; y++ {
		row := img.Pix[y*img.Stride:]
		for x := ix0; x < ix1; x++ {
			sum += int(row[x])
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return float64(sum) / float64(n)
}

// Calibrate estimates the black and white points from the sampled cells.
//
// Robust percentiles rather than the minimum and maximum: one cell caught by a
// compression artefact would otherwise set the whole frame's scale, and on a frame
// with thousands of cells there is always one. The fifth and ninety-fifth land on
// the extreme levels because the distribution is multi-modal by construction.
func Calibrate(samples []float64) (black, white float64) {
	if len(samples) == 0 {
		return 0, 255
	}
	sorted := make([]float64, len(samples))
	copy(sorted, samples)
	sort.Float64s(sorted)

	pick := func(q float64) float64 {
		i := int(q * float64(len(sorted)-1))
		return sorted[max(0, min(i, len(sorted)-1))]
	}
	black, white = pick(0.05), pick(0.95)

	if white-black < 8 {
		// Not enough spread to trust. Fall back to the nominal range and let the
		// confidences downstream report how badly this went.
		return 0, 255
	}
	return black, white
}
