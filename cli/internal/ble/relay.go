package ble

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/onehumanbeing/esp32-iphone-agent/cli/internal/proto"
	"github.com/onehumanbeing/esp32-iphone-agent/cli/internal/relay"
)

// DialRelay joins the board boardID through the relay at addr ("host:port"; docs/cli-protocol.md, "Cloud relay").
// The link then works as the board's Wi-Fi link: open a session on it with HELLO.
func DialRelay(ctx context.Context, addr string, boardID []byte) (proto.Link, error) {
	var d net.Dialer
	nc, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	if tc, ok := nc.(*net.TCPConn); ok {
		tc.SetNoDelay(true)
	}
	if dl, ok := ctx.Deadline(); ok {
		nc.SetDeadline(dl)
	} else {
		nc.SetDeadline(time.Now().Add(10 * time.Second))
	}
	if err := relay.Record(nc, append([]byte{relay.Join}, boardID...)); err != nil {
		nc.Close()
		return nil, err
	}
	f, err := relay.ReadRecord(nc)
	if err != nil {
		nc.Close()
		return nil, err
	}
	nc.SetDeadline(time.Time{})
	switch {
	case len(f) == 1 && f[0] == relay.Joined:
		return newStreamLink(nc), nil
	case len(f) > 0 && f[0] == relay.Error:
		nc.Close()
		if string(f[1:]) == relay.ErrOffline.Error() {
			return nil, relay.ErrOffline
		}
		return nil, errors.New("relay: " + string(f[1:]))
	}
	nc.Close()
	return nil, errors.New("relay: unexpected answer")
}
