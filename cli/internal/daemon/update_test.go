package daemon

import (
	"bufio"
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/onehumanbeing/esp32-iphone-agent/cli/internal/proto"
)

const otaToken = "0123456789abcdef0123456789abcdef"

// fakeOTA serves ota.h's protocol on one end of a pipe. answer: what the board says after the image ("done").
type fakeOTA struct {
	mu     sync.Mutex
	addr   string
	head   string
	got    []byte
	answer string
	after  func() // the board reboots
}

func (f *fakeOTA) dial(_ context.Context, _, addr string) (net.Conn, error) {
	board, client := net.Pipe()
	f.mu.Lock()
	f.addr = addr
	f.mu.Unlock()
	go func() {
		defer board.Close()
		r := bufio.NewReader(board)
		head, _ := r.ReadString('\n')
		var tok, sum string
		var size int
		fmt.Sscanf(head, "ota %s %d %s", &tok, &size, &sum)
		f.mu.Lock()
		f.head = strings.TrimSpace(head)
		f.mu.Unlock()
		if tok != otaToken {
			io.WriteString(board, "err wrong token\n")
			return
		}
		io.WriteString(board, "ok\n")
		img := make([]byte, size)
		if _, err := io.ReadFull(r, img); err != nil {
			return
		}
		f.mu.Lock()
		f.got = img
		f.mu.Unlock()
		if s := md5.Sum(img); hex.EncodeToString(s[:]) != sum {
			io.WriteString(board, "err MD5 Check Failed\n")
			return
		}
		io.WriteString(board, f.answer+"\n")
		if f.after != nil {
			go f.after()
		}
	}()
	return client, nil
}

func fwImage(t *testing.T) string {
	img := make([]byte, 4096)
	img[0] = 0xE9
	for i := 1; i < len(img); i++ {
		img[i] = byte(i)
	}
	p := filepath.Join(t.TempDir(), "hid_bridge.ino.bin")
	if err := os.WriteFile(p, img, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// updateSetup: a paired board on Wi-Fi (fw 0.2.5, up a minute) that hands out otaToken.
func updateSetup(t *testing.T, handle func(string) string) (*Client, *proto.FakeBoard, *fakeOTA) {
	t.Setenv("ESP32AGENT_TRANSPORT", "ble") // the session itself stays on the fake BLE link
	b := fakeBoard()
	b.Handle = handle
	c, s := start(t, fakeTransport{b})
	f := &fakeOTA{answer: "done"}
	s.DialOTA = f.dial
	if r, _ := call(t, c, Request{Cmd: "pair"}); r.Status != "ok" {
		t.Fatalf("pair: %+v", r)
	}
	b.Status(`{"fw":"0.2.5","up":60000,"net":"sta","ip":"192.168.1.50"}`)
	time.Sleep(50 * time.Millisecond)
	return c, b, f
}

func tokenHandler(cmd string) string {
	if cmd == "ota" {
		return "ok " + otaToken
	}
	return "ok"
}

func TestUpdatePushesAndWaitsForTheNewBoot(t *testing.T) {
	c, b, f := updateSetup(t, tokenHandler)
	f.after = func() {
		time.Sleep(100 * time.Millisecond)
		b.Status(`{"fw":"0.2.6","up":900,"net":"sta","ip":"192.168.1.50"}`)
	}
	path := fwImage(t)
	r, data := call(t, c, Request{Cmd: "update", Text: path})
	if r.Status != "ok" {
		t.Fatalf("update: %+v", r)
	}
	if data["fw"] != "0.2.6" || data["from"] != "0.2.5" || data["message"] != "updated to 0.2.6" {
		t.Fatalf("data %+v", data)
	}
	want, _ := os.ReadFile(path)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.addr != "192.168.1.50:7300" || string(f.got) != string(want) {
		t.Fatalf("pushed %d bytes to %s", len(f.got), f.addr)
	}
	sum := md5.Sum(want)
	if f.head != fmt.Sprintf("ota %s 4096 %s", otaToken, hex.EncodeToString(sum[:])) {
		t.Fatalf("header %q", f.head)
	}
}

func TestUpdateStatusFromTheOldBootIsNotTheNewOne(t *testing.T) {
	old := UpdateBack
	UpdateBack = 700 * time.Millisecond
	t.Cleanup(func() { UpdateBack = old })
	c, b, f := updateSetup(t, tokenHandler)
	f.after = func() { b.Status(`{"fw":"0.2.5","up":61000,"net":"sta","ip":"192.168.1.50"}`) } // not rebooted
	r, _ := call(t, c, Request{Cmd: "update", Text: fwImage(t)})
	if r.Status != "error" || !strings.Contains(r.Error, "did not come back") {
		t.Fatalf("got %+v", r)
	}
}

func TestUpdateRefusals(t *testing.T) {
	for _, tc := range []struct {
		name, reply, answer, want string
		file                     func(t *testing.T) string
	}{
		{"app owns the board", "err busy: the app is using the board", "done", "busy: the app", fwImage},
		{"firmware without ota", "err unknown command", "done", "can't be updated from the CLI yet", fwImage},
		{"lean build", "err this build has no OTA (--lean)", "done", "no OTA", fwImage},
		{"board refuses the image", "ok " + otaToken, "err Flash Write Failed", "still runs 0.2.5: board: err Flash Write Failed", fwImage},
		{"not a firmware image", "ok " + otaToken, "done", "not an ESP32 firmware image", func(t *testing.T) string {
			p := filepath.Join(t.TempDir(), "x.bin")
			os.WriteFile(p, []byte(strings.Repeat("x", 4096)), 0o644)
			return p
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, b, f := updateSetup(t, func(cmd string) string {
				if cmd == "ota" {
					return tc.reply
				}
				return "ok"
			})
			f.answer = tc.answer
			r, _ := call(t, c, Request{Cmd: "update", Text: tc.file(t)})
			if r.Status != "error" || !strings.Contains(r.Error, tc.want) {
				t.Fatalf("got %+v, want error with %q", r, tc.want)
			}
			if tc.name == "not a firmware image" && strings.Contains(strings.Join(b.Commands, ","), "ota") {
				t.Fatal("asked the board for a token for a file it would refuse")
			}
		})
	}
}

func TestUpdateNeedsWiFi(t *testing.T) {
	c, b, f := updateSetup(t, tokenHandler)
	b.Status(`{"fw":"0.2.5","up":61000,"net":"none","ip":"0.0.0.0"}`)
	time.Sleep(50 * time.Millisecond)
	r, _ := call(t, c, Request{Cmd: "update", Text: fwImage(t)})
	if r.Status != "error" || !strings.Contains(r.Error, "not on Wi-Fi") {
		t.Fatalf("got %+v", r)
	}
	if f.addr != "" {
		t.Fatal("dialed the board anyway")
	}
}

