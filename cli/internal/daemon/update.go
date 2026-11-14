package daemon

import (
	"bufio"
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"time"
)

var errTooOld = errors.New("this board's firmware can't be updated from the CLI yet: update it once in the app or over USB")

// OTAPort is the board's firmware update server (hardware/hid_bridge/ota.h).
const OTAPort = "7300"

// UpdateBack: how long update waits for the board to come back after it took the image.
var UpdateBack = 2 * time.Minute

// update pushes a firmware image to the board over Wi-Fi, as the app's Update Board does, without a USB cable:
// the board hands this paired session a one-time token (`ota`), the image goes to TCP 7300, the board checks its
// MD5, writes the other app slot and reboots. Then it waits for the board to answer again and reports its fw.
// docs/cli-protocol.md, "Firmware update".
func (s *Server) update(ctx context.Context, c *Conn, req Request) Response {
	image, err := os.ReadFile(req.Text)
	if err != nil {
		return fail(err)
	}
	// An ESP32 app image starts with 0xE9; the board would refuse anything else only after taking all of it.
	if len(image) < 1024 || image[0] != 0xE9 {
		return fail(fmt.Errorf("%s is not an ESP32 firmware image (the .ino.bin from hardware/flash.sh --build-only)", req.Text))
	}
	st, _ := c.Status()
	from, _ := st["fw"].(string)
	ip := boardIP(c)
	if ip == "" {
		return fail(errors.New("the board is not on Wi-Fi yet: an update goes over Wi-Fi (esp32agent net join)"))
	}
	tctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	token, err := c.Send(tctx, "ota")
	cancel()
	if err != nil && err.Error() != "unknown command" {
		return fail(err) // busy (the app), an update already running, a --lean build
	}
	if token = strings.TrimSpace(token); err != nil || len(token) != 32 {
		return fail(errTooOld)
	}
	sum := md5.Sum(image)
	started := time.Now()
	log.Printf("%s: update: %d bytes to %s (fw %s)", c.ID, len(image), ip, from)
	if err := s.pushOTA(ctx, ip, token, image, hex.EncodeToString(sum[:])); err != nil {
		return fail(fmt.Errorf("update failed, the board still runs %s: %w", from, err))
	}
	sent := time.Since(started)
	log.Printf("%s: update: sent in %v, waiting for the board", c.ID, sent.Round(time.Millisecond))
	c.pointer(s.Dir).Forget() // a new boot: the pointer re-homes
	fw, err := s.waitBack(ctx, req.Device, started)
	if err != nil {
		return fail(fmt.Errorf("the board took the image but did not come back within %v: %w", UpdateBack, err))
	}
	msg := "updated to " + fw
	if fw == from {
		msg = "the board came back with " + fw + " again: the new image did not start, or has the same version"
	}
	return ok(map[string]any{"message": msg, "from": from, "fw": fw, "bytes": len(image),
		"send_ms": sent.Milliseconds(), "total_ms": time.Since(started).Milliseconds()})
}

// pushOTA speaks ota.h's protocol: "ota <token> <size> <md5>\n", "ok\n", the image, "done\n" or "err <why>\n".
func (s *Server) pushOTA(ctx context.Context, ip, token string, image []byte, md5hex string) error {
	dial := s.DialOTA
	if dial == nil {
		dial = (&net.Dialer{Timeout: 10 * time.Second}).DialContext
	}
	nc, err := dial(ctx, "tcp", net.JoinHostPort(ip, OTAPort))
	if err != nil {
		return fmt.Errorf("can't reach the board at %s (this computer must be on the board's Wi-Fi): %w", ip, err)
	}
	defer nc.Close()
	stop := context.AfterFunc(ctx, func() { nc.Close() }) // Ctrl-C: the board aborts and keeps the old image
	defer stop()
	// The board drops a session quiet for 5 s; the whole image takes well under a minute on Wi-Fi.
	nc.SetDeadline(time.Now().Add(15 * time.Second))
	r := bufio.NewReader(nc)
	if _, err := fmt.Fprintf(nc, "ota %s %d %s\n", token, len(image), md5hex); err != nil {
		return err
	}
	if line, err := readOTALine(r); err != nil || line != "ok" {
		return otaErr(line, err)
	}
	nc.SetDeadline(time.Now().Add(5 * time.Minute))
	if _, err := nc.Write(image); err != nil {
		return fmt.Errorf("sending the image: %w", err)
	}
	nc.SetDeadline(time.Now().Add(60 * time.Second)) // Update.end checks the MD5
	if line, err := readOTALine(r); err != nil || line != "done" {
		return otaErr(line, err)
	}
	return nil
}

func readOTALine(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	return strings.TrimSpace(line), err
}

func otaErr(line string, err error) error {
	if line != "" {
		return fmt.Errorf("board: %s", line)
	}
	return err
}

// waitBack waits for a status from the board's new boot (uptime shorter than the time since the update began)
// and returns its fw.
func (s *Server) waitBack(ctx context.Context, sel string, since time.Time) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, UpdateBack)
	defer cancel()
	last := errors.New("no status yet")
	for {
		select {
		case <-ctx.Done():
			return "", last
		case <-time.After(500 * time.Millisecond):
		}
		c, err := s.conn(ctx, sel)
		if err != nil {
			last = err
			continue
		}
		st, age := c.Status()
		up, _ := st["up"].(float64)
		fw, _ := st["fw"].(string)
		if age < 0 || fw == "" || age > time.Since(since) || time.Duration(up)*time.Millisecond >= time.Since(since) {
			last = errors.New("the board has not rebooted yet")
			continue
		}
		return fw, nil
	}
}
