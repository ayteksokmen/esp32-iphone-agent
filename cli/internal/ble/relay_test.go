package ble

import (
	"bytes"
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/onehumanbeing/esp32-iphone-agent/cli/internal/relay"
)

// A board attached to the relay, a computer joining it: frames cross both ways, as on the board's Wi-Fi link.
func TestRelayJoinsBoardAndComputer(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go (&relay.Server{Logf: t.Logf}).Serve(l)
	id := []byte{0x24, 0x6f, 0x28, 0xaa, 0x6a, 0xd5}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := DialRelay(ctx, l.Addr().String(), id); !errors.Is(err, relay.ErrOffline) {
		t.Fatalf("no board yet: %v", err)
	}

	board, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer board.Close()
	relay.Record(board, append([]byte{relay.Attach}, id...))
	time.Sleep(100 * time.Millisecond)

	link, err := DialRelay(ctx, l.Addr().String(), id)
	if err != nil {
		t.Fatal(err)
	}
	defer link.Close()
	if err := link.Write([]byte{0x02, 1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	board.SetReadDeadline(time.Now().Add(3 * time.Second))
	got, err := relay.ReadRecord(board)
	if err != nil || !bytes.Equal(got, []byte{0x02, 1, 2, 3}) {
		t.Fatalf("board got %x, %v", got, err)
	}
	relay.Record(board, []byte{0x82, 9})
	select {
	case f := <-link.Recv():
		if !bytes.Equal(f, []byte{0x82, 9}) {
			t.Fatalf("computer got %x", f)
		}
	case <-ctx.Done():
		t.Fatal("nothing reached the computer")
	}

	// the board is taken: a second computer finds it offline until the board dials again
	if _, err := DialRelay(ctx, l.Addr().String(), id); !errors.Is(err, relay.ErrOffline) {
		t.Fatalf("second computer: %v", err)
	}
}
