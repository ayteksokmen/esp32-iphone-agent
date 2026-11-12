// Package pointer turns screen positions into the board's relative mouse moves. Port of the iOS app's
// Pointer.swift (itself a port of phone.py).
//
// The pointer is relative, so we track where it is (in mouse units) and move from there; every few gestures it
// is slammed into the top-left corner (`home`) again to cancel drift. Coordinates are fractions of the screen
// (0..1, origin top-left), so they work for any frame size.
package pointer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"math"
	"os"
	"strings"
	"time"
)

const (
	CalibrationMove = 200                    // mouse units moved diagonally for a one-point calibration
	RehomeEvery     = 10                     // gestures between re-homing
	Hold            = 250 * time.Millisecond // rest before lifting, so iOS adds no momentum
	TapGap          = 150 * time.Millisecond // stay still after a click, so iOS sees a finished tap
	Nudge           = 12                     // mouse units the pointer is wiggled by to find it on screen
	AimTolerance    = 0.008                  // screen fraction; a bigger miss is corrected before pressing
	MaxMiss         = 0.06                   // bigger misses are still corrected, but not learned from
	SettleLag       = 300 * time.Millisecond // after a key, frames captured sooner still show the old screen
	ChangeWait      = 5 * time.Second        // how long GoHome watches for the screen to change
	// FreshWait: how long a wiggle waits for a frame captured after it. Over BLE, video yields to commands, and a
	// frame often took over a second: calibration with 1 s saw no pointer (2026-11-06).
	FreshWait = 3 * time.Second
	// MoveLag: a frame captured sooner after a nudge may not show it yet. On a still screen the phone sends frames
	// only once the pointer moves, so the next frame showed it; with a timer counting down on screen frames come
	// every second, the next one still had the pointer where it was, and Home never found it (2026-11-12).
	MoveLag = 250 * time.Millisecond
	// BottomEdge: the home-bar flick presses only with the pointer seen this low (see HomeSwipe).
	BottomEdge = 0.96
	// FlickSpeed: mouse units per report for the home-bar flick (see HomeSwipe).
	FlickSpeed = 40
	// HomeRest: iOS smooths pointer speed over the last reports, so right after `home` (fast reports) the first units
	// of the next move went several times further: 24 units crossed 31% of the screen, the next 116 only 60%
	// (2026-11-12). Resting after `home` lets that speed die down; phone.py's home rested 80 ms too.
	HomeRest = 250 * time.Millisecond
	// GlideWait: with Pointer Animations on (the iOS default) the pointer glides to a new spot for up to ~1 s, and a
	// click sent at once was lost: the pointer sat on the Wi-Fi row and nothing opened (2026-11-12). Before pressing,
	// wait for the picture to settle (or this long without video), so nobody has to turn the animations off.
	GlideWait = 900 * time.Millisecond
)

// CalibrationStops are the distances at which calibration finds the pointer, at scale 1. How far a mouse unit goes
// depends on the phone and its Tracking Speed: on one iPhone the first stop already pinned the pointer to the right
// edge (2026-11-12). So calibration rescales the stops until both land on screen; nobody has to touch the setting.
var CalibrationStops = []int{150, 350}

const (
	calibrationTries = 5
	minStop          = 24 // units; shorter stops measure too little movement to trust
	maxStop          = 4000
)

// Device sends one board command; an "err" reply is an error. Links counts (re)connections.
type Device interface {
	Send(ctx context.Context, line string) error
	Links() int
}

// Screen is the mirrored screen.
type Screen interface {
	Live() bool
	Frame() image.Image
	// Settled waits until the image stopped changing for `quiet` (at least `least`, at most `most`).
	Settled(ctx context.Context, quiet, least, most time.Duration)
	// Fresh returns the first frame captured after `since`, nil if none within timeout.
	Fresh(ctx context.Context, since time.Time, timeout time.Duration) image.Image
}

type Span struct{ W, H int }

type saved struct {
	W       int                    `json:"w"`
	H       int                    `json:"h"`
	Seen    map[string][][]float64 `json:"seen,omitempty"`
	Quality *float64               `json:"quality,omitempty"`
	Aspect  *float64               `json:"aspect,omitempty"`
	Moves   string                 `json:"moves,omitempty"` // "axis": measured with moveXY's moves; older ones are redone
}

// MovesAxis marks a calibration measured with axis-by-axis moves.
const MovesAxis = "axis"

type Pointer struct {
	dev    Device
	screen Screen
	file   string
	Span   *Span // mouse units needed to cross the full screen width / height
	pos    *[2]int
	moves  int
	link   int
	// where the pointer really landed (seen on screen) for the fraction aimed at, per axis; a line fit through
	// them corrects every later move, so taps get better as the agent works
	seen    map[string][][2]float64
	Quality *float64 // pointer error at the last check, fraction of the screen
	Aspect  *float64 // frame width / height when calibrated
	Log     func(string)
	Sleep   func(context.Context, time.Duration) error
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// New loads a saved calibration from file, if any. screen may be nil.
func New(dev Device, screen Screen, file string) *Pointer {
	p := &Pointer{dev: dev, screen: screen, file: file, seen: map[string][][2]float64{"x": nil, "y": nil},
		Log: func(string) {}, Sleep: sleep}
	raw, err := os.ReadFile(file)
	var s saved
	if err != nil || json.Unmarshal(raw, &s) != nil || s.W <= 0 || s.H <= 0 || s.Moves != MovesAxis {
		return p
	}
	p.Span = &Span{s.W, s.H}
	p.Quality, p.Aspect = s.Quality, s.Aspect
	for _, axis := range []string{"x", "y"} {
		for _, v := range s.Seen[axis] {
			if len(v) == 2 {
				p.remember(axis, [2]float64{v[0], v[1]})
			}
		}
	}
	return p
}

func (p *Pointer) Calibrated() bool { return p.Span != nil }

// Forget makes the position unknown (someone else moved the pointer): the next gesture homes first.
func (p *Pointer) Forget() { p.pos = nil }

func (p *Pointer) remember(axis string, sample [2]float64) {
	s := append(p.seen[axis], sample)
	if len(s) > 12 { // deque(maxlen=12)
		s = s[len(s)-12:]
	}
	p.seen[axis] = s
}

// send runs one command; after a failed one the pointer position is unknown (the next gesture homes first).
func (p *Pointer) send(ctx context.Context, format string, a ...any) error {
	err := p.dev.Send(ctx, fmt.Sprintf(format, a...))
	if err != nil {
		p.pos = nil
	}
	return err
}

func (p *Pointer) screenAspect() float64 {
	if p.screen != nil {
		if f := p.screen.Frame(); f != nil && f.Bounds().Dy() > 0 {
			return float64(f.Bounds().Dx()) / float64(f.Bounds().Dy())
		}
	}
	return 9 / 19.5
}

// ErrNotFound: calibration could not see the pointer on screen.
var ErrNotFound = errors.New("calibration: pointer not found on screen (is the mouse pointer visible, AssistiveTouch on, Screen Mirroring on?)")

// Calibrate finds the pointer on screen at two known distances from the corner and fits the mapping from both. A
// pointer that runs into the far edge makes it retry with shorter stops, one that barely leaves the corner with
// longer ones.
func (p *Pointer) Calibrate(ctx context.Context) error {
	scale, grew := 1.0, false
	for try := 0; try < calibrationTries; try++ {
		stops := make([]int, len(CalibrationStops))
		for i, u := range CalibrationStops {
			stops[i] = min(max(int(math.RoundToEven(float64(u)*scale)), minStop*(i+1)), maxStop)
		}
		// the nudge shrinks with the stops: a fast pointer moved by the full Nudge shifts too far for Locate's
		// correction, and the same spot was found at 0.49 and 0.62 (2026-11-12)
		nudge := max(4, int(math.RoundToEven(Nudge*min(scale, 1))))
		found, seen, err := p.calibrationRun(ctx, stops, nudge)
		if err != nil {
			return err
		}
		p.Log(fmt.Sprintf("calibration: stops %v: found %v, then seen %d (1 near edge, 2 far edge)", stops, found, seen))
		if len(found) == 2 {
			a, b := found[0], found[1]
			if b.x > a.x && b.y > a.y {
				s := Span{int(math.RoundToEven(float64(b.units-a.units) / (b.x - a.x))), int(math.RoundToEven(float64(b.units-a.units) / (b.y - a.y)))}
				// Two sightings of one spot (a stale frame) give a huge span, and the check move after it then took
				// the board 20 s: its watchdog reset it (2026-11-06). A real span is a few hundred to a few thousand.
				if s.W >= 100 && s.H >= 100 && s.W <= 5000 && s.H <= 5000 {
					return p.setCalibration(s, found)
				}
			}
		}
		next := scale
		switch seen {
		case seenFar: // ran into the right or bottom edge: the pointer is fast, go less far
			next = scale * 0.4
		case seenNear: // still in the corner: the pointer is slow, go further
			next = scale * 2.5
		case seenNone:
			// Not seen at all at the first stop: a slow pointer may still be under the status bar, where nothing is
			// searched. Going further once tells that apart from a pointer that is not there (which costs a run).
			if len(found) == 0 && !grew {
				next, grew = scale*4, true
			}
		}
		if next == scale || stops[0] == minStop && next < scale || stops[len(stops)-1] == maxStop && next > scale {
			break
		}
		p.Log(fmt.Sprintf("calibration: stops %v did not fit the screen, trying again at %.2fx", stops, next))
		scale = next
	}
	return ErrNotFound
}

type calibrationStop struct {
	units int
	x, y  float64
}

// where findPointer saw the pointer when it was not usable
const (
	seenNone = iota
	seenNear // pinned to the top or left edge
	seenFar  // pinned to the right or bottom edge
)

// calibrationRun homes, then moves out to each stop and finds the pointer there. It stops at the first stop where
// the pointer is not usable, and says where it was seen then.
func (p *Pointer) calibrationRun(ctx context.Context, stops []int, nudge int) ([]calibrationStop, int, error) {
	p.pos = nil
	if err := p.home(ctx); err != nil {
		return nil, seenNone, err
	}
	var found []calibrationStop
	for _, units := range stops {
		step := units
		if len(found) > 0 {
			step = units - found[len(found)-1].units
		}
		if err := p.moveXY(ctx, step, step); err != nil {
			return nil, seenNone, err
		}
		if p.screen == nil || !p.screen.Live() {
			return found, seenNone, nil
		}
		pt, seen, err := p.findPointer(ctx, nudge)
		if err == nil && pt == nil && seen == seenNone { // once more: a frame lost or a late one
			pt, seen, err = p.findPointer(ctx, nudge)
		}
		if err != nil {
			return nil, seenNone, err
		}
		if pt == nil {
			return found, seen, nil
		}
		found = append(found, calibrationStop{units, pt.X, pt.Y})
	}
	return found, seenNone, nil
}

func (p *Pointer) setCalibration(s Span, found []calibrationStop) error {
	p.Span = &s
	aspect := p.screenAspect()
	p.Quality, p.Aspect = nil, &aspect
	p.seen = map[string][][2]float64{"x": nil, "y": nil}
	for _, f := range found { // where each stop landed, so moves are corrected for the offset too
		p.remember("x", [2]float64{float64(f.units) / float64(s.W), f.x})
		p.remember("y", [2]float64{float64(f.units) / float64(s.H), f.y})
	}
	p.Log(fmt.Sprintf("calibration: span %d x %d (stops %d, %d)", s.W, s.H, found[0].units, found[1].units))
	return p.save()
}

const (
	edgeBand = 0.03 // a pointer this close to the screen edge may be pinned there: its position says nothing
	// Locate skips the status bar (top 5%), so a pointer just below it is seen only in part, and too low: 0.052 for a
	// pointer at 0.042 in a test. Such a stop is treated as still in the corner.
	topBand = 0.08
)

// findPointer wiggles the pointer and finds it anywhere on screen (the span may still be unknown). It returns nil
// when the pointer was not seen, or was seen at an edge (and which one).
func (p *Pointer) findPointer(ctx context.Context, size int) (*Point, int, error) {
	// iOS glides the pointer to a new spot over ~1 s, and the first frames arrive ~0.2 s late: a shorter wait took
	// a frame from before the stop's move as "before" (2026-11-06).
	p.screen.Settled(ctx, 400*time.Millisecond, 800*time.Millisecond, 3*time.Second)
	before := p.screen.Frame()
	// A pointer pinned to the right edge cannot move right and would not show up: wiggle it left and back instead.
	// It shows up then, so calibration knows it went too far rather than that the pointer is missing.
	for _, nudge := range []int{size, -size} {
		after, back, err := p.wiggle(ctx, nudge)
		if err != nil {
			return nil, seenNone, err
		}
		pt, ok := LocateBack(before, after, back, Point{0.5, 0.5}, math.Copysign(0.025, float64(nudge)), Point{0.5, 0.5})
		p.Log(fmt.Sprintf("calibration: nudge %+d: found %v at %.3f, %.3f (after %v, back %v)", nudge, ok, pt.X, pt.Y, after != nil, back != nil))
		if !ok {
			before = p.screen.Frame()
			continue
		}
		switch {
		case nudge < 0 && pt.X >= 0.8: // it could not move right: pinned to the right edge
			return nil, seenFar, nil
		case pt.X >= 1-edgeBand || pt.Y >= 1-edgeBand:
			return nil, seenFar, nil
		case pt.X <= edgeBand || pt.Y <= topBand:
			return nil, seenNear, nil
		}
		return &pt, seenNone, nil
	}
	return nil, seenNone, nil
}

// wiggle moves the pointer by nudge units sideways and back, and returns a frame from each end.
func (p *Pointer) wiggle(ctx context.Context, nudge int) (after, back image.Image, err error) {
	sent := time.Now()
	if err := p.send(ctx, "move %d 0", nudge); err != nil {
		return nil, nil, err
	}
	after = p.screen.Fresh(ctx, sent.Add(MoveLag), FreshWait)
	if after != nil {
		p.screen.Settled(ctx, 150*time.Millisecond, 100*time.Millisecond, 600*time.Millisecond)
		after = p.screen.Frame()
	}
	sent = time.Now()
	if err := p.send(ctx, "move %d 0", -nudge); err != nil {
		return nil, nil, err
	}
	if after != nil && p.screen.Fresh(ctx, sent.Add(MoveLag), FreshWait) != nil {
		p.screen.Settled(ctx, 150*time.Millisecond, 100*time.Millisecond, 600*time.Millisecond)
		back = p.screen.Frame()
	}
	return after, back, nil
}

// FinishCalibration: x, y is where the pointer ended up after `home` + `move 200 200` (one point).
func (p *Pointer) FinishCalibration(x, y float64) error {
	aspect := p.screenAspect()
	p.Quality, p.Aspect = nil, &aspect
	p.Span = &Span{int(math.RoundToEven(CalibrationMove / x)), int(math.RoundToEven(CalibrationMove / y))}
	p.seen = map[string][][2]float64{"x": nil, "y": nil}
	return p.save()
}

// save keeps the calibration plus the observed landings, so the correction survives a restart.
func (p *Pointer) save() error {
	if p.Span == nil || p.file == "" {
		return nil
	}
	s := saved{W: p.Span.W, H: p.Span.H, Seen: map[string][][]float64{}, Quality: p.Quality, Aspect: p.Aspect, Moves: MovesAxis}
	for axis, pts := range p.seen {
		s.Seen[axis] = [][]float64{}
		for _, v := range pts {
			s.Seen[axis] = append(s.Seen[axis], []float64{v[0], v[1]})
		}
	}
	raw, _ := json.Marshal(s)
	return os.WriteFile(p.file, raw, 0o600)
}

// ErrNotCalibrated: run Calibrate first.
var ErrNotCalibrated = errors.New("pointer not calibrated")

// Point moves the pointer to x, y. aim: find it on screen afterwards and fix a miss (slower).
func (p *Pointer) Point(ctx context.Context, x, y float64, aim bool) error {
	if !p.Calibrated() {
		return ErrNotCalibrated
	}
	if err := p.moveTo(ctx, p.dx(x), p.dy(y), true); err != nil {
		return err
	}
	if aim && p.screen != nil {
		return p.aim(ctx, x, y)
	}
	return nil
}

// aim is the closed loop: find the real pointer on screen, fix a miss, and learn the error for later moves.
func (p *Pointer) aim(ctx context.Context, x, y float64) error {
	found, ok, err := p.measure(ctx, x, y)
	if err != nil {
		return err
	}
	if !ok {
		p.Log(fmt.Sprintf("aim (%.3f, %.3f): pointer not found", x, y))
		return nil
	}
	if math.Max(math.Abs(found.X-x), math.Abs(found.Y-y)) <= MaxMiss { // learn from normal drift only
		p.remember("x", [2]float64{p.Fix(x, "x"), found.X})
		p.remember("y", [2]float64{p.Fix(y, "y"), found.Y})
		p.save()
	}
	if math.Abs(found.X-x) <= AimTolerance && math.Abs(found.Y-y) <= AimTolerance {
		return nil
	}
	mx := int(math.RoundToEven((x - found.X) * float64(p.Span.W)))
	my := int(math.RoundToEven((y - found.Y) * float64(p.Span.H)))
	if err := p.moveXY(ctx, mx, my); err != nil { // fix this miss right away
		return err
	}
	if p.pos != nil {
		p.pos = &[2]int{p.pos[0] + mx, p.pos[1] + my}
	}
	return nil
}

// measure wiggles the pointer and finds it on screen, near the target first, then anywhere.
func (p *Pointer) measure(ctx context.Context, x, y float64) (Point, bool, error) {
	if p.screen == nil || p.Span == nil {
		return Point{}, false, nil
	}
	nudge := Nudge
	if x >= 0.9 { // stay clear of the screen edge
		nudge = -Nudge
	}
	p.screen.Settled(ctx, 150*time.Millisecond, 100*time.Millisecond, 600*time.Millisecond) // pointer at rest
	before, sent := p.screen.Frame(), time.Now()
	if err := p.send(ctx, "move %d 0", nudge); err != nil {
		return Point{}, false, err
	}
	after := p.screen.Fresh(ctx, sent.Add(MoveLag), 1200*time.Millisecond)
	if after != nil {
		p.screen.Settled(ctx, 100*time.Millisecond, 50*time.Millisecond, 400*time.Millisecond)
		after = p.screen.Frame()
	}
	if err := p.send(ctx, "move %d 0", -nudge); err != nil {
		return Point{}, false, err
	}
	shift := float64(nudge) / float64(p.Span.W)
	if pt, ok := Locate(before, after, Point{x, y}, shift, Point{0.12, 0.08}); ok {
		return pt, true, nil
	}
	pt, ok := Locate(before, after, Point{0.5, 0.5}, shift, Point{0.5, 0.5})
	return pt, ok, nil
}

// Verify moves to a known point and measures the miss (fraction of the screen); ok=false: pointer not found.
func (p *Pointer) Verify(ctx context.Context, x, y float64) (float64, bool, error) {
	if err := p.Point(ctx, x, y, false); err != nil {
		return 0, false, err
	}
	found, ok, err := p.measure(ctx, x, y)
	if err != nil || !ok {
		return 0, false, err
	}
	e := math.Max(math.Abs(found.X-x), math.Abs(found.Y-y))
	p.Quality = &e
	return e, true, p.save()
}

// Drift is how far the learned correction has moved targets away from the plain calibration.
func (p *Pointer) Drift() float64 {
	d := 0.0
	for _, t := range []float64{0.2, 0.8} {
		for _, axis := range []string{"x", "y"} {
			d = math.Max(d, math.Abs(p.Fix(t, axis)-t))
		}
	}
	return d
}

// Fix is the fraction to aim at so the pointer lands on t, from the line through the observed landings.
func (p *Pointer) Fix(t float64, axis string) float64 {
	pts := p.seen[axis]
	if len(pts) < 2 {
		return t
	}
	n := float64(len(pts))
	mc, mf := 0.0, 0.0
	for _, v := range pts {
		mc += v[0] / n
		mf += v[1] / n
	}
	variance, cov := 0.0, 0.0
	for _, v := range pts {
		variance += (v[0] - mc) * (v[0] - mc)
		cov += (v[0] - mc) * (v[1] - mf)
	}
	a := 1.0 // landed = a*aimed + b
	if variance > 1e-4 {
		a = cov / variance
	}
	if a <= 0.85 || a >= 1.15 {
		a = 1
	}
	b := mf - a*mc
	if math.Abs(b) < 0.1 {
		return (t - b) / a
	}
	return t
}

func (p *Pointer) moveTo(ctx context.Context, tx, ty int, rehome bool) error {
	if l := p.dev.Links(); p.link != l { // new link: a dropped gesture may have left the button down
		if err := p.send(ctx, "release"); err != nil {
			return err
		}
		p.pos, p.link = nil, l
	}
	if p.pos == nil || rehome && p.moves >= RehomeEvery {
		if err := p.home(ctx); err != nil {
			return err
		}
		p.pos, p.moves = &[2]int{0, 0}, 0
	}
	x, y := p.pos[0], p.pos[1]
	p.pos = nil // unknown until the move succeeds
	if err := p.moveXY(ctx, tx-x, ty-y); err != nil {
		return err
	}
	p.pos, p.moves = &[2]int{tx, ty}, p.moves+1
	return nil
}

// moveXY moves the pointer along x, then along y. The board paces a move in reports of at most 10 units per axis,
// and iOS accelerates by a report's length: a diagonal report (10, 10) went 40% further per unit than (10, 0), so
// how far one axis went depended on the other, and taps after a diagonal calibration missed (2026-11-06).
// One axis at a time keeps every report the same length: the units per screen stay the same in any direction.
func (p *Pointer) moveXY(ctx context.Context, dx, dy int) error {
	if dx != 0 {
		if err := p.send(ctx, "move %d 0", dx); err != nil {
			return err
		}
	}
	if dy != 0 {
		return p.send(ctx, "move 0 %d", dy)
	}
	return nil
}

// home slams the pointer into the corner and rests (see HomeRest).
func (p *Pointer) home(ctx context.Context) error {
	if err := p.send(ctx, "home"); err != nil {
		return err
	}
	return p.Sleep(ctx, HomeRest)
}

// Reset lets go of the button and re-homes the pointer, whatever state a gesture left.
func (p *Pointer) Reset(ctx context.Context) error {
	p.pos = nil
	if err := p.send(ctx, "release"); err != nil {
		return err
	}
	if err := p.home(ctx); err != nil {
		return err
	}
	p.pos, p.moves = &[2]int{0, 0}, 0
	return nil
}

// Tap clicks count times (at least once) at x, y without moving in between: 2 is a double tap.
func (p *Pointer) Tap(ctx context.Context, x, y float64, aim bool, count int) error {
	if err := p.Point(ctx, x, y, aim); err != nil {
		return err
	}
	if err := p.glide(ctx); err != nil {
		return err
	}
	for i := 0; i < max(count, 1); i++ {
		if err := p.send(ctx, "click"); err != nil {
			return err
		}
	}
	return p.Sleep(ctx, TapGap)
}

// glide waits until the pointer has stopped where it was sent (see GlideWait).
func (p *Pointer) glide(ctx context.Context) error {
	if p.screen != nil && p.screen.Live() {
		p.screen.Settled(ctx, 250*time.Millisecond, 300*time.Millisecond, GlideWait+300*time.Millisecond)
		return ctx.Err()
	}
	return p.Sleep(ctx, GlideWait)
}

// LongPress presses at x, y for d without moving.
func (p *Pointer) LongPress(ctx context.Context, x, y float64, d time.Duration) error {
	if err := p.Point(ctx, x, y, false); err != nil {
		return err
	}
	if err := p.glide(ctx); err != nil {
		return err
	}
	if err := p.send(ctx, "press"); err != nil {
		return err
	}
	err := p.Sleep(ctx, d)
	if rerr := p.send(context.WithoutCancel(ctx), "release"); err == nil {
		err = rerr
	}
	return err
}

func (p *Pointer) Swipe(ctx context.Context, x1, y1, x2, y2 float64, hold bool) error {
	return p.Drag(ctx, []Point{{x1, y1}, {x2, y2}}, hold)
}

// Drag presses at the first point, moves through the rest, releases: one continuous stroke. hold rests at the
// end first, so lists and pickers stop exactly there; system gestures (home, back) need the flick.
func (p *Pointer) Drag(ctx context.Context, pts []Point, hold bool) error {
	if err := p.Point(ctx, pts[0].X, pts[0].Y, false); err != nil {
		return err
	}
	if err := p.glide(ctx); err != nil {
		return err
	}
	if err := p.send(ctx, "press"); err != nil {
		return err
	}
	err := func() error {
		for _, pt := range pts[1:] {
			if err := p.moveTo(ctx, p.dx(pt.X), p.dy(pt.Y), false); err != nil { // never home mid-drag
				return err
			}
		}
		if hold {
			return p.Sleep(ctx, Hold)
		}
		return nil
	}()
	if rerr := p.send(context.WithoutCancel(ctx), "release"); err == nil {
		err = rerr
	}
	return err
}

// Type types text as the board can (see Typeable).
func (p *Pointer) Type(ctx context.Context, text string) (string, error) {
	ascii := Typeable(text)
	if ascii == "" {
		return "", errors.New("nothing typeable in that text (the board types ASCII only)")
	}
	// Spaces go as their own key: inside a `type` line iOS dropped every one ("coffee near East Broadway" came
	// out "coffeenearEastBroadway" in Maps, 2026-10-21), while a separate space key always landed.
	for i, word := range strings.Split(ascii, " ") {
		if i > 0 {
			if err := p.Key(ctx, "space"); err != nil {
				return ascii, err
			}
		}
		if word == "" {
			continue
		}
		if err := p.send(ctx, "type %s", word); err != nil {
			return ascii, err
		}
	}
	return ascii, nil
}

func (p *Pointer) Key(ctx context.Context, combo string) error { return p.send(ctx, "key %s", combo) }

// GoHome goes to the Home Screen like the app's leaveApp: the consumer-control Home key, then Cmd+H, then the flick
// up from the home bar, each until the screen really changes. The board acknowledges a key iOS ignores (seen on
// an iPhone in a video app, 2026-10-21), so an "ok" alone proves nothing; without video the key's "ok" is all there is.
func (p *Pointer) GoHome(ctx context.Context) error {
	tries := []func() error{
		func() error { return p.send(ctx, "homescreen") },
		func() error { return p.Key(ctx, "cmd+h") },
		func() error {
			if !p.Calibrated() {
				if err := p.Calibrate(ctx); err != nil {
					return err
				}
			}
			return p.HomeSwipe(ctx)
		},
	}
	var last error
	for i, try := range tries {
		before, sent := p.frame(), time.Now()
		err := try()
		if err != nil && strings.HasPrefix(err.Error(), "busy") {
			return err
		}
		if err == nil && before == nil && i != 1 {
			return nil // no video to check against: trust the Home key, and after Cmd+H still flick (as before)
		}
		if err == nil && before != nil && p.changedSince(ctx, before, sent) {
			return nil
		}
		last = err
	}
	if last != nil {
		return last
	}
	return errors.New("the Home Screen did not come up (Home key, Cmd+H and the swipe up all left the screen as it was)")
}

func (p *Pointer) frame() image.Image {
	if p.screen == nil || !p.screen.Live() {
		return nil
	}
	return p.screen.Frame()
}

// changedSince: a frame captured at least SettleLag after `sent` differs from before in more than a sliver (the
// pointer alone is not it). Frames from the first SettleLag still show the old screen, and over BLE the picture
// lags the phone and the Home Screen animation starts late: one look after 1.5 s, and later a 3 s window, missed
// it and the next fallback ran on top (2026-10-21). So: board capture time >= sent+SettleLag, for up to ChangeWait.
func (p *Pointer) changedSince(ctx context.Context, before image.Image, sent time.Time) bool {
	// From now, not from sent: the swipe up first finds the pointer on the bottom edge, seconds that used up the
	// window, and Home from Control Center reached the Home Screen but reported it did not (2026-11-12).
	deadline := time.Now().Add(ChangeWait)
	for since := sent.Add(SettleLag); time.Now().Before(deadline); since = later(since, time.Now()) {
		f := p.screen.Fresh(ctx, since, time.Until(deadline))
		if f == nil {
			return false
		}
		if Changed(before, f, 0.05) {
			return true
		}
	}
	return false
}

func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// HomeSwipe is a real home-bar flick (a flick, not a rest: resting opens the app switcher).
//
// The press must land on the bottom edge: pressed anywhere else it is a tap. On 2026-11-12 the flick from Search
// pressed on the dock and opened WhatsApp, and the next one opened a chat in it. So the pointer is pushed past the
// bottom edge (iOS clamps it there whatever the estimate), and with video the flick only goes ahead once the
// pointer is seen on that edge; otherwise nothing is pressed.
func (p *Pointer) HomeSwipe(ctx context.Context) error {
	defer func() { p.pos = nil }() // edge clamping invalidates the tracked position
	if err := p.Point(ctx, 0.5, 0.9, false); err != nil {
		return err
	}
	if err := p.send(ctx, "move 0 %d", p.Span.H/2); err != nil { // pin it to the bottom edge
		return err
	}
	if err := p.glide(ctx); err != nil {
		return err
	}
	if p.screen != nil && p.screen.Live() {
		pt, ok, err := p.measure(ctx, 0.5, 1)
		if err != nil {
			return err
		}
		p.Log(fmt.Sprintf("home swipe: pointer found %v at %.3f, %.3f", ok, pt.X, pt.Y))
		if !ok || pt.Y < BottomEdge || math.Abs(pt.X-0.5) > 0.2 {
			return errors.New("did not swipe up: the pointer was not on the bottom edge, and a press elsewhere would tap the screen")
		}
	}
	if err := p.send(ctx, "press"); err != nil {
		return err
	}
	// Fast: at the usual 10 units a report the stroke took ~0.5 s, and on the Home Screen iOS read it as a swipe and
	// hold, opening the app switcher (2026-11-12). 40 a report (the board's most) is a flick.
	err := p.send(ctx, "move 0 %d %d", -int(0.85*float64(p.Span.H)), FlickSpeed)
	if rerr := p.send(context.WithoutCancel(ctx), "release"); err == nil {
		err = rerr
	}
	return err
}

func (p *Pointer) dx(x float64) int {
	return int(math.RoundToEven(math.Min(math.Max(p.Fix(x, "x"), 0), 1) * float64(p.Span.W)))
}

func (p *Pointer) dy(y float64) int {
	return int(math.RoundToEven(math.Min(math.Max(p.Fix(y, "y"), 0), 1) * float64(p.Span.H)))
}
