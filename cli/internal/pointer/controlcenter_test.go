package pointer

import (
	"image"
	"image/color"
	"math"
	"testing"
)

// Drawn stand-ins for an iPhone 16 Pro's frames (718x1560, measured 2026-11-12/13): Control Center over the Home
// Screen and over an app, and the Screen Mirroring panel. Colors are the ones measured on the phone.
var (
	ccBack    = color.RGBA{84, 80, 79, 255}
	white     = color.RGBA{254, 251, 252, 255}
	sysBlue   = color.RGBA{3, 122, 255, 255}
	tileGray  = color.RGBA{110, 108, 108, 255}
	panelGray = color.RGBA{100, 103, 107, 255}
	panelBack = color.RGBA{40, 46, 52, 255}
)

func canvas(bg color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, 718, 1560))
	paint(img, image.Rect(0, 0, 718, 1560), bg)
	return img
}

func paint(img *image.RGBA, r image.Rectangle, c color.RGBA) {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			img.SetRGBA(x, y, c)
		}
	}
}

func disc(img *image.RGBA, cx, cy, r int, c color.RGBA) {
	for y := cy - r; y <= cy+r; y++ {
		for x := cx - r; x <= cx+r; x++ {
			if (x-cx)*(x-cx)+(y-cy)*(y-cy) <= r*r {
				img.SetRGBA(x, y, c)
			}
		}
	}
}

// controlCenter: round buttons in a grid, one white one with a blue icon (Screen Mirroring, mirroring), one white
// with a red icon (silent mode), a white slider, and the app's name row on top when appRow.
func controlCenter(appRow bool) *image.RGBA {
	img := canvas(ccBack)
	dy := 0
	if appRow {
		dy = 33
		paint(img, image.Rect(300, 108, 450, 130), white) // the app's name
	}
	for _, c := range [][2]int{{136, 587}, {136, 884}, {285, 884}, {433, 884}, {582, 884}, {136, 1180}} {
		disc(img, c[0], c[1]+dy, 60, tileGray)
	}
	disc(img, 285, 587+dy, 60, white)
	paint(img, image.Rect(258, 565+dy, 312, 610+dy), sysBlue) // the mirroring icon
	disc(img, 285, 1180+dy, 60, white)
	paint(img, image.Rect(265, 1160+dy, 305, 1200+dy), color.RGBA{255, 80, 90, 255}) // the bell, red
	paint(img, image.Rect(522, 527+dy, 642, 797+dy), white)                          // a slider
	paint(img, image.Rect(560, 715+dy, 605, 755+dy), color.RGBA{0, 185, 230, 255})   // its cyan badge
	return img
}

// mirrorPanel: the panel over a dark background, with the icon on top and this board's row checked.
func mirrorPanel() *image.RGBA {
	img := canvas(panelBack)
	paint(img, image.Rect(62, 488, 657, 1070), panelGray)
	paint(img, image.Rect(332, 520, 390, 568), white)                         // the icon
	paint(img, image.Rect(252, 590, 467, 612), white)                         // "Screen Mirroring"
	paint(img, image.Rect(135, 678, 296, 700), white)                         // "ESP32 Agent"
	paint(img, image.Rect(597, 676, 622, 702), white)                         // the checkmark
	paint(img, image.Rect(62, 985, 657, 987), color.RGBA{120, 123, 127, 255}) // a row line
	paint(img, image.Rect(271, 1018, 447, 1040), color.RGBA{230, 230, 230, 255})
	disc(img, 655, 425, 45, white) // the AssistiveTouch button, outside the panel
	return img
}

// near: within 12 px of the expected 718x1560 position.
func near(p Point, x, y float64) bool {
	return math.Abs(p.X*718-x) <= 12 && math.Abs(p.Y*1560-y) <= 12
}

func TestFindMirrorTile(t *testing.T) {
	for _, appRow := range []bool{false, true} {
		img := controlCenter(appRow)
		want := 587.0
		if appRow {
			want = 620
		}
		if p, ok := FindMirrorTile(img); !ok || !near(p, 285, want) {
			t.Errorf("app row %v: tile %v at (%.0f, %.0f), want (285, %.0f)", appRow, ok, p.X*718, p.Y*1560, want)
		}
		if HasAppRow(img) != appRow {
			t.Errorf("app row %v: HasAppRow says %v", appRow, !appRow)
		}
	}
	if p, ok := FindMirrorTile(mirrorPanel()); ok {
		t.Errorf("found a tile in the panel at (%.0f, %.0f)", p.X*718, p.Y*1560)
	}
	plain := controlCenter(false) // not mirroring: the button is gray, nothing to find
	disc(plain, 285, 587, 60, tileGray)
	if _, ok := FindMirrorTile(plain); ok {
		t.Error("found a tile with no white mirroring button")
	}
}

func TestFindMirrorPanel(t *testing.T) {
	p, ok := FindMirrorPanel(mirrorPanel())
	if !ok || !p.Checked || !near(p.Stop, 359, 1029) || math.Abs(p.Row.Y*1560-689) > 12 {
		t.Errorf("%v %+v; want Stop at (359, 1029), the checked row at y 689", ok, p)
	}
	if p, ok := FindMirrorPanel(controlCenter(false)); ok {
		t.Errorf("found a panel in Control Center: %+v", p)
	}
}
