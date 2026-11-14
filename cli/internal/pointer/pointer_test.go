package pointer

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeDevice records commands and, as a phone would, tracks where the pointer is in mouse units.
type fakeDevice struct {
	mu    sync.Mutex
	sent  []string
	x, y  int
	fail  map[string]bool
	moved time.Time
	// the screen edge in units, as the phone stops the pointer there (0: no edge)
	maxX, maxY int
}

func (d *fakeDevice) Links() int { return 1 }

func (d *fakeDevice) Send(_ context.Context, line string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sent = append(d.sent, line)
	cmd, arg, _ := strings.Cut(line, " ")
	if d.fail[cmd] {
		return fmt.Errorf("err unknown command")
	}
	switch cmd {
	case "home":
		d.x, d.y = 0, 0
	case "move":
		var dx, dy int
		fmt.Sscanf(arg, "%d %d", &dx, &dy)
		d.x, d.y = max(0, d.x+dx), max(0, d.y+dy)
		if d.maxX > 0 {
			d.x, d.y = min(d.x, d.maxX), min(d.y, d.maxY)
		}
	}
	d.moved = time.Now()
	return nil
}

func nosleep(context.Context, time.Duration) error { return nil }

func newPointer(t *testing.T, d Device, s Screen) *Pointer {
	p := New(d, s, filepath.Join(t.TempDir(), "cal.json"))
	p.Sleep = nosleep
	return p
}

func TestPointerRehomesEveryTenGestures(t *testing.T) {
	d := &fakeDevice{}
	p := newPointer(t, d, nil)
	p.FinishCalibration(0.5, 0.25) // span 400 x 800
	for i := 0; i < 11; i++ {
		if err := p.Point(context.Background(), 0.5, 0.5, false); err != nil {
			t.Fatal(err)
		}
	}
	homes := 0
	for _, s := range d.sent {
		if s == "home" {
			homes++
		}
	}
	if homes != 2 || d.sent[0] != "release" || d.sent[2] != "move 200 0" || d.sent[3] != "move 0 400" {
		t.Fatalf("sent %v", d.sent)
	}
}

func TestDoubleTapClicksTwiceInPlace(t *testing.T) {
	d := &fakeDevice{}
	p := newPointer(t, d, nil)
	p.FinishCalibration(0.5, 0.25)
	if err := p.Tap(context.Background(), 0.5, 0.5, false, 2); err != nil {
		t.Fatal(err)
	}
	if n := len(d.sent); n < 2 || d.sent[n-1] != "click" || d.sent[n-2] != "click" {
		t.Fatalf("sent %v", d.sent)
	}
}

func TestChineseIsTypedAsPinyin(t *testing.T) {
	d := &fakeDevice{}
	p := newPointer(t, d, nil)
	for _, s := range []string{"hello world", "拿铁 3杯", "café 👋"} {
		p.Type(context.Background(), s)
	}
	if _, err := p.Type(context.Background(), "👋"); err == nil {
		t.Fatal("emoji-only text was typed")
	}
	want := []string{"type hello", "key space", "type world", "type natie", "key space", "type 3bei", "type cafe"}
	if fmt.Sprint(d.sent) != fmt.Sprint(want) {
		t.Fatalf("sent %q", d.sent)
	}
}

func TestFreshCalibrationHasNoDriftAndSurvivesARestart(t *testing.T) {
	file := filepath.Join(t.TempDir(), "cal.json")
	p := New(&fakeDevice{}, nil, file)
	if p.Calibrated() {
		t.Fatal("calibrated without a file")
	}
	p.FinishCalibration(0.5, 0.25)
	if p.Drift() != 0 {
		t.Fatal("drift")
	}
	again := New(&fakeDevice{}, nil, file)
	if *again.Span != (Span{400, 800}) {
		t.Fatalf("span %+v", again.Span)
	}
}

func TestHomeSwipeIsAFlickFromTheHomeBar(t *testing.T) {
	d := &fakeDevice{}
	p := newPointer(t, d, nil)
	p.FinishCalibration(0.5, 0.25)
	if err := p.HomeSwipe(context.Background()); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(d.sent, ",")
	if !strings.Contains(joined, "move 0 400,press,move 0 -680 40,release") {
		t.Fatalf("sent %v", d.sent)
	}
}

// With video the flick presses only once the pointer is seen pinned to the bottom edge: pressed anywhere else
// it is a tap (it opened WhatsApp from the dock, 2026-11-12).
func TestHomeSwipePressesOnlyOnTheBottomEdge(t *testing.T) {
	d := &fakeDevice{maxX: 410, maxY: 870}
	p := newPointer(t, d, &fakeScreen{d: d})
	p.FinishCalibration(0.5, 0.25)
	if err := p.HomeSwipe(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(d.sent, ","), "press") {
		t.Fatalf("sent %v", d.sent)
	}

	d = &fakeDevice{} // a pointer much slower than calibrated stops mid-screen
	p = newPointer(t, d, &fakeScreen{d: d, w: 410, h: 3000})
	p.FinishCalibration(0.5, 0.25)
	if err := p.HomeSwipe(context.Background()); err == nil || strings.Contains(strings.Join(d.sent, ","), "press") {
		t.Fatalf("err %v sent %v", err, d.sent)
	}
}

// A picture that lags the phone shows no pointer at first: the flick looks again and presses once it is seen on
// the edge (2026-11-14), and still presses nothing when it never is.
func TestHomeSwipeLooksAgainWhenThePictureLags(t *testing.T) {
	d := &fakeDevice{maxX: 410, maxY: 870}
	p := newPointer(t, d, &laggyScreen{fakeScreen: fakeScreen{d: d}, stale: 4})
	p.FinishCalibration(0.5, 0.25)
	if err := p.HomeSwipe(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(d.sent, ","), "press") {
		t.Fatalf("sent %v", d.sent)
	}

	d = &fakeDevice{maxX: 410, maxY: 870}
	p = newPointer(t, d, &laggyScreen{fakeScreen: fakeScreen{d: d}, stale: 1000})
	p.FinishCalibration(0.5, 0.25)
	if err := p.HomeSwipe(context.Background()); err == nil || strings.Contains(strings.Join(d.sent, ","), "press") {
		t.Fatalf("err %v sent %v", err, d.sent)
	}
}

// laggyScreen shows no pointer in its first `stale` frames, like a picture still catching up with the phone.
type laggyScreen struct {
	fakeScreen
	stale int
}

func (s *laggyScreen) Frame() image.Image {
	if s.stale > 0 {
		s.stale--
		return frame(-100, -100)
	}
	return s.fakeScreen.Frame()
}

func (s *laggyScreen) Fresh(context.Context, time.Time, time.Duration) image.Image { return s.Frame() }

func TestGoHomeFallsBackWithoutTheHomeKey(t *testing.T) {
	d := &fakeDevice{fail: map[string]bool{"homescreen": true}}
	p := newPointer(t, d, nil)
	p.FinishCalibration(0.5, 0.25)
	if err := p.GoHome(context.Background()); err != nil {
		t.Fatal(err)
	}
	if d.sent[0] != "homescreen" || d.sent[1] != "key cmd+h" || d.sent[len(d.sent)-1] != "release" {
		t.Fatalf("sent %v", d.sent)
	}
}

func TestLongPressHoldsThenReleases(t *testing.T) {
	d := &fakeDevice{}
	p := newPointer(t, d, nil)
	p.FinishCalibration(0.5, 0.25)
	var held time.Duration
	p.Sleep = func(_ context.Context, dd time.Duration) error { held = dd; return nil }
	p.LongPress(context.Background(), 0.25, 0.5, 2*time.Second)
	if held != 2*time.Second || fmt.Sprint(d.sent[len(d.sent)-4:]) != "[move 100 0 move 0 400 press release]" {
		t.Fatalf("sent %v held %v", d.sent, held)
	}
}

func TestFixLearnsAnOffset(t *testing.T) {
	p := newPointer(t, &fakeDevice{}, nil)
	p.FinishCalibration(0.5, 0.25)
	for _, aimed := range []float64{0.2, 0.5, 0.8} {
		p.remember("x", [2]float64{aimed, aimed + 0.02}) // always lands 2% right
	}
	if got := p.Fix(0.5, "x"); math.Abs(got-0.48) > 1e-9 {
		t.Fatalf("fix %v", got)
	}
	if math.Abs(p.Drift()-0.02) > 1e-9 {
		t.Fatalf("drift %v", p.Drift())
	}
}

// fakeScreen draws the fake device's pointer, with true span 410 x 870 (or w x h) and an offset.
type fakeScreen struct {
	d    *fakeDevice
	w, h float64
}

func (s *fakeScreen) Live() bool { return true }

func (s *fakeScreen) Frame() image.Image {
	s.d.mu.Lock()
	x, y := s.d.x, s.d.y
	s.d.mu.Unlock()
	sw, sh := s.w, s.h
	if sw == 0 {
		sw, sh = 410, 870
	}
	fx, fy := min(0.01+float64(x)/sw, 0.995), min(0.005+float64(y)/sh, 0.995)
	return frame(fx*W, fy*H)
}

func (s *fakeScreen) Settled(context.Context, time.Duration, time.Duration, time.Duration) {}

func (s *fakeScreen) Fresh(_ context.Context, since time.Time, _ time.Duration) image.Image {
	return s.Frame()
}

func TestCalibrationFindsTheSpanFromTheScreen(t *testing.T) {
	d := &fakeDevice{}
	p := newPointer(t, d, &fakeScreen{d: d})
	if err := p.Calibrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if math.Abs(float64(p.Span.W-410)) > 12 || math.Abs(float64(p.Span.H-870)) > 25 {
		t.Fatalf("span %+v", p.Span)
	}
	// the stops' offset is learned too: a tap at the middle lands there
	p.Point(context.Background(), 0.5, 0.5, false)
	fx, fy := 0.01+float64(d.x)/410, 0.005+float64(d.y)/870
	if math.Abs(fx-0.5) > 0.01 || math.Abs(fy-0.5) > 0.01 {
		t.Fatalf("landed at %.3f, %.3f", fx, fy)
	}
}

// A fast pointer (Tracking Speed up) crosses the screen in fewer units than the first stop: calibration shortens
// its stops instead of failing (seen on an iPhone, 2026-11-12).
func TestCalibrationShortensItsStopsForAFastPointer(t *testing.T) {
	d := &fakeDevice{maxX: 120, maxY: 260}
	p := newPointer(t, d, &fakeScreen{d, 120, 260})
	if err := p.Calibrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if math.Abs(float64(p.Span.W-120)) > 8 || math.Abs(float64(p.Span.H-260)) > 16 {
		t.Fatalf("span %+v", p.Span)
	}
}

// A slow pointer hardly leaves the corner at the first stops: calibration goes further.
func TestCalibrationLengthensItsStopsForASlowPointer(t *testing.T) {
	d := &fakeDevice{maxX: 1800, maxY: 4000}
	p := newPointer(t, d, &fakeScreen{d, 1800, 4000})
	if err := p.Calibrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if math.Abs(float64(p.Span.W-1800)) > 90 || math.Abs(float64(p.Span.H-4000)) > 200 {
		t.Fatalf("span %+v", p.Span)
	}
}

func TestCalibrationFailsWhenThePointerIsInvisible(t *testing.T) {
	d := &fakeDevice{}
	p := newPointer(t, d, &blankScreen{})
	if err := p.Calibrate(context.Background()); err != ErrNotFound {
		t.Fatalf("err %v", err)
	}
}

type blankScreen struct{}

func (blankScreen) Live() bool                                                           { return true }
func (blankScreen) Frame() image.Image                                                   { return frame(-100, -100) }
func (blankScreen) Settled(context.Context, time.Duration, time.Duration, time.Duration) {}
func (blankScreen) Fresh(context.Context, time.Time, time.Duration) image.Image {
	return frame(-100, -100)
}

// Vision, ported from VisionTests.swift.

const W, H = 498.0, 1080.0

// frame is a synthetic home screen: gradient background and a pointer dot at px, py.
func frame(px, py float64) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, int(W), int(H)))
	for y := 0; y < int(H); y++ {
		for x := 0; x < int(W); x++ {
			c := color.RGBA{40, uint8(60 + y/10), 120, 255}
			if (float64(x)-px)*(float64(x)-px)+(float64(y)-py)*(float64(y)-py) <= 12.5*12.5 {
				c = color.RGBA{200, 200, 200, 255}
			}
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

func TestPointerIsFoundFromANudge(t *testing.T) {
	pt, ok := Locate(frame(262, 856), frame(274, 856), Point{0.5, 0.8}, 12/W, Point{0.12, 0.08})
	if !ok || math.Abs(pt.X*W-262) > 3 || math.Abs(pt.Y*H-856) > 3 {
		t.Fatalf("found %v %+v", ok, pt)
	}
}

func TestPointerNotFoundWhenNothingMoved(t *testing.T) {
	f := frame(262, 856)
	if _, ok := Locate(f, f, Point{0.5, 0.8}, 12/W, Point{0.12, 0.08}); ok {
		t.Fatal("found a pointer in two equal frames")
	}
}

func TestPointerFarFromTheTargetIsFoundWithAFullScreenSearch(t *testing.T) {
	before, after := frame(354, 136), frame(366, 136)
	if _, ok := Locate(before, after, Point{0.9, 0.1}, 12/W, Point{0.12, 0.08}); ok {
		t.Fatal("found outside the window")
	}
	pt, ok := Locate(before, after, Point{0.5, 0.5}, 12/W, Point{0.5, 0.5})
	if !ok || math.Abs(pt.X*W-354) > 3 || math.Abs(pt.Y*H-136) > 3 {
		t.Fatalf("found %v %+v", ok, pt)
	}
}

// grayFrame: a gray background (lum `bg`), the pointer a disc `lift` brighter at px, py, and a soft cloud band
// `cloud` brighter from y cy down.
func grayFrame(px, py float64, bg, lift, cloud uint8, cy int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, int(W), int(H)))
	for y := 0; y < int(H); y++ {
		for x := 0; x < int(W); x++ {
			l := bg
			if y >= cy {
				l += cloud
			}
			if (float64(x)-px)*(float64(x)-px)+(float64(y)-py)*(float64(y)-py) <= 12.5*12.5 {
				l += lift
			}
			img.SetRGBA(x, y, color.RGBA{l, l, l, 255})
		}
	}
	return img
}

// Over Weather's gray clouds the see-through pointer is only ~25 levels brighter than the background, and only its
// top half shows on the bottom edge: Locate misses it, LocateFaint near the edge finds it (2026-11-14). A cloud
// drifting by changes a wide band, which is not taken for the pointer.
func TestFaintPointerOnTheBottomEdge(t *testing.T) {
	before, after := grayFrame(249, H-2, 110, 22, 0, 0), grayFrame(261, H-2, 110, 22, 0, 0)
	if _, ok := Locate(before, after, Point{0.5, 1}, 12/W, Point{0.12, 0.08}); ok {
		t.Fatal("Locate found a faint pointer; this test no longer shows the case")
	}
	pt, ok := LocateFaint(before, after, Point{0.5, 1}, 12/W, Point{0.12, 0.08}, FaintChange)
	if !ok || math.Abs(pt.X*W-249) > 4 || pt.Y < BottomEdge {
		t.Fatalf("faint: %v at (%.0f, %.3f)", ok, pt.X*W, pt.Y)
	}
	cloud := grayFrame(249, H-2, 110, 0, 18, int(H)-60) // no pointer, a cloud moving into the bottom
	if pt, ok := LocateFaint(grayFrame(249, H-2, 110, 0, 0, 0), cloud, Point{0.5, 1}, 12/W, Point{0.12, 0.08}, FaintChange); ok {
		t.Fatalf("took a cloud for the pointer at (%.0f, %.3f)", pt.X*W, pt.Y)
	}
}
