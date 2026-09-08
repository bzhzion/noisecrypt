// Command icongen draws the NoiseCrypt marks and packs them into Windows .ico files.
//
// The logo is not a picture of the product, it is the product: a grid of macro cells,
// the same thing the codec draws when it turns a file into video. Drawing it in code
// rather than in an editor keeps that literal, and means the marks can be regenerated at
// any size without an asset pipeline, a design tool or a node_modules.
//
// ICO is a simple container: a header, one directory entry per size, then the payloads.
// Since Windows Vista those payloads may be PNG, which `image/png` in the standard
// library already writes, so this needs no dependency at all.
//
//	go run ./tools/icongen
package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// The palette is the interface's, so the marks and the page look like the same product.
var (
	ink    = color.NRGBA{0x0a, 0x0b, 0x0d, 0xff}
	paper  = color.NRGBA{0xe8, 0xe6, 0xe1, 0xff}
	signal = color.NRGBA{0xff, 0xb0, 0x20, 0xff}
	// The underside of the folded corner. A mid tone, because it has to separate from
	// the page on one side and from whatever is behind the cut on the other.
	fold = color.NRGBA{0x4d, 0x4a, 0x45, 0xff}
	none = color.NRGBA{0, 0, 0, 0}
)

// Sizes Windows actually asks for. 16 is the one that decides whether a mark works: a
// design that only reads at 256 is a design nobody sees, because the places an icon
// matters most are the taskbar and a context menu.
var sizes = []int{16, 24, 32, 48, 64, 128, 256}

// Every mark is drawn once, at this resolution, and every smaller size is an average of
// it. The alternative, re-running the drawing code at each size, is what this file used to
// do and it does not hold together: the margins and the cell size are integer fractions of
// the canvas, they round differently at each size, and the 32-pixel version came out with
// nine columns where the 48-pixel version had seven. Two icons of the same thing that are
// not the same drawing.
//
// 768 is not arbitrary. Every size above divides it exactly (48, 32, 24, 16, 12, 6, 3), so
// each reduction is a whole-number box average with no resampling and no dependency. A
// size that did not divide it would be silently wrong, so that is checked rather than
// trusted.
const master = 768

// Proportions, in master units, so they scale with the reduction instead of being decided
// per size. keyline is the value that matters: at 768 it is a thin border, and it survives
// the reduction to 48 as roughly one pixel. Chosen from that end, because a near-white
// page with no border is invisible in a light file manager, which is the default one.
const (
	keyline = master / 48
	columns = 6
)

// The two marks this product needs, and they must not be the same drawing. The
// executable is a program you launch; a .ncry is a document you were handed. Shipping one
// icon for both tells the user those are the same kind of thing, and the consequence is
// not cosmetic: the container icon is what appears in a download folder, next to files
// from every other application, and it has to say "this is a sealed file" before anything
// else.
var marks = map[string]func(*image.NRGBA){
	// The application. A filled tile, because a program is an object you press.
	"noisecrypt": drawTile,
	// The container. A page silhouette, because that is the shape every operating
	// system has used for "a file" for forty years and there is nothing to gain by
	// being clever about it. What goes on the page is this product's own noise.
	"container": drawSheet,
}

func main() {
	dir := flag.String("dir", "assets", "the directory to write the .ico files into")
	pngDir := flag.String("png-dir", "", "also write each size as a PNG here, for review")
	svg := flag.String("svg", filepath.Join("web", "favicon.svg"),
		"also write the application tile as SVG here, for the website (empty to skip)")
	flag.Parse()

	for _, size := range sizes {
		if master%size != 0 {
			fail(fmt.Errorf("%d does not divide the master resolution %d, so it "+
				"cannot be reduced exactly; pick another size or another master",
				size, master))
		}
	}

	if err := os.MkdirAll(*dir, 0o755); err != nil {
		fail(err)
	}
	if *pngDir != "" {
		if err := os.MkdirAll(*pngDir, 0o755); err != nil {
			fail(err)
		}
	}

	// Sorted, so the output is the same order every run and a diff of two runs means
	// something changed rather than that a map iterated differently.
	for _, name := range slices.Sorted(maps.Keys(marks)) {
		full := image.NewNRGBA(image.Rect(0, 0, master, master))
		marks[name](full)

		var images []*image.NRGBA
		for _, size := range sizes {
			images = append(images, reduce(full, master/size))
		}

		if *pngDir != "" {
			for i, img := range images {
				writePNG(filepath.Join(*pngDir,
					fmt.Sprintf("%s-%d.png", name, sizes[i])), img)
			}
			writePNG(filepath.Join(*pngDir, name+"-master.png"), full)
		}

		out := filepath.Join(*dir, name+".ico")
		blob, err := encodeICO(images)
		if err != nil {
			fail(err)
		}
		if err := os.WriteFile(out, blob, 0o644); err != nil {
			fail(err)
		}
		fmt.Printf("%s: %d sizes from one %dpx drawing, %d bytes\n",
			out, len(images), master, len(blob))
	}

	if *svg != "" {
		writeTileSVG(*svg)
	}
}

// reduce box-averages by a whole factor.
//
// The averaging is done on premultiplied values. Straight NRGBA averaging looks correct
// and is not: a transparent pixel still carries a colour, and on this drawing that colour
// is black, so the page would pick up a dark fringe everywhere the folded corner is cut
// away. The fringe is invisible at 256 and obvious at 24.
func reduce(src *image.NRGBA, factor int) *image.NRGBA {
	if factor == 1 {
		return src
	}
	size := src.Bounds().Dx() / factor
	dst := image.NewNRGBA(image.Rect(0, 0, size, size))
	area := uint32(factor * factor)

	for y := range size {
		for x := range size {
			var r, g, b, a uint32
			for dy := range factor {
				for dx := range factor {
					c := src.NRGBAAt(x*factor+dx, y*factor+dy)
					al := uint32(c.A)
					r += uint32(c.R) * al
					g += uint32(c.G) * al
					b += uint32(c.B) * al
					a += al
				}
			}
			if a == 0 {
				continue // fully transparent, and dividing by it would panic
			}
			dst.SetNRGBA(x, y, color.NRGBA{
				R: uint8(r / a), G: uint8(g / a), B: uint8(b / a),
				A: uint8(a / area),
			})
		}
	}
	return dst
}

// tileCells is the side of the tile's macro cell grid.
const tileCells = 5

// tileShape is the N, and it is shared with the SVG writer rather than repeated there.
//
// Le site vitrine affichait un motif dessine a la main dans son HTML, cense evoquer des
// cellules, alors que l'icone de l'application est un N reconnaissable. Deux dessins pour
// une seule marque, dont un qui ne representait rien : painteau l'a vu immediatement. Le
// SVG du site sort donc de CETTE matrice, et une refonte de l'icone emmene le logo du site
// avec elle au lieu de le laisser derriere.
var tileShape = [tileCells][tileCells]int{
	{1, 0, 0, 0, 1},
	{1, 1, 0, 0, 1},
	{1, 0, 1, 0, 1},
	{1, 0, 0, 1, 1},
	{1, 0, 0, 0, 1},
}

// tileGeometry rend la disposition de la tuile, en unites du dessin maitre.
func tileGeometry() (offset, cell, gap int) {
	margin := master / 6
	span := master - 2*margin
	cell = span / tileCells
	offset = margin + (span-cell*tileCells)/2
	gap = cell / 10
	return offset, cell, gap
}

// tileColour rend la couleur d'une cellule : la diagonale porte le signal.
func tileColour(row, col int) color.NRGBA {
	if row == col {
		return signal
	}
	return paper
}

// drawTile is the application icon: an N built out of macro cells on a filled ground.
func drawTile(img *image.NRGBA) {
	fill(img, img.Bounds(), ink)
	offset, cell, gap := tileGeometry()

	for row := range tileCells {
		for col := range tileCells {
			if tileShape[row][col] == 0 {
				continue
			}
			fill(img, image.Rect(
				offset+col*cell, offset+row*cell,
				offset+(col+1)*cell-gap, offset+(row+1)*cell-gap,
			), tileColour(row, col))
		}
	}
}

// writeTileSVG ecrit la meme tuile en SVG, pour le site vitrine.
//
// Un SVG et pas un PNG extrait du .ico : le logo doit rester net a n'importe quelle taille
// dans une page, et les coordonnees sont deja entieres dans le dessin maitre, donc la
// conversion est exacte plutot qu'approchee.
func writeTileSVG(path string) {
	offset, cell, gap := tileGeometry()

	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" role="img" aria-label="NoiseCrypt">`+"\n", master, master)
	b.WriteString("  <!-- Genere par tools/icongen, ne pas modifier a la main : ce fichier\n")
	b.WriteString("       et assets/noisecrypt.ico sortent du meme dessin, et les editer\n")
	b.WriteString("       separement est exactement la divergence qu'on evite ici. -->\n")
	fmt.Fprintf(&b, `  <rect width="%d" height="%d" fill="%s"/>`+"\n", master, master, hexOf(ink))
	for row := range tileCells {
		for col := range tileCells {
			if tileShape[row][col] == 0 {
				continue
			}
			fmt.Fprintf(&b, `  <rect x="%d" y="%d" width="%d" height="%d" fill="%s"/>`+"\n",
				offset+col*cell, offset+row*cell, cell-gap, cell-gap, hexOf(tileColour(row, col)))
		}
	}
	b.WriteString("</svg>\n")

	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		fail(err)
	}
	fmt.Printf("%s: la meme tuile en SVG, depuis la meme matrice\n", path)
}

func hexOf(c color.NRGBA) string {
	return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B)
}

// drawSheet is the document icon: a folded page speckled with macro cells.
//
// The page is transparent outside its silhouette, unlike the application tile, because a
// document icon sits on whatever background the file manager uses and a tile would read
// as a program.
//
// The page is ink and the cells are paper, which is the inverse of real paper and is the
// right way round here for two reasons. It matches the application tile, so the two marks
// are recognisably one product rather than two. And it is what actually survives an
// unknown background: a light page depended entirely on its keyline to exist against a
// light file manager, whereas a dark page outlined in paper reads on light and on dark
// alike, and the keyline becomes a refinement instead of a load-bearing part.
func drawSheet(img *image.NRGBA) {
	// Portrait, like paper, and centred. A page drawn square reads as a tile again.
	x0, x1 := master*5/32, master-master*5/32
	y0, y1 := master/16, master-master/16
	flap := (x1 - x0) * 2 / 5

	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			c := ink
			// The corner cut and the flap folded down behind it. Inside the flap
			// square, above the diagonal is off the page entirely; below it is the
			// underside of the fold, which is what makes the page look like paper
			// rather than a rounded rectangle.
			if dx, dy := x-(x1-flap), y-y0; dx >= 0 && dy < flap {
				if dx > dy {
					c = none
				} else {
					c = fold
				}
			}
			img.SetNRGBA(x, y, c)
		}
	}

	outline(img, x0, y0, x1, y1, flap)
	drawNoise(img, x0, y0, x1, y1, flap)
}

// outline draws the page border. Only the straight edges: the diagonal is already defined
// by the flap behind it.
func outline(img *image.NRGBA, x0, y0, x1, y1, flap int) {
	fill(img, image.Rect(x0, y0, x1-flap, y0+keyline), paper) // top, up to the cut
	fill(img, image.Rect(x0, y1-keyline, x1, y1), paper)      // bottom
	fill(img, image.Rect(x0, y0, x0+keyline, y1), paper)      // left
	fill(img, image.Rect(x1-keyline, y0+flap, x1, y1), paper) // right, below the cut
}

// drawNoise fills the page with the grid the codec actually draws.
//
// The pattern is deterministic, from a generator written out here rather than taken from
// math/rand: an icon that changes every time it is regenerated produces a diff on every
// build and makes it impossible to tell a deliberate redesign from a rebuild.
func drawNoise(img *image.NRGBA, x0, y0, x1, y1, flap int) {
	pad := master / 32
	gx0, gy0 := x0+pad, y0+pad
	gx1, gy1 := x1-pad, y1-pad

	cell := (gx1 - gx0) / columns
	gap := cell / 10

	state := uint32(0x9e3779b9) // any fixed value; this one is the golden-ratio constant
	next := func() uint32 {
		// xorshift32, spelled out so this stays byte-identical for as long as the
		// language does.
		state ^= state << 13
		state ^= state >> 17
		state ^= state << 5
		return state
	}

	// A whole number of rows fits, and what is left over is split between top and bottom
	// rather than all landing under the last row. Aligning the grid to the top instead
	// left a blank band roughly a cell high across the foot of the page, which does not
	// read as a margin but as a printing fault: the eye reads the outline and the pattern
	// as one block, and an asymmetric gap inside a symmetric frame looks like a mistake.
	rows := (gy1 - gy0) / cell
	gy0 += ((gy1 - gy0) - rows*cell) / 2

	// One cell carries the accent, so the two marks are visibly the same product. Chosen
	// by position rather than by chance, or it would move whenever the grid resizes.
	accentRow, accentCol := rows/2, columns/2

	for row := range rows {
		for col := range columns {
			cx, cy := gx0+col*cell, gy0+row*cell
			// The generator advances for every cell, fold or not, so the pattern
			// does not shift when the fold changes size.
			roll := next()
			if cx+cell > x1-flap && cy < y0+flap {
				continue // under the fold, where nothing is printed
			}
			var c color.NRGBA
			switch {
			case row == accentRow && col == accentCol:
				c = signal
			case roll&1 == 0:
				c = paper
			default:
				continue // leave the page showing
			}
			fill(img, image.Rect(cx, cy, cx+cell-gap, cy+cell-gap), c)
		}
	}
}

func fill(img *image.NRGBA, r image.Rectangle, c color.NRGBA) {
	r = r.Intersect(img.Bounds())
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
}

func writePNG(name string, img *image.NRGBA) {
	f, err := os.Create(name)
	if err != nil {
		fail(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		fail(err)
	}
}

// encodeICO writes the container. Payloads are PNG, which every Windows since Vista
// reads, and which avoids hand-rolling the legacy DIB layout and its inverted rows.
func encodeICO(images []*image.NRGBA) ([]byte, error) {
	var payloads [][]byte
	for _, img := range images {
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			return nil, err
		}
		payloads = append(payloads, buf.Bytes())
	}

	var out bytes.Buffer
	// ICONDIR: reserved, type 1 (icon), count.
	binary.Write(&out, binary.LittleEndian, uint16(0))
	binary.Write(&out, binary.LittleEndian, uint16(1))
	binary.Write(&out, binary.LittleEndian, uint16(len(images)))

	offset := 6 + 16*len(images)
	for i, img := range images {
		w, h := img.Bounds().Dx(), img.Bounds().Dy()
		// 256 is written as 0, which is the format's way of saying "not a byte".
		out.WriteByte(byte(w % 256))
		out.WriteByte(byte(h % 256))
		out.WriteByte(0)                                    // palette entries, 0 for truecolour
		out.WriteByte(0)                                    // reserved
		binary.Write(&out, binary.LittleEndian, uint16(1))  // colour planes
		binary.Write(&out, binary.LittleEndian, uint16(32)) // bits per pixel
		binary.Write(&out, binary.LittleEndian, uint32(len(payloads[i])))
		binary.Write(&out, binary.LittleEndian, uint32(offset))
		offset += len(payloads[i])
	}
	for _, p := range payloads {
		out.Write(p)
	}
	return out.Bytes(), nil
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "icongen:", err)
	os.Exit(1)
}
