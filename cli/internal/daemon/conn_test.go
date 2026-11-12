package daemon

import (
	"context"
	"image"
	"strings"
	"testing"
	"time"
)

// noteApp takes s.mu while Server.conn / devices / closeAll hold s.mu and then
// take c.mu, so onStatus must release c.mu before invoking the app callback.
func TestOnStatusNotifiesAfterReleasingConnMu(t *testing.T) {
	c := newConn("0123456789ab", "test", "")

	called := false
	c.onApp = func() {
		called = true
		acquired := make(chan struct{})
		go func() {
			c.mu.Lock()
			c.mu.Unlock()
			close(acquired)
		}()
		select {
		case <-acquired:
		case <-time.After(3 * time.Second):
			// Return so onStatus can release c.mu; blocking here would hang the test.
			t.Error("app callback ran while c.mu was still held")
		}
	}

	c.onStatus([]byte(`{"owner":"app","busy":1}`))
	if !called {
		t.Fatal("app callback was not invoked for owner: app")
	}
}

// A screenshot never shows a screen long gone: only the board's latest frame passes, however old (2026-11-12).
func TestScreenshotRefusesAStaleFrame(t *testing.T) {
	c := newConn("0123456789ab", "test", "")
	c.onStatus([]byte(`{"ap":"streaming","up":600000,"vage":20000}`)) // still screen: last frame 20 s ago
	c.mu.Lock()
	c.frame = image.NewRGBA(image.Rect(0, 0, 4, 8))
	c.captured = time.UnixMilli(580000 + *c.offset)
	c.mu.Unlock()
	s := &Server{}
	if err := s.recent(context.Background(), c); err != nil {
		t.Fatalf("the latest frame: %v", err)
	}
	c.mu.Lock()
	c.captured = time.UnixMilli(450000 + *c.offset) // just arrived from a replayed GOP, but older than the latest
	c.mu.Unlock()
	if err := s.recent(context.Background(), c); err == nil || !strings.Contains(err.Error(), "older") {
		t.Fatalf("an older frame: %v", err)
	}
	c.onStatus([]byte(`{"ap":"streaming","up":700000,"vage":120000}`))
	if err := s.recent(context.Background(), c); err == nil || !strings.Contains(err.Error(), "no picture") {
		t.Fatalf("a silent phone: %v", err)
	}
	c.onStatus([]byte(`{"ap":"idle"}`))
	if err := s.recent(context.Background(), c); err != errMirroring {
		t.Fatalf("mirroring off: %v", err)
	}
}

// A replayed GOP minutes old must not move the clock: its frames would pass for fresh (2026-11-12).
func TestOldVideoDoesNotResetTheClock(t *testing.T) {
	c := newConn("0123456789ab", "test", "")
	now := time.Now()
	c.learnClock(now, 600_000, true)  // the board is up 10 min
	c.learnClock(now, 450_000, false) // a chunk the board got 150 s ago
	if want := now.UnixMilli() - 600_000; *c.offset != want {
		t.Fatalf("offset %d, want %d", *c.offset, want)
	}
	c.learnClock(now, 1_000, true) // the board restarted
	if want := now.UnixMilli() - 1_000; *c.offset != want {
		t.Fatalf("offset %d after a restart, want %d", *c.offset, want)
	}
}
