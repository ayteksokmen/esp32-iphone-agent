package pointer

import (
	"image"
	"math"
)

// Finding Screen Mirroring in Control Center, so that restarting mirroring does not depend on one phone's layout.
// Measured on an iPhone 16 Pro (718x1560 frames, 2026-11-13); every length is scaled by the frame width.
//
// While the phone mirrors, the Screen Mirroring button is a white disc (radius ~60 px) with a blue icon, on the
// dark Control Center background. Other white controls are either not discs (the sliders) or have no blue in them
// (the silent-mode bell is red); the blue round buttons (Wi-Fi, AirDrop) are filled blue, not white.

type rgb struct{ r, g, b int }

func px(img image.Image, x, y int) (rgb, bool) {
	b := img.Bounds()
	if x < 0 || y < 0 || x >= b.Dx() || y >= b.Dy() {
		return rgb{}, false
	}
	r, g, bl, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
	return rgb{int(r >> 8), int(g >> 8), int(bl >> 8)}, true
}

func (c rgb) lum() int { return (c.r*299 + c.g*587 + c.b*114) / 1000 }

func (c rgb) white() bool {
	return c.lum() > 228 && max(c.r, c.g, c.b)-min(c.r, c.g, c.b) < 30
}

// systemBlue: the icon's blue (about 0,122,255); not the Apple TV badge's cyan (0,185,230).
func (c rgb) systemBlue() bool { return c.b > 200 && c.r < 90 && c.g > 90 && c.g < 175 }

// FindMirrorTile finds the Screen Mirroring button in a frame of Control Center while the phone mirrors.
func FindMirrorTile(img image.Image) (Point, bool) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	s := float64(w) / 718
	ring := func(cx, cy int, r float64, n int, ok func(rgb) bool) int {
		hits := 0
		for k := 0; k < n; k++ {
			a := 2 * math.Pi * float64(k) / float64(n)
			if c, in := px(img, cx+int(math.Round(r*s*math.Cos(a))), cy+int(math.Round(r*s*math.Sin(a)))); in && ok(c) {
				hits++
			}
		}
		return hits
	}
	inner, step := int(26*s), max(1, int(3*s))
	best, bestBlue := Point{}, 0
	for cy := 0; cy < h; cy += step {
		for cx := 0; cx < w; cx += step {
			if c, _ := px(img, cx, cy); c.b < 200 {
				continue
			}
			if ring(cx, cy, 45, 24, rgb.white) < 23 || ring(cx, cy, 56, 32, rgb.white) < 30 { // round, not an app icon
				continue
			}
			if ring(cx, cy, 72, 24, func(c rgb) bool { return !c.white() }) < 18 { // a disc, not a slider
				continue
			}
			blue := 0
			for dy := -inner; dy <= inner; dy += 2 {
				for dx := -inner; dx <= inner; dx += 2 {
					if c, in := px(img, cx+dx, cy+dy); in && dx*dx+dy*dy <= inner*inner && c.systemBlue() {
						blue++
					}
				}
			}
			if blue > bestBlue {
				best, bestBlue = Point{float64(cx) / float64(w), float64(cy) / float64(h)}, blue
			}
		}
	}
	return best, bestBlue >= 15
}

// HasAppRow: Control Center opened over an app shows the app's name in a row on top, and everything below moves
// down by about 32 px (on the Home Screen there is no such row).
func HasAppRow(img image.Image) bool {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	bright := 0
	for y := int(0.067 * float64(h)); y <= int(0.085*float64(h)); y += 2 {
		for x := int(0.3 * float64(w)); x <= int(0.7*float64(w)); x += 2 {
			if c, _ := px(img, x, y); c.lum() > 200 {
				bright++
			}
		}
	}
	return bright > 20
}

// MirrorPanel is the Screen Mirroring panel as found in a frame: where to tap Stop Mirroring and the receiver
// that has the checkmark (this board, while it mirrors).
type MirrorPanel struct {
	Top, Bottom float64 // the panel's edges, fractions of the height
	Stop        Point
	Row         Point // the checked receiver; zero when none is checked
	Checked     bool
}

// FindMirrorPanel finds the Screen Mirroring panel: a gray rounded rectangle in the middle of the screen whose last
// row is Stop Mirroring. The checkmark is the only bright mark near its right edge between the title and that row.
func FindMirrorPanel(img image.Image) (MirrorPanel, bool) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	x := int(0.89 * float64(w))
	gray := func(c rgb) bool {
		l := c.lum()
		return l >= 70 && l <= 170 && max(c.r, c.g, c.b)-min(c.r, c.g, c.b) < 25
	}
	top, end, bestTop, bestRun := -1, 0, 0, 0
	for y := 0; y < h; y++ {
		c, _ := px(img, x, y)
		switch {
		case gray(c):
			if top < 0 {
				top = y
			}
			end = y
			if end-top > bestRun {
				bestTop, bestRun = top, end-top
			}
		case top >= 0 && y-end > 4: // the row lines are a pixel or two of another shade
			top = -1
		}
	}
	if bestRun < h/4 || bestRun > h*3/5 || bestTop+bestRun > h*9/10 {
		return MirrorPanel{}, false
	}
	pt, pb := bestTop, bestTop+bestRun
	p := MirrorPanel{Top: float64(pt) / float64(h), Bottom: float64(pb) / float64(h),
		Stop: Point{0.5, float64(pb)/float64(h) - 20.0/1560}} // the gray run ends ~20 px above the rounded bottom edge
	ph := float64(pb - pt)
	icon := 0 // the Screen Mirroring icon above the title, white in the middle of the top
	for y := pt + int(0.04*ph); y < pt+int(0.14*ph); y++ {
		for x := int(0.44 * float64(w)); x <= int(0.56*float64(w)); x++ {
			if c, _ := px(img, x, y); c.lum() > 200 {
				icon++
			}
		}
	}
	if icon < 30 {
		return MirrorPanel{}, false
	}
	// "Stop Mirroring": the light text in the middle of the panel's last row
	sum, n := 0, 0
	for y := pb - int(0.15*ph); y <= pb; y++ {
		for x := int(0.35 * float64(w)); x <= int(0.65*float64(w)); x++ {
			if c, _ := px(img, x, y); c.lum() > 150 {
				sum += y
				n++
			}
		}
	}
	if n >= 20 {
		p.Stop.Y = float64(sum) / float64(n) / float64(h)
	}
	sum, n = 0, 0
	for y := pt + int(0.2*ph); y < pb-int(0.15*ph); y++ {
		for x := int(0.80 * float64(w)); x <= int(0.88*float64(w)); x++ {
			if c, _ := px(img, x, y); c.lum() > 200 {
				sum += y
				n++
			}
		}
	}
	if n >= 20 {
		p.Row, p.Checked = Point{0.3, float64(sum) / float64(n) / float64(h)}, true
	}
	return p, true
}
