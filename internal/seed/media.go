package seed

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
)

// Demo attachments are small, deterministic bitmaps rendered with the standard
// library: no fixture files, identical bytes on every boot (the blob store is
// write-once, so a re-seed never rewrites them anyway). A PNG is one of the
// passive types the media handler serves inline; a PDF is not, so it is served
// as a download — both paths of GET /api/events/{id}/media/{i} get exercised.

const (
	imgW = 320
	imgH = 200
)

// brand is the source's timeline colour (frontend/src/lib/platform.js); the
// bitmaps are tinted with it so a thumbnail reads as belonging to its source.
var brand = map[string]color.RGBA{
	"reddit":    {0xff, 0x45, 0x00, 0xff},
	"twitter":   {0x1d, 0x9b, 0xf0, 0xff},
	"instagram": {0xe1, 0x30, 0x6c, 0xff},
	"facebook":  {0x18, 0x77, 0xf2, 0xff},
	"google":    {0xff, 0x00, 0x00, 0xff},
	"whatsapp":  {0x25, 0xd3, 0x66, 0xff},
	"telegram":  {0x26, 0xa5, 0xe4, 0xff},
	"snapchat":  {0xff, 0xfc, 0x00, 0xff},
	"tiktok":    {0xfe, 0x2c, 0x55, 0xff},
	"discord":   {0x58, 0x65, 0xf2, 0xff},
	"linkedin":  {0x0a, 0x66, 0xc2, 0xff},
}

// pictureStyle selects one of the bitmap looks.
type pictureStyle int

const (
	styleGradient  pictureStyle = iota // diagonal two-tone gradient
	styleBlocks                        // colour-block "poster"
	styleLandscape                     // sky, sun, hills: the "photo"
	styleCity                          // night skyline with lit windows
)

// picture renders a 320×200 PNG in the source's brand colour. seed varies the
// composition so twenty attachments do not all look alike.
func picture(source string, style pictureStyle, seed int) []byte {
	base, ok := brand[source]
	if !ok {
		base = color.RGBA{0x8b, 0x93, 0xa7, 0xff}
	}
	img := image.NewRGBA(image.Rect(0, 0, imgW, imgH))
	switch style {
	case styleBlocks:
		drawBlocks(img, base, seed)
	case styleLandscape:
		drawLandscape(img, base, seed)
	case styleCity:
		drawCity(img, base, seed)
	default:
		drawGradient(img, base, seed)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(fmt.Sprintf("seed: encode png: %v", err)) // in-memory encode of a valid image cannot fail
	}
	return buf.Bytes()
}

func mix(a, b color.RGBA, t float64) color.RGBA {
	t = math.Max(0, math.Min(1, t))
	l := func(x, y uint8) uint8 { return uint8(float64(x) + (float64(y)-float64(x))*t) }
	return color.RGBA{l(a.R, b.R), l(a.G, b.G), l(a.B, b.B), 0xff}
}

func shade(c color.RGBA, f float64) color.RGBA {
	if f < 1 {
		return mix(color.RGBA{0, 0, 0, 0xff}, c, f)
	}
	return mix(c, color.RGBA{0xff, 0xff, 0xff, 0xff}, f-1)
}

func drawGradient(img *image.RGBA, base color.RGBA, seed int) {
	dark, light := shade(base, 0.45), shade(base, 1.55)
	angle := float64(seed%5) * 0.3
	for y := 0; y < imgH; y++ {
		for x := 0; x < imgW; x++ {
			t := (float64(x)/imgW)*math.Cos(angle) + (float64(y)/imgH)*math.Sin(angle)
			img.SetRGBA(x, y, mix(dark, light, t))
		}
	}
	// a soft disc, offset by seed
	cx, cy, r := 80+(seed*53)%180, 60+(seed*29)%90, 34+(seed*7)%40
	for y := cy - r; y <= cy+r; y++ {
		for x := cx - r; x <= cx+r; x++ {
			if x < 0 || y < 0 || x >= imgW || y >= imgH {
				continue
			}
			d := math.Hypot(float64(x-cx), float64(y-cy))
			if d <= float64(r) {
				img.SetRGBA(x, y, mix(img.RGBAAt(x, y), color.RGBA{0xff, 0xff, 0xff, 0xff}, 0.35))
			}
		}
	}
}

func drawBlocks(img *image.RGBA, base color.RGBA, seed int) {
	tones := []color.RGBA{shade(base, 0.5), shade(base, 0.8), base, shade(base, 1.35), shade(base, 1.7)}
	cols, rows := 4+seed%3, 3+seed%2
	cw, rh := imgW/cols, imgH/rows
	for r := 0; r < rows; r++ {
		for c := 0; c < cols; c++ {
			t := tones[(r*cols+c+seed)%len(tones)]
			for y := r * rh; y < (r+1)*rh && y < imgH; y++ {
				for x := c * cw; x < (c+1)*cw && x < imgW; x++ {
					img.SetRGBA(x, y, t)
				}
			}
		}
	}
}

// drawLandscape is the "holiday photo": a sky graded towards the horizon, a
// sun and two ranges of hills, all tinted by the source colour.
func drawLandscape(img *image.RGBA, base color.RGBA, seed int) {
	skyTop := mix(color.RGBA{0x1e, 0x2a, 0x4a, 0xff}, base, 0.25)
	skyLow := mix(color.RGBA{0xff, 0xd7, 0x9a, 0xff}, base, 0.35)
	horizon := 120 + seed%20
	for y := 0; y < horizon; y++ {
		c := mix(skyTop, skyLow, float64(y)/float64(horizon))
		for x := 0; x < imgW; x++ {
			img.SetRGBA(x, y, c)
		}
	}
	// sun
	sx, sy, sr := 60+(seed*41)%200, horizon-30-(seed*13)%40, 16+seed%10
	sun := mix(color.RGBA{0xff, 0xf3, 0xc0, 0xff}, base, 0.15)
	for y := sy - sr; y <= sy+sr; y++ {
		for x := sx - sr; x <= sx+sr; x++ {
			if x >= 0 && y >= 0 && x < imgW && y < imgH && math.Hypot(float64(x-sx), float64(y-sy)) <= float64(sr) {
				img.SetRGBA(x, y, sun)
			}
		}
	}
	// hills: two sine ranges, back lighter, front darker
	back, front := shade(mix(base, color.RGBA{0x2f, 0x5d, 0x3a, 0xff}, 0.6), 0.9), shade(mix(base, color.RGBA{0x1b, 0x3a, 0x22, 0xff}, 0.7), 0.6)
	for x := 0; x < imgW; x++ {
		fx := float64(x)
		yb := float64(horizon) - 18 - 14*math.Sin(fx/48+float64(seed)) - 6*math.Sin(fx/17)
		yf := float64(horizon) + 10 - 20*math.Sin(fx/70+float64(seed)*0.7) - 5*math.Sin(fx/23+1)
		for y := int(yb); y < imgH; y++ {
			if y >= 0 {
				img.SetRGBA(x, y, back)
			}
		}
		for y := int(yf); y < imgH; y++ {
			if y >= 0 {
				img.SetRGBA(x, y, front)
			}
		}
	}
}

// drawCity is the "night out" photo: a dark gradient sky over a skyline of
// towers with lit windows.
func drawCity(img *image.RGBA, base color.RGBA, seed int) {
	top, low := color.RGBA{0x0b, 0x10, 0x20, 0xff}, mix(color.RGBA{0x2a, 0x1e, 0x3c, 0xff}, base, 0.3)
	for y := 0; y < imgH; y++ {
		c := mix(top, low, float64(y)/imgH)
		for x := 0; x < imgW; x++ {
			img.SetRGBA(x, y, c)
		}
	}
	wall, lit := color.RGBA{0x14, 0x16, 0x1e, 0xff}, mix(color.RGBA{0xff, 0xe6, 0x9a, 0xff}, base, 0.2)
	x := 0
	n := seed
	for x < imgW {
		n = n*1103515245 + 12345
		w := 18 + (n>>16)%30
		n = n*1103515245 + 12345
		h := 50 + (n>>16)%120
		for yy := imgH - h; yy < imgH; yy++ {
			for xx := x; xx < x+w && xx < imgW; xx++ {
				img.SetRGBA(xx, yy, wall)
			}
		}
		for yy := imgH - h + 4; yy < imgH-4; yy += 7 {
			for xx := x + 3; xx < x+w-3 && xx < imgW; xx += 6 {
				n = n*1103515245 + 12345
				if (n>>16)%3 != 0 {
					img.SetRGBA(xx, yy, lit)
					img.SetRGBA(xx+1, yy, lit)
					img.SetRGBA(xx, yy+1, lit)
					img.SetRGBA(xx+1, yy+1, lit)
				}
			}
		}
		x += w + 2
	}
}

// pdfDocument is a minimal, valid one-page PDF (Helvetica, a few lines of
// text) written by hand, with a correct cross-reference table: ~1 KB, enough
// for a viewer to open the download the media endpoint serves.
func pdfDocument(title string, lines []string) []byte {
	var content bytes.Buffer
	fmt.Fprintf(&content, "BT /F1 20 Tf 72 760 Td (%s) Tj ET\n", pdfEscape(title))
	y := 720
	for _, l := range lines {
		fmt.Fprintf(&content, "BT /F1 12 Tf 72 %d Td (%s) Tj ET\n", y, pdfEscape(l))
		y -= 18
	}
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", content.Len(), content.String()),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	var out bytes.Buffer
	out.WriteString("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
	offsets := make([]int, len(objects)+1)
	for i, obj := range objects {
		offsets[i+1] = out.Len()
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", i+1, obj)
	}
	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n", len(objects)+1)
	out.WriteString("0000000000 65535 f \n")
	for _, off := range offsets[1:] {
		fmt.Fprintf(&out, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return out.Bytes()
}

func pdfEscape(s string) string {
	var b bytes.Buffer
	for _, r := range s {
		switch {
		case r == '(' || r == ')' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20 || r > 0x7e:
			b.WriteByte('?')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
