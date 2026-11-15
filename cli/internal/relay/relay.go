// Package relay joins boards and computers that can't reach each other directly (docs/cli-protocol.md,
// "Cloud relay"). A board dials out and attaches under its id; a computer joins that id; from then on the relay
// copies bytes both ways. The session inside is the paired AES-GCM one: the relay sees ids and sizes, never
// the commands, the replies or the video.
package relay

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"net"
	"sync"
	"time"
)

// Frame types of the relay's own records (plain, before the pipe starts). The rest are the CLI's (proto).
const (
	Attach = 0x03 // board → relay: board_id[6]
	Join   = 0x04 // computer → relay: board_id[6]
	Joined = 0x84 // relay → computer: the board is on the other end now
	Error  = 0xE0 // relay → computer: UTF-8 reason
)

const (
	keepalive = 2 * time.Second  // the board drops a relay link quiet for 6 s
	firstWait = 10 * time.Second // for a new connection's first record
)

// Server holds the boards waiting for a computer.
type Server struct {
	mu     sync.Mutex
	boards map[string]*board
	Logf   func(format string, args ...any) // nil: log.Printf
}

type board struct {
	nc    net.Conn
	claim chan net.Conn
}

func (s *Server) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

// Serve accepts connections on l until it fails.
func (s *Server) Serve(l net.Listener) error {
	for {
		nc, err := l.Accept()
		if err != nil {
			return err
		}
		go s.handle(nc)
	}
}

// ListenAndServe serves on addr (":7400").
func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	var lc net.ListenConfig
	l, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		l.Close()
	}()
	s.logf("relay: listening on %s", l.Addr())
	return s.Serve(l)
}

// Record writes one [u16 LE length][frame] record.
func Record(w io.Writer, frame []byte) error {
	_, err := w.Write(append(binary.LittleEndian.AppendUint16(nil, uint16(len(frame))), frame...))
	return err
}

// ReadRecord reads one record, skipping keepalives (zero length).
func ReadRecord(r io.Reader) ([]byte, error) {
	for {
		var hdr [2]byte
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			return nil, err
		}
		n := binary.LittleEndian.Uint16(hdr[:])
		if n == 0 {
			continue
		}
		f := make([]byte, n)
		_, err := io.ReadFull(r, f)
		return f, err
	}
}

func (s *Server) handle(nc net.Conn) {
	if tc, ok := nc.(*net.TCPConn); ok {
		tc.SetNoDelay(true)
		tc.SetKeepAlive(true)
		tc.SetKeepAlivePeriod(15 * time.Second)
	}
	nc.SetReadDeadline(time.Now().Add(firstWait))
	f, err := ReadRecord(nc)
	nc.SetReadDeadline(time.Time{})
	if err != nil || len(f) != 7 || f[0] != Attach && f[0] != Join {
		nc.Close()
		return
	}
	id := hex.EncodeToString(f[1:])
	if f[0] == Attach {
		s.attach(nc, id)
	} else {
		s.join(nc, id)
	}
}

// attach keeps a board's link alive until a computer claims it.
func (s *Server) attach(nc net.Conn, id string) {
	b := &board{nc: nc, claim: make(chan net.Conn, 1)}
	s.mu.Lock()
	if s.boards == nil {
		s.boards = map[string]*board{}
	}
	old := s.boards[id]
	s.boards[id] = b
	s.mu.Unlock()
	if old != nil { // the board dialed again (its old link is dead, or it rebooted)
		old.nc.Close()
	}
	s.logf("relay: board %s attached from %s", id, nc.RemoteAddr())
	t := time.NewTicker(keepalive)
	defer t.Stop()
	for {
		select {
		case c := <-b.claim:
			pipe(nc, c)
			s.logf("relay: board %s session ended", id)
			return
		case <-t.C:
			nc.SetWriteDeadline(time.Now().Add(5 * time.Second))
			err := Record(nc, nil)
			nc.SetWriteDeadline(time.Time{})
			if err != nil {
				s.mu.Lock()
				current := s.boards[id] == b
				if current {
					delete(s.boards, id)
				}
				s.mu.Unlock()
				nc.Close()
				select {
				case c := <-b.claim: // claimed meanwhile
					refuse(c, "board went offline")
				default:
				}
				if current { // not when a new link of the same board replaced it
					s.logf("relay: board %s gone", id)
				}
				return
			}
		}
	}
}

// join hands a computer the board waiting under id.
func (s *Server) join(nc net.Conn, id string) {
	s.mu.Lock()
	b := s.boards[id]
	delete(s.boards, id) // one computer per board link; the board dials again after the session
	s.mu.Unlock()
	if b == nil {
		refuse(nc, "board offline")
		return
	}
	nc.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if Record(nc, []byte{Joined}) != nil {
		nc.Close()
		b.nc.Close() // the board dials again
		return
	}
	nc.SetWriteDeadline(time.Time{})
	s.logf("relay: computer %s joined board %s", nc.RemoteAddr(), id)
	b.claim <- nc
}

func refuse(nc net.Conn, why string) {
	nc.SetWriteDeadline(time.Now().Add(5 * time.Second))
	Record(nc, append([]byte{Error}, why...))
	nc.Close()
}

// pipe copies both ways until either side closes, then closes both.
func pipe(a, b net.Conn) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		io.Copy(dst, src)
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	<-done
	a.Close()
	b.Close()
	<-done
}

// ErrOffline: no board is attached under that id.
var ErrOffline = errors.New("board offline")
