package daemon

import (
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/onehumanbeing/esp32-iphone-agent/cli/internal/config"
)

// The keyboard layout given once is remembered for the board: later text is typed for Turkish Q without it, and a
// character with no key there is reported, not typed.
func TestTextKeyboardIsRemembered(t *testing.T) {
	b := fakeBoard()
	var mu sync.Mutex
	var typed []string
	b.Handle = func(cmd string) string {
		if strings.HasPrefix(cmd, "type ") {
			mu.Lock()
			typed = append(typed, strings.TrimPrefix(cmd, "type "))
			mu.Unlock()
		}
		return "ok"
	}
	c, s := start(t, fakeTransport{b})
	call(t, c, Request{Cmd: "pair"})
	b.Status(`{"up":5000,"ap":"idle","w":480,"h":1040}`)
	time.Sleep(50 * time.Millisecond)
	os.WriteFile(config.CalibrationPath(s.Dir, "246f28aa6ad5"), []byte(`{"w":400,"h":860,"moves":"axis"}`), 0o600)

	if r, _ := call(t, c, Request{Cmd: "text", Text: "gitmem", Keyboard: "TR"}); r.Status != "ok" {
		t.Fatalf("text: %+v", r)
	}
	r, data := call(t, c, Request{Cmd: "text", Text: "çiğ@"})
	if r.Status != "ok" || !strings.Contains(data["left_out"].(string), "@") {
		t.Fatalf("text: %+v %v", r, data)
	}
	if r, _ := call(t, c, Request{Cmd: "text", Text: "x", Keyboard: "de"}); r.Status != "error" {
		t.Fatalf("unknown keyboard: %+v", r)
	}
	call(t, c, Request{Cmd: "text", Text: "gitmem", Keyboard: "us"})
	mu.Lock()
	defer mu.Unlock()
	if want := []string{"g'tmem", ".'[", "gitmem"}; strings.Join(typed, ",") != strings.Join(want, ",") {
		t.Fatalf("typed %q, want %q", typed, want)
	}
}
