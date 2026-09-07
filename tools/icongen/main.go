// Command icongen draws the NoiseCrypt mark and packs it into a Windows .ico.
//
// The logo is not a picture of the product, it is the product: a grid of macro cells,
// the same thing the codec draws when it turns a file into video. Drawing it in code
// rather than in an editor keeps that literal, and means the mark can be regenerated at
// any size without an asset pipeline, a design tool or a node_modules.
//
// ICO is a simple container: a header, one directory entry per size, then the payloads.
// Since Windows Vista those payloads may be PNG, which `image/png` in the standard
// library already writes, so this needs no dependency at all.
//
//	go run ./tools/icongen -out assets/noisecrypt.ico
package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
)

// The palette is the interface's, so the mark and the page look like the same product.
var (
	ink    = color.NRGBA{0x0a, 0x0b, 0x0d, 0xff}
	paper  = color.NRGBA{0xe8, 0xe6, 0xe1, 0xff}
	signal = color.NRGBA{0xff, 0xb0, 0x20, 0xff}
)

// Sizes Windows actually asks for. 16 is the one that decides whether a mark works: a
// design that only reads at 256 is a design nobody sees, because the places an icon
// matters most are the taskbar and a context menu.
var sizes = []int{16, 24, 32, 48, 64, 128, 256}

func main() {
	out := flag.String("out", "assets/noisecrypt.ico", "the .ico to write")
	pngDir := flag.String("png-dir", "", "also write each size as a PNG here, for review")
	flag.Parse()

	var images []*image.NRGBA
	for _, size := range sizes {
		images = append(images, draw(size))
	}

	if *pngDir != "" {
		if err := os.MkdirAll(*pngDir, 0o755); err != nil {
			fail(err)
		}
		for i, img := range images {
			name := filepath.Join(*pngDir, fmt.Sprintf("noisecrypt-%d.png", sizes[i]))
			f, err := os.Create(name)
			if err != nil {
				fail(err)
			}
			if err := png.Encode(f, img); err != nil {
				fail(err)
			}
			f.Close()
		}
	}

	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		fail(err)
	}
	blob, err := encodeICO(images)
	if err != nil {
		fail(err)
	}
	if err := os.WriteFile(*out, blob, 0o644); err != nil {
		fail(err)
	}
	fmt.Printf("%s: %d sizes, %d bytes\n", *out, len(images), len(blob))
}

func draw(size int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	fill(img, img.Bounds(), ink)
	drawGlyph(img, size)
	return img
}

// drawGlyph is an N built out of cells, for comparison. Kept so the choice between a
// literal mark and a lettered one is visible rather than asserted.
func drawGlyph(img *image.NRGBA, size int) {
	const cells = 5
	margin := max(1, size/6)
	span := size - 2*margin
	cell := span / cells
	offset := margin + (span-cell*cells)/2

	// An N on a five by five grid.
	shape := [cells][cells]int{
		{1, 0, 0, 0, 1},
		{1, 1, 0, 0, 1},
		{1, 0, 1, 0, 1},
		{1, 0, 0, 1, 1},
		{1, 0, 0, 0, 1},
	}
	for row := range cells {
		for col := range cells {
			if shape[row][col] == 0 {
				continue
			}
			c := paper
			if row == col {
				c = signal
			}
			fill(img, image.Rect(
				offset+col*cell, offset+row*cell,
				offset+(col+1)*cell-gap(size), offset+(row+1)*cell-gap(size),
			), c)
		}
	}
}

// gap is the sliver of background between cells that makes them read as separate.
// Below 32 pixels there is no room for one, and forcing it there turns the cells into
// dots.
func gap(size int) int {
	if size < 32 {
		return 0
	}
	return max(1, size/64)
}

func fill(img *image.NRGBA, r image.Rectangle, c color.NRGBA) {
	r = r.Intersect(img.Bounds())
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			img.SetNRGBA(x, y, c)
		}
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
