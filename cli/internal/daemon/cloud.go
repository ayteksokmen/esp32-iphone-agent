package daemon

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/onehumanbeing/esp32-iphone-agent/cli/internal/ble"
	"github.com/onehumanbeing/esp32-iphone-agent/cli/internal/proto"
	"github.com/onehumanbeing/esp32-iphone-agent/cli/internal/relay"
)

// The board dials the relay again right after a session; a computer that comes in that moment waits for it.
const relayWait = 8 * time.Second

// viaRelay connects to a paired board through the cloud relay (docs/cli-protocol.md, "Cloud relay"), when one
// is set and sel names one paired board (or only one is paired). nil, nil: no relay or no board to ask for.
func (s *Server) viaRelay(ctx context.Context, sel string) (*Conn, error) {
	addr := s.Config.RelayAddr()
	if addr == "" {
		return nil, nil
	}
	ids := s.Config.IDs()
	if sel != "" {
		ids = s.Config.Match(sel)
	}
	if len(ids) != 1 {
		return nil, nil
	}
	id := ids[0]
	bid, _ := hex.DecodeString(id)
	ctx, cancel := context.WithTimeout(ctx, relayWait+5*time.Second)
	defer cancel()
	var err error
	for until := time.Now().Add(relayWait); ; {
		dctx, dcancel := context.WithTimeout(ctx, 5*time.Second)
		l, derr := s.dialRelay(dctx, addr, bid)
		dcancel()
		if derr == nil {
			name := "ESP32 Agent " + strings.ToUpper(id[8:])
			return s.open(ctx, ble.Found{Name: name}, id, l, "cloud")
		}
		err = derr
		if !errors.Is(derr, relay.ErrOffline) || time.Now().After(until) {
			return nil, err
		}
		if sleepCtx(ctx, time.Second) != nil {
			return nil, err
		}
	}
}

func (s *Server) dialRelay(ctx context.Context, addr string, boardID []byte) (proto.Link, error) {
	if s.DialRelay != nil {
		return s.DialRelay(ctx, addr, boardID)
	}
	return ble.DialRelay(ctx, addr, boardID)
}

// cloud: the relay the board dials out to, and the one this computer reaches boards through.
// arg: "" (show), "HOST:PORT" (set both), "off" (stop both).
func (s *Server) cloud(ctx context.Context, c *Conn, arg string) Response {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	arg = strings.TrimSpace(arg)
	if arg != "" {
		if arg != "off" {
			if _, port, err := net.SplitHostPort(arg); err != nil || port == "" {
				return fail(errors.New("usage: cloud [HOST:PORT | off]"))
			}
		}
		if _, err := c.Send(ctx, "cloud "+arg); err != nil {
			return fail(fmt.Errorf("the board did not take it (firmware before 0.2.7?): %w", err))
		}
		local := arg
		if arg == "off" {
			local = ""
		}
		if err := s.Config.SetRelay(local); err != nil {
			return fail(err)
		}
	}
	reply, err := c.Send(ctx, "cloud")
	if err != nil {
		return fail(fmt.Errorf("the board did not answer (firmware before 0.2.7?): %w", err))
	}
	var board map[string]any
	if json.Unmarshal([]byte(reply), &board) != nil {
		return fail(errors.New("board firmware too old for cloud"))
	}
	return ok(map[string]any{"via": c.Via(), "relay": s.Config.RelayAddr(), "board": board})
}
