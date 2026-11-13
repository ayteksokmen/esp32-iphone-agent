package daemon

import (
	"image"
	"image/color"
	"math"
	"testing"
)

// cc draws Control Center with the white Screen Mirroring button at y (718x1560), and the app's name row on top
// when appRow (see pointer's tests for the full drawing).
func cc(y int, appRow bool) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, 718, 1560))
	set := func(r image.Rectangle, c color.RGBA) {
		for yy := r.Min.Y; yy < r.Max.Y; yy++ {
			for x := r.Min.X; x < r.Max.X; x++ {
				img.SetRGBA(x, yy, c)
			}
		}
	}
	set(img.Bounds(), color.RGBA{84, 80, 79, 255})
	if appRow {
		set(image.Rect(300, 108, 450, 130), color.RGBA{254, 251, 252, 255})
	}
	for yy := y - 60; yy <= y+60; yy++ {
		for x := 225; x <= 345; x++ {
			if (x-285)*(x-285)+(yy-y)*(yy-y) <= 3600 {
				img.SetRGBA(x, yy, color.RGBA{254, 251, 252, 255})
			}
		}
	}
	set(image.Rect(258, y-22, 312, y+23), color.RGBA{3, 122, 255, 255})
	return img
}

// Learning the button over an app gives the same layout as over the Home Screen, the blind tap lands inside the
// button in both places, and the layout survives a restart.
func TestMirrorLayoutLearn(t *testing.T) {
	home, app := defaultMirrorLayout, defaultMirrorLayout
	if _, ok := home.learnTile(cc(500, false)); !ok {
		t.Fatal("no button over the Home Screen")
	}
	if _, ok := app.learnTile(cc(533, true)); !ok {
		t.Fatal("no button over an app")
	}
	if d := math.Abs(home.Tile.Y-app.Tile.Y) * 1560; d > 8 {
		t.Errorf("button over the Home Screen and over an app %.0f px apart after the app row", d)
	}
	for _, y := range []float64{500, 533} { // the button's center in each place; it is 120 px across
		if d := math.Abs(home.blindTile().Y*1560 - y); d > 40 {
			t.Errorf("blind tap %.0f px from the button at y %.0f", d, y)
		}
	}
	if !home.Learned {
		t.Error("not marked learned")
	}
	dir := t.TempDir()
	if err := home.save(dir, "x"); err != nil {
		t.Fatal(err)
	}
	if got := loadMirrorLayout(dir, "x"); got != home {
		t.Errorf("loaded %+v, saved %+v", got, home)
	}
	if got := loadMirrorLayout(dir, "other"); got != defaultMirrorLayout {
		t.Errorf("no file: %+v, want the defaults", got)
	}
}
