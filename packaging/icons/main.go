// Command icons draws Itan's app and tray icons from the logo in the web
// UI (a rounded square with an amber gradient and a dark play triangle):
//
//	go run ./packaging/icons
//
// It writes the macOS app icon (Itan.icns), the Windows app icon
// (itan.ico) and the tray icons the desktop app embeds.
package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"os"
	"path/filepath"
)

// The logo, in the 32-unit space of its SVG.
const (
	unit   = 32.0
	radius = 9.0
)

var (
	gradFrom = [3]float64{0xff, 0xd2, 0x7a}
	gradTo   = [3]float64{0xf0, 0x8a, 0x24}
	ink      = [3]float64{0x1c, 0x14, 0x04}
	triangle = [3][2]float64{{12.5, 10}, {12.5, 22}, {22.5, 16}}
)

func main() {
	out := func(path string, data []byte) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			log.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			log.Fatal(err)
		}
		log.Printf("wrote %s (%d bytes)", path, len(data))
	}

	// macOS: the square sits inside the canvas with room for a shadow, like
	// other Mac app icons.
	var icns []icnsEntry
	for _, e := range []struct {
		kind string
		size int
	}{{"ic07", 128}, {"ic08", 256}, {"ic09", 512}, {"ic10", 1024}, {"ic11", 32}, {"ic12", 64}, {"ic13", 256}, {"ic14", 512}} {
		icns = append(icns, icnsEntry{e.kind, encodePNG(macIcon(e.size))})
	}
	out("packaging/macos/Itan.icns", encodeICNS(icns))

	// Windows: the square fills the icon.
	out("packaging/windows/itan.ico", encodeICO([]int{16, 20, 24, 32, 40, 48, 64, 128, 256}, logo))

	// Tray: a black template image on macOS (the system colours it for
	// light and dark menu bars) and the full-colour logo on Windows.
	out("cmd/itan-desktop/icons/tray-template.png", encodePNG(template(32)))
	out("cmd/itan-desktop/icons/tray.ico", encodeICO([]int{16, 20, 24, 32, 40, 48, 64}, logo))
}

// coverage returns how much of the pixel at (px, py) a shape covers, by
// sampling it 4×4 times. scale maps the logo's 32 units to pixels.
func coverage(px, py int, scale, offset float64, inside func(x, y float64) bool) float64 {
	const n = 4
	hits := 0
	for sy := 0; sy < n; sy++ {
		for sx := 0; sx < n; sx++ {
			x := (float64(px) + (float64(sx)+0.5)/n - offset) / scale
			y := (float64(py) + (float64(sy)+0.5)/n - offset) / scale
			if inside(x, y) {
				hits++
			}
		}
	}
	return float64(hits) / (n * n)
}

func inSquare(x, y float64) bool {
	// Signed distance to a rounded box centred in the 32-unit square.
	h := unit/2 - radius
	qx, qy := math.Abs(x-unit/2)-h, math.Abs(y-unit/2)-h
	d := math.Hypot(math.Max(qx, 0), math.Max(qy, 0)) + math.Min(math.Max(qx, qy), 0) - radius
	return d <= 0
}

func inTriangle(x, y float64) bool {
	edge := func(a, b [2]float64) float64 {
		return (b[0]-a[0])*(y-a[1]) - (b[1]-a[1])*(x-a[0])
	}
	t := triangle
	e1, e2, e3 := edge(t[0], t[1]), edge(t[1], t[2]), edge(t[2], t[0])
	return (e1 >= 0 && e2 >= 0 && e3 >= 0) || (e1 <= 0 && e2 <= 0 && e3 <= 0)
}

// draw paints the logo into img, the square spanning size pixels from
// (offset, offset).
func draw(img *image.NRGBA, size, offset float64) {
	scale := size / unit
	b := img.Bounds()
	for py := b.Min.Y; py < b.Max.Y; py++ {
		for px := b.Min.X; px < b.Max.X; px++ {
			sq := coverage(px, py, scale, offset, inSquare)
			if sq == 0 {
				continue
			}
			tri := coverage(px, py, scale, offset, inTriangle)
			// Gradient from top left to bottom right, as in the SVG.
			x := (float64(px) + 0.5 - offset) / scale
			y := (float64(py) + 0.5 - offset) / scale
			t := math.Max(0, math.Min(1, (x+y)/(2*unit)))
			var c [3]float64
			for i := range c {
				g := gradFrom[i] + (gradTo[i]-gradFrom[i])*t
				c[i] = g + (ink[i]-g)*tri
			}
			over(img, px, py, c, sq)
		}
	}
}

// over composites colour c with alpha a over the pixel.
func over(img *image.NRGBA, x, y int, c [3]float64, a float64) {
	dst := img.NRGBAAt(x, y)
	da := float64(dst.A) / 255
	oa := a + da*(1-a)
	if oa == 0 {
		return
	}
	mix := func(s float64, d uint8) uint8 {
		return uint8(math.Round((s*a + float64(d)*da*(1-a)) / oa))
	}
	img.SetNRGBA(x, y, color.NRGBA{mix(c[0], dst.R), mix(c[1], dst.G), mix(c[2], dst.B), uint8(math.Round(oa * 255))})
}

func logo(size int) image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	draw(img, float64(size), 0)
	return img
}

// macIcon follows the macOS icon grid: an 824/1024 square with a soft
// shadow below it.
func macIcon(size int) image.Image {
	s := float64(size)
	square, offset := s*824/1024, s*100/1024
	img := image.NewNRGBA(image.Rect(0, 0, size, size))

	shadow := make([]float64, size*size)
	scale := square / unit
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			shadow[py*size+px] = coverage(px, py, scale, offset, inSquare)
		}
	}
	shadow = blur(shadow, size, int(math.Max(1, s*12/1024)))
	dy := int(math.Round(s * 10 / 1024))
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			if sy := py - dy; sy >= 0 {
				if a := shadow[sy*size+px] * 0.3; a > 0 {
					over(img, px, py, [3]float64{0, 0, 0}, a)
				}
			}
		}
	}
	draw(img, square, offset)
	return img
}

// blur is three passes of a box blur, close to a Gaussian.
func blur(a []float64, size, r int) []float64 {
	tmp := make([]float64, len(a))
	pass := func(src, dst []float64, horizontal bool) {
		for i := 0; i < size; i++ {
			for j := 0; j < size; j++ {
				var sum float64
				for k := -r; k <= r; k++ {
					jj := j + k
					if jj < 0 || jj >= size {
						continue
					}
					if horizontal {
						sum += src[i*size+jj]
					} else {
						sum += src[jj*size+i]
					}
				}
				if horizontal {
					dst[i*size+j] = sum / float64(2*r+1)
				} else {
					dst[j*size+i] = sum / float64(2*r+1)
				}
			}
		}
	}
	for n := 0; n < 3; n++ {
		pass(a, tmp, true)
		pass(tmp, a, false)
	}
	return a
}

// template is the logo's silhouette with the triangle cut out, in black.
func template(size int) image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	pad := float64(size) / 16
	square := float64(size) - 2*pad
	scale := square / unit
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			a := coverage(px, py, scale, pad, inSquare) - coverage(px, py, scale, pad, inTriangle)
			if a > 0 {
				img.SetNRGBA(px, py, color.NRGBA{0, 0, 0, uint8(math.Round(a * 255))})
			}
		}
	}
	return img
}

func encodePNG(img image.Image) []byte {
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		log.Fatal(err)
	}
	return b.Bytes()
}

type icnsEntry struct {
	kind string
	data []byte
}

func encodeICNS(entries []icnsEntry) []byte {
	var body bytes.Buffer
	for _, e := range entries {
		body.WriteString(e.kind)
		binary.Write(&body, binary.BigEndian, uint32(8+len(e.data)))
		body.Write(e.data)
	}
	var b bytes.Buffer
	b.WriteString("icns")
	binary.Write(&b, binary.BigEndian, uint32(8+body.Len()))
	b.Write(body.Bytes())
	return b.Bytes()
}

// encodeICO writes an icon with one image per size: bitmaps up to 64
// pixels, which every Windows API reads, and PNG above that.
func encodeICO(sizes []int, draw func(int) image.Image) []byte {
	var images [][]byte
	for _, s := range sizes {
		img := draw(s)
		if s > 64 {
			images = append(images, encodePNG(img))
		} else {
			images = append(images, icoBitmap(img))
		}
	}
	var b bytes.Buffer
	binary.Write(&b, binary.LittleEndian, [3]uint16{0, 1, uint16(len(sizes))})
	offset := 6 + 16*len(sizes)
	for i, s := range sizes {
		dim := uint8(s)
		if s >= 256 {
			dim = 0
		}
		b.Write([]byte{dim, dim, 0, 0})
		binary.Write(&b, binary.LittleEndian, [2]uint16{1, 32})
		binary.Write(&b, binary.LittleEndian, [2]uint32{uint32(len(images[i])), uint32(offset)})
		offset += len(images[i])
	}
	for _, data := range images {
		b.Write(data)
	}
	return b.Bytes()
}

// icoBitmap is a 32-bit BGRA bitmap, bottom row first, with an empty AND
// mask (the alpha channel does the masking).
func icoBitmap(img image.Image) []byte {
	s := img.Bounds().Dx()
	maskRow := (s + 31) / 32 * 4
	var b bytes.Buffer
	binary.Write(&b, binary.LittleEndian, struct {
		Size                   uint32
		Width, Height          int32
		Planes, BitCount       uint16
		Compression, SizeImage uint32
		XPels, YPels           int32
		ClrUsed, ClrImportant  uint32
	}{40, int32(s), int32(2 * s), 1, 32, 0, uint32(s*s*4 + maskRow*s), 0, 0, 0, 0})
	for y := s - 1; y >= 0; y-- {
		for x := 0; x < s; x++ {
			c := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
			b.Write([]byte{c.B, c.G, c.R, c.A})
		}
	}
	b.Write(make([]byte, maskRow*s))
	return b.Bytes()
}
