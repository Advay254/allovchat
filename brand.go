package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// ---------- brand assets: the symbol is drawn in code so the icons and share image always match the logo ----------

const faviconSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64"><rect width="64" height="64" rx="15" fill="#0B1220"/><g transform="translate(6 16.8) scale(.52)"><path fill="#2563FF" fill-rule="evenodd" d="M28 4a26 26 0 1 0 0 52 26 26 0 0 0 0-52zm0 14a12 12 0 1 1 0 24 12 12 0 0 1 0-24z"/><path fill="#2563FF" d="M36 48l16 10-6-16z"/><path fill="#fff" fill-rule="evenodd" d="M70 4A26 26 0 0 0 44 30 26 26 0 0 0 70 56H96V30A26 26 0 0 0 70 4zM70 17a11 11 0 1 0 0 22 11 11 0 0 0 0-22z"/><rect x="64" y="22" width="12" height="9" rx="4.5" fill="#fff"/><path fill="#fff" d="M65 30h5l-5 6z"/></g></svg>`

func (a *App) favicon(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write([]byte(faviconSVG))
}

var (
	cNavy  = color.RGBA{0x0B, 0x12, 0x20, 255}
	cBlue  = color.RGBA{0x25, 0x63, 0xFF, 255}
	cWhite = color.RGBA{255, 255, 255, 255}
	cSlate = color.RGBA{0x16, 0x23, 0x38, 255}
	imgs   sync.Map
)

func rrect(img *image.RGBA, x0, y0, x1, y1, r int, c color.RGBA) {
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			dx, dy := 0, 0
			if x < x0+r {
				dx = x0 + r - x
			} else if x >= x1-r {
				dx = x - (x1 - r - 1)
			}
			if y < y0+r {
				dy = y0 + r - y
			} else if y >= y1-r {
				dy = y - (y1 - r - 1)
			}
			if dx*dx+dy*dy <= r*r {
				img.SetRGBA(x, y, c)
			}
		}
	}
}

func blendPx(img *image.RGBA, x, y int, c color.RGBA, a float64) {
	if !image.Pt(x, y).In(img.Bounds()) || a <= 0 {
		return
	}
	d := img.RGBAAt(x, y)
	mix := func(s, t uint8) uint8 { return uint8(float64(s)*(1-a) + float64(t)*a) }
	img.SetRGBA(x, y, color.RGBA{mix(d.R, c.R), mix(d.G, c.G), mix(d.B, c.B), 255})
}

// glow paints a soft blue radial light.
func glow(img *image.RGBA, cx, cy, radius int, c color.RGBA, strength float64) {
	for y := cy - radius; y < cy+radius; y++ {
		for x := cx - radius; x < cx+radius; x++ {
			d := math.Hypot(float64(x-cx), float64(y-cy)) / float64(radius)
			if d < 1 {
				blendPx(img, x, y, c, strength*(1-d)*(1-d))
			}
		}
	}
}

func tri(px, py, ax, ay, bx, by, cx, cy float64) bool {
	d1 := (px-bx)*(ay-by) - (ax-bx)*(py-by)
	d2 := (px-cx)*(by-cy) - (bx-cx)*(py-cy)
	d3 := (px-ax)*(cy-ay) - (cx-ax)*(py-ay)
	return !((d1 < 0 || d2 < 0 || d3 < 0) && (d1 > 0 || d2 > 0 || d3 > 0))
}

func roundRectHit(px, py, x0, y0, x1, y1, r float64) bool {
	if px < x0 || px > x1 || py < y0 || py > y1 {
		return false
	}
	cx, cy := math.Max(x0+r, math.Min(px, x1-r)), math.Max(y0+r, math.Min(py, y1-r))
	return math.Hypot(px-cx, py-cy) <= r
}

// markAt returns the colour of the brand symbol at a point on its 100 x 60 design grid.
// Two overlapping chat bubbles: a blue ring on the left, a solid bubble with a small speech bubble cut-out on the right.
func markAt(x, y float64, right color.RGBA) (color.RGBA, bool) {
	if math.Hypot(x-70, y-30) <= 26 || (x >= 70 && x <= 96 && y >= 30 && y <= 56) {
		if math.Hypot(x-70, y-28) > 11 || roundRectHit(x, y, 64, 22, 76, 31, 4.5) || tri(x, y, 65, 30, 65, 36, 70, 30) {
			return right, true
		}
	}
	d := math.Hypot(x-28, y-30)
	if (d >= 12 && d <= 26) || tri(x, y, 36, 48, 52, 58, 46, 42) {
		return cBlue, true
	}
	return color.RGBA{}, false
}

func paintMark(img *image.RGBA, ox, oy int, s float64, right color.RGBA) {
	w, h := int(100*s)+1, int(60*s)+1
	for py := 0; py < h; py++ {
		for px := 0; px < w; px++ {
			var r, g, b, n float64
			for sy := 0; sy < 2; sy++ {
				for sx := 0; sx < 2; sx++ {
					x, y := (float64(px)+0.25+0.5*float64(sx))/s, (float64(py)+0.25+0.5*float64(sy))/s
					if c, ok := markAt(x, y, right); ok {
						r, g, b, n = r+float64(c.R), g+float64(c.G), b+float64(c.B), n+1
					}
				}
			}
			if n > 0 {
				blendPx(img, ox+px, oy+py, color.RGBA{uint8(r / n), uint8(g / n), uint8(b / n), 255}, n/4)
			}
		}
	}
}

func renderPNG(key string, w, h int, draw func(*image.RGBA)) []byte {
	if v, ok := imgs.Load(key); ok {
		return v.([]byte)
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = cNavy.R, cNavy.G, cNavy.B, 255
	}
	draw(img)
	var buf bytes.Buffer
	png.Encode(&buf, img)
	imgs.Store(key, buf.Bytes())
	return buf.Bytes()
}

func servePNG(w http.ResponseWriter, data []byte) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write(data)
}

func (a *App) ogImage(w http.ResponseWriter, r *http.Request) {
	servePNG(w, renderPNG("og2", 1200, 630, func(img *image.RGBA) {
		glow(img, 1000, 140, 560, cBlue, 0.42)
		paintMark(img, 110, 160, 5.2, cWhite)
		rrect(img, 700, 210, 1090, 246, 18, cSlate)
		rrect(img, 700, 278, 990, 314, 18, cSlate)
		rrect(img, 700, 346, 880, 382, 18, cSlate)
		rrect(img, 700, 440, 900, 496, 28, cBlue)
	}))
}

func (a *App) icon(w http.ResponseWriter, r *http.Request) {
	n, err := strconv.Atoi(strings.TrimSuffix(r.PathValue("size"), ".png"))
	if err != nil || (n != 180 && n != 192 && n != 512) {
		http.NotFound(w, r)
		return
	}
	data := renderPNG("icon2"+strconv.Itoa(n), n, n, func(img *image.RGBA) {
		s := float64(n) * 0.6 / 100
		paintMark(img, int((float64(n)-100*s)/2), int((float64(n)-60*s)/2), s, cWhite)
	})
	servePNG(w, data)
}
