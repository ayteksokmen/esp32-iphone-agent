package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/onehumanbeing/esp32-iphone-agent/cli/internal/pointer"
)

// The iPhone sends one keyframe per mirroring session. Once the board's GOP buffer is full, a decoder that loses its
// place (a Wi-Fi gap, the screen dimming, a daemon restart) can't start again until the next keyframe, and that only
// comes with a new session: the video stays frozen until someone restarts Screen Mirroring (2026-11-12, after about
// 16 minutes of mirroring). remirror does that restart on the phone itself, through the pointer, without a picture.

// mirrorLayout is where Screen Mirroring sits in this phone's Control Center, as fractions of the screen. Whenever
// the picture is live during a restart, it is measured (pointer.FindMirrorTile, FindMirrorPanel) and saved, so the
// next restart, maybe blind, taps where this phone has it. Users rearrange Control Center, and phones differ.
type mirrorLayout struct {
	// Tile: the Screen Mirroring button with Control Center opened over the Home Screen. Over an app, a row with the
	// app's name sits on top and everything moves down by AppRow.
	Tile   pointer.Point `json:"tile"`
	AppRow float64       `json:"app_row"`
	Stop   pointer.Point `json:"stop"` // "Stop Mirroring" in the panel
	Row    pointer.Point `json:"row"`  // this board's row in the panel
	// Learned: measured on this phone (not the defaults).
	Learned bool `json:"learned,omitempty"`
}

// defaultMirrorLayout: an iPhone 16 Pro's default Control Center, measured 2026-11-12 on a 718x1560 frame.
var defaultMirrorLayout = mirrorLayout{
	Tile:   pointer.Point{X: 285.0 / 718, Y: 587.0 / 1560},
	AppRow: 33.0 / 1560,
	Stop:   pointer.Point{X: 359.0 / 718, Y: 1029.0 / 1560},
	Row:    pointer.Point{X: 215.0 / 718, Y: 689.0 / 1560},
}

func mirrorLayoutPath(dir, id string) string { return filepath.Join(dir, "mirror-layout-"+id+".json") }

func loadMirrorLayout(dir, id string) mirrorLayout {
	l := defaultMirrorLayout
	if raw, err := os.ReadFile(mirrorLayoutPath(dir, id)); err == nil {
		var saved mirrorLayout
		if json.Unmarshal(raw, &saved) == nil && saved.Tile.Y > 0 && saved.Stop.Y > 0 && saved.Row.Y > 0 {
			l = saved
		}
	}
	return l
}

func (l mirrorLayout) save(dir, id string) error {
	raw, _ := json.Marshal(l)
	return os.WriteFile(mirrorLayoutPath(dir, id), raw, 0o600)
}

// blindTile is where to tap the button without a picture: halfway between its places over the Home Screen and over
// an app, inside the button either way (it is about 120 px across, the app row 33 px).
func (l mirrorLayout) blindTile() pointer.Point {
	return pointer.Point{X: l.Tile.X, Y: l.Tile.Y + l.AppRow/2}
}

// away is a tap below the panel, which closes it.
func (l mirrorLayout) away() pointer.Point {
	return pointer.Point{X: 0.3, Y: min(l.Stop.Y+0.17, 0.92)}
}

// learnTile measures the button in a frame of Control Center; false if it is not in it.
func (l *mirrorLayout) learnTile(f image.Image) (pointer.Point, bool) {
	t, ok := pointer.FindMirrorTile(f)
	if !ok {
		return t, false
	}
	l.Tile = t
	if pointer.HasAppRow(f) {
		l.Tile.Y -= l.AppRow
	}
	l.Learned = true
	return t, true
}

// learnPanel measures the panel in a frame; false if it is not in it.
func (l *mirrorLayout) learnPanel(f image.Image) bool {
	p, ok := pointer.FindMirrorPanel(f)
	if !ok {
		return false
	}
	l.Stop = p.Stop
	if p.Checked {
		l.Row = p.Row
	}
	l.Learned = true
	return true
}

// RemirrorEvery: at most one automatic restart in this long, so a phone where it does not work is not tapped
// around again and again.
const RemirrorEvery = 2 * time.Minute

// remirrorWait: pauses for Control Center and the mirroring panel to open or close.
var remirrorWait = 1500 * time.Millisecond

// remirror stops Screen Mirroring and starts it again: Control Center (drag down from the top-right edge) >
// Screen Mirroring > Stop Mirroring, which closes the panel back to Control Center; then Screen Mirroring >
// ESP32 Agent. The new session brings a keyframe. Each step is checked against the board's AirPlay state. Where
// there is a live picture, each target is found in it first, and nothing is tapped when it is not there; without
// one (the frozen case) the taps go where the last restart found them. Then the panel and Control Center are
// closed, back to the screen the phone was on.
func (s *Server) remirror(ctx context.Context, c *Conn, force bool) error {
	c.mu.Lock()
	if !force && time.Since(c.remirrored) < RemirrorEvery {
		c.mu.Unlock()
		return fmt.Errorf("tried %.0f s ago", time.Since(c.remirrored).Seconds())
	}
	c.remirrored = time.Now()
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.lastAction = time.Now()
		c.mu.Unlock()
	}()
	p := c.pointer(s.Dir)
	if !p.Calibrated() {
		return pointer.ErrNotCalibrated
	}
	started := time.Now()
	lay := loadMirrorLayout(s.Dir, c.ID)
	was := lay
	defer func() {
		if lay != was {
			if err := lay.save(s.Dir, c.ID); err != nil {
				log.Printf("%s: mirror layout not saved: %v", c.ID, err)
			}
		}
	}()
	log.Printf("%s: restarting Screen Mirroring (layout %s)", c.ID, map[bool]string{true: "learned", false: "default"}[lay.Learned])
	tap := func(pt pointer.Point) error { return p.Tap(ctx, pt.X, pt.Y, false, 1) }
	pause := func(d time.Duration) error { return sleepCtx(ctx, d) }
	mirroring := func(st map[string]any) bool { return st["ap"] == "streaming" }
	// until waits for a picture in which ok finds what it looks for. The picture lags the phone by a second or more,
	// and the first one after a gesture may still show the screen before it (2026-11-13).
	until := func(ok func(image.Image) bool) bool {
		deadline := time.Now().Add(6 * time.Second)
		for since := time.Now().Add(pointer.SettleLag); time.Now().Before(deadline); since = time.Now() {
			f := c.Fresh(ctx, since, time.Until(deadline))
			if f == nil {
				return false
			}
			if ok(f) {
				return true
			}
		}
		return false
	}

	// With a live picture (a restart asked for by hand, or mirroring off), every target is looked for first: on a
	// fresh daemon the swipe once missed and the taps landed in an app (2026-11-12). A fresh daemon may still be
	// catching up on minutes of old pictures, which is not live.
	st, _ := c.Status()
	before := c.Frame()
	inStep, _ := c.current()
	live := before != nil && c.Live() && num(st["fz"]) == 0 && inStep
	if err := p.ControlCenterSwipe(ctx); err != nil {
		return err
	}
	tile := lay.blindTile()
	if live { // mirroring, so its button is white with a blue icon
		if !until(func(f image.Image) bool { t, ok := lay.learnTile(f); tile = t; return ok }) {
			if f := c.Frame(); f != nil && pointer.Changed(before, f, 0.05) {
				p.HomeSwipe(ctx) // something opened: close it again
				return errors.New("no Screen Mirroring button in Control Center (removed from it?); closed it, nothing tapped")
			}
			return errors.New("Control Center did not open; nothing was tapped")
		}
		log.Printf("%s: Screen Mirroring found in Control Center at %.3f, %.3f", c.ID, tile.X, tile.Y)
	}
	if err := pause(remirrorWait); err != nil {
		return err
	}
	if mirroring(c.freshStatus()) {
		if err := tap(tile); err != nil {
			return err
		}
		if err := pause(remirrorWait); err != nil {
			return err
		}
		if live {
			if !until(lay.learnPanel) {
				p.HomeSwipe(ctx) // close Control Center again
				return errors.New("the Screen Mirroring panel did not open; closed Control Center without stopping")
			}
		}
		if err := tap(lay.Stop); err != nil {
			return err
		}
		if !s.waitStatus(ctx, c, 5*time.Second, func(st map[string]any) bool { return !mirroring(st) }) {
			return errors.New("Stop Mirroring did not stop it (is Control Center laid out differently?)")
		}
		if err := pause(remirrorWait); err != nil {
			return err
		}
	}
	if err := tap(tile); err != nil { // the panel closed back to Control Center, as it was
		return err
	}
	if err := pause(2 * time.Second); err != nil { // the receiver list fills in late
		return err
	}
	if err := tap(lay.Row); err != nil {
		return err
	}
	back := func(st map[string]any) bool { return mirroring(st) && num(st["fz"]) == 0 }
	if !s.waitStatus(ctx, c, 20*time.Second, back) {
		return errors.New("mirroring did not come back within 20 s")
	}
	// The new session's picture shows the panel with this board checked: measure it for next time.
	if f := c.Fresh(ctx, time.Now().Add(500*time.Millisecond), 5*time.Second); f != nil && !lay.learnPanel(f) {
		log.Printf("%s: mirroring is back, but the panel is not where expected", c.ID)
	}
	// The panel stays open over Control Center. A tap below it closes it; then the home-bar flick closes Control
	// Center, back to the screen the phone was on. A swipe up inside Control Center only turns to its next page
	// (2026-11-13).
	if err := tap(lay.away()); err != nil {
		return err
	}
	if err := pause(remirrorWait); err != nil {
		return err
	}
	if f := c.Fresh(ctx, time.Now(), 2*time.Second); f != nil {
		lay.learnTile(f) // Control Center again, the button white now that it mirrors
	}
	if err := p.HomeSwipe(ctx); err != nil {
		return err
	}
	// Let Control Center slide away first: the screenshot right after showed it still open (2026-11-13).
	c.Fresh(ctx, time.Now().Add(pointer.SettleLag), 2*time.Second)
	c.Settled(ctx, 400*time.Millisecond, 300*time.Millisecond, 3*time.Second)
	log.Printf("%s: Screen Mirroring restarted in %.0f s", c.ID, time.Since(started).Seconds())
	return nil
}

// freshStatus is the board's latest status, nil when it is older than 3 s.
func (c *Conn) freshStatus() map[string]any {
	st, age := c.Status()
	if age < 0 || age > 3*time.Second {
		return nil
	}
	return st
}

// waitStatus polls the board's status until ok, for up to d.
func (s *Server) waitStatus(ctx context.Context, c *Conn, d time.Duration, ok func(map[string]any) bool) bool {
	for end := time.Now().Add(d); time.Now().Before(end); {
		if st := c.freshStatus(); st != nil && ok(st) {
			return true
		}
		if sleepCtx(ctx, 250*time.Millisecond) != nil {
			return false
		}
	}
	return false
}

// unfreeze restarts mirroring when the board says the video froze; nil when it is not frozen (any more).
func (s *Server) unfreeze(ctx context.Context, c *Conn) error {
	if st, _ := c.Status(); num(st["fz"]) != 1 {
		return nil
	}
	if err := s.remirror(ctx, c, false); err != nil {
		log.Printf("%s: automatic mirroring restart failed: %v", c.ID, err)
		return fmt.Errorf("%w (restarting it automatically failed: %v)", errFrozen, err)
	}
	if st, _ := c.Status(); num(st["fz"]) == 1 {
		return errFrozen
	}
	return nil
}
