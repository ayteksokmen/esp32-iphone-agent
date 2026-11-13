package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/onehumanbeing/esp32-iphone-agent/cli/internal/pointer"
)

// The iPhone sends one keyframe per mirroring session. Once the board's GOP buffer is full, a decoder that loses its
// place (a Wi-Fi gap, the screen dimming, a daemon restart) can't start again until the next keyframe, and that only
// comes with a new session: the video stays frozen until someone restarts Screen Mirroring (2026-11-12, after about
// 16 minutes of mirroring). remirror does that restart on the phone itself, through the pointer, without a picture.

// Where Screen Mirroring sits in Control Center, as fractions of the screen: an iPhone 16 Pro's layout, measured
// 2026-11-12 on a 718x1560 frame. A phone whose Control Center is laid out differently needs other values.
var (
	mirrorTile = pointer.Point{X: 285.0 / 718, Y: 603.0 / 1560}  // the Screen Mirroring button: 587 over the Home Screen, 620 over an app (its name row above)
	mirrorStop = pointer.Point{X: 359.0 / 718, Y: 1029.0 / 1560} // "Stop Mirroring" in its panel
	mirrorRow  = pointer.Point{X: 215.0 / 718, Y: 689.0 / 1560}  // the first receiver in the panel: ESP32 Agent
	mirrorAway = pointer.Point{X: 215.0 / 718, Y: 1300.0 / 1560} // below the panel: a tap there closes it
)

// RemirrorEvery: at most one automatic restart in this long, so a phone where it does not work is not tapped
// around again and again.
const RemirrorEvery = 2 * time.Minute

// remirrorWait: pauses for Control Center and the mirroring panel to open or close.
var remirrorWait = 1500 * time.Millisecond

// remirror stops Screen Mirroring and starts it again: Control Center (drag down from the top-right edge) >
// Screen Mirroring > Stop Mirroring, which closes the panel back to Control Center; then Screen Mirroring >
// ESP32 Agent. The new session brings a keyframe. Each step is checked against the board's AirPlay state, not the
// picture. Then the panel and Control Center are swiped away, back to the screen the phone was on.
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
	log.Printf("%s: restarting Screen Mirroring", c.ID)
	tap := func(pt pointer.Point) error { return p.Tap(ctx, pt.X, pt.Y, false, 1) }
	pause := func(d time.Duration) error { return sleepCtx(ctx, d) }
	mirroring := func(st map[string]any) bool { return st["ap"] == "streaming" }

	// With a live picture (a restart asked for by hand), check that Control Center really opened before tapping
	// anything: on a fresh daemon the swipe once missed and the taps landed in an app (2026-11-12).
	st, _ := c.Status()
	before := c.Frame()
	inStep, _ := c.current() // a fresh daemon may still be catching up on minutes of old pictures
	live := before != nil && c.Live() && num(st["fz"]) == 0 && inStep
	if err := p.ControlCenterSwipe(ctx); err != nil {
		return err
	}
	if live { // a picture from after the drag, once Control Center has slid in
		f := c.Fresh(ctx, time.Now().Add(700*time.Millisecond), 3*time.Second)
		if f == nil || !pointer.Changed(before, f, 0.05) {
			return errors.New("Control Center did not open; nothing was tapped")
		}
	}
	if err := pause(remirrorWait); err != nil {
		return err
	}
	if mirroring(c.freshStatus()) {
		if err := tap(mirrorTile); err != nil {
			return err
		}
		if err := pause(remirrorWait); err != nil {
			return err
		}
		if err := tap(mirrorStop); err != nil {
			return err
		}
		if !s.waitStatus(ctx, c, 5*time.Second, func(st map[string]any) bool { return !mirroring(st) }) {
			return errors.New("Stop Mirroring did not stop it (is Control Center laid out differently?)")
		}
		if err := pause(remirrorWait); err != nil {
			return err
		}
	}
	if err := tap(mirrorTile); err != nil {
		return err
	}
	if err := pause(2 * time.Second); err != nil { // the receiver list fills in late
		return err
	}
	if err := tap(mirrorRow); err != nil {
		return err
	}
	back := func(st map[string]any) bool { return mirroring(st) && num(st["fz"]) == 0 }
	if !s.waitStatus(ctx, c, 20*time.Second, back) {
		return errors.New("mirroring did not come back within 20 s")
	}
	c.Fresh(ctx, time.Now(), 5*time.Second) // the new session's first picture
	// The panel stays open over Control Center. A tap outside it closes it; then the home-bar flick closes Control
	// Center, back to the screen the phone was on. A swipe up inside Control Center only turns to its next page
	// (2026-11-13).
	if err := tap(mirrorAway); err != nil {
		return err
	}
	if err := pause(remirrorWait); err != nil {
		return err
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
