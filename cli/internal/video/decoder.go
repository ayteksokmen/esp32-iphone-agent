package video

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"io"
	"os/exec"
	"sync"
	"time"

	"golang.org/x/image/bmp"
)

// Frame is one decoded picture.
type Frame struct {
	Img image.Image
	TS  uint32 // board ms of its AU
}

// Decoder is an ffmpeg process: Annex-B in, one BMP per picture out. (Not PNG: ffmpeg's PNG encoder holds
// each picture until the next one arrives, a whole frame of latency.)
type Decoder struct {
	cmd   *exec.Cmd
	in    io.WriteCloser
	queue chan AU
	mu    sync.Mutex
	ts    []uint32 // board times of AUs in flight, oldest first
	// Resync when idle (see idle): the last picture out, and the last AU fed, with when.
	lastImg          image.Image
	lastRaw          []byte // the last picture out when it was skipped (not decoded yet)
	lastOut, lastFed time.Time
	lastFedTS        uint32
	stderr           bytes.Buffer
	done             chan struct{}
	err              error
}

// FFmpeg finds the ffmpeg binary.
func FFmpeg() (string, error) {
	p, err := exec.LookPath("ffmpeg")
	if err != nil {
		for _, c := range []string{"/opt/homebrew/bin/ffmpeg", "/usr/local/bin/ffmpeg", "/usr/bin/ffmpeg"} {
			if _, e := exec.LookPath(c); e == nil {
				return c, nil
			}
		}
		return "", errors.New("ffmpeg not found: install it (brew install ffmpeg, apt install ffmpeg, winget install ffmpeg)")
	}
	return p, nil
}

// NewDecoder starts ffmpeg; onFrame gets every decoded picture until Close.
func NewDecoder(ffmpeg string, onFrame func(Frame)) (*Decoder, error) {
	// Room for a whole GOP (the board's holds up to 8192 AUs): a new stream starts with it at once, and over Wi-Fi it
	// arrives faster than ffmpeg decodes. 256 overflowed there and the stream waited minutes for the next IDR (2026-11-06).
	d := &Decoder{queue: make(chan AU, 8192), done: make(chan struct{})}
	d.cmd = exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-flags", "low_delay",
		"-probesize", "32", "-analyzeduration", "0", "-threads", "1", "-f", "h264", "-i", "pipe:0", "-fps_mode", "passthrough", "-flush_packets", "1", "-f", "image2pipe", "-c:v", "bmp", "pipe:1")
	d.cmd.Stderr = &d.stderr
	var err error
	if d.in, err = d.cmd.StdinPipe(); err != nil {
		return nil, err
	}
	out, err := d.cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := d.cmd.Start(); err != nil {
		return nil, err
	}
	go d.feed()
	go d.idle(onFrame)
	go func() {
		r := bufio.NewReaderSize(out, 1<<20)
		for {
			raw, err := readBMP(r)
			if err != nil {
				break
			}
			ts := d.popTS()
			// Catching up (the next picture already on its way): skip turning it into an image, the costly part.
			// A new stream starts with the whole GOP, minutes of pictures on a still screen, and decoding each took
			// minutes before the screen now showed (2026-11-12). The last one is always decoded.
			// The sign is the next picture's bytes already waiting from ffmpeg; the count of times queued is not one
			// (pictures ffmpeg dropped leave theirs there, and every picture would be skipped).
			if r.Buffered() > 0 {
				d.mu.Lock()
				d.lastRaw, d.lastOut = raw, time.Now() // idle decodes it if it stays the last
				d.mu.Unlock()
				continue
			}
			img, err := bmp.Decode(bytes.NewReader(raw))
			if err == nil {
				d.mu.Lock()
				d.lastImg, d.lastRaw, d.lastOut = img, nil, time.Now()
				d.mu.Unlock()
				onFrame(Frame{Img: img, TS: ts})
			}
		}
		d.err = d.cmd.Wait()
		if s := d.stderr.String(); s != "" {
			d.err = fmt.Errorf("ffmpeg: %s", s)
		}
		close(d.done)
	}()
	return d, nil
}

// idle: once ffmpeg has had nothing to do for a while, the picture out is the last AU's. Times still queued then
// belong to pictures ffmpeg dropped (undecodable ones), and each would have put every later picture one further in
// the past: a live screen read as 30 s old (2026-11-12). So: drop them, and give the last picture the last AU's time.
func (d *Decoder) idle(onFrame func(Frame)) {
	t := time.NewTicker(200 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-d.done:
			return
		case <-t.C:
		}
		d.mu.Lock()
		quiet := len(d.queue) == 0 && (len(d.ts) > 0 || d.lastRaw != nil) && (d.lastImg != nil || d.lastRaw != nil) &&
			time.Since(d.lastFed) > IdleAfter && time.Since(d.lastOut) > IdleAfter
		img, raw, ts := d.lastImg, d.lastRaw, d.lastFedTS
		if quiet {
			d.ts, d.lastRaw = d.ts[:0], nil
		}
		d.mu.Unlock()
		if quiet && raw != nil { // the last picture out was skipped: it is the screen now
			dec, err := bmp.Decode(bytes.NewReader(raw))
			if err != nil {
				continue
			}
			img = dec
			d.mu.Lock()
			d.lastImg = dec
			d.mu.Unlock()
		}
		if quiet {
			onFrame(Frame{Img: img, TS: ts})
		}
	}
}

// IdleAfter: how long ffmpeg has had no input and given no output before it counts as idle (see idle).
const IdleAfter = 400 * time.Millisecond

// maxPending: pictures fed to ffmpeg and not out yet whose times are kept (see feed).
const maxPending = 8192

func (d *Decoder) popTS() uint32 {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.ts) == 0 {
		return 0
	}
	t := d.ts[0]
	d.ts = d.ts[1:]
	return t
}

func (d *Decoder) feed() {
	for au := range d.queue {
		if au.Flags&(IDR|Config) == Config {
			// parameter sets only: no picture comes out of it
		} else {
			d.mu.Lock()
			d.lastFed, d.lastFedTS = time.Now(), au.TS
			d.ts = append(d.ts, au.TS)
			// Bounded, but far above a GOP: the board sends its whole GOP on a snap (a still screen's runs minutes,
			// over a thousand pictures) faster than ffmpeg decodes it. At 30, the queue dropped the old pictures'
			// times, those pictures took the newest ones, and a screen a minute old passed for now (2026-11-12).
			if len(d.ts) > maxPending {
				d.ts = d.ts[len(d.ts)-maxPending:]
			}
			d.mu.Unlock()
		}
		// The trailing delimiter ends the AU for ffmpeg's parser, which otherwise holds it until the next one starts.
		if _, err := d.in.Write(append(au.Data, aud...)); err != nil {
			break
		}
	}
	d.in.Close()
}

// Write queues one AU. A full queue means ffmpeg fell far behind; the AU is dropped and false returned.
func (d *Decoder) Write(au AU) bool {
	select {
	case <-d.done:
		return false
	case d.queue <- au:
		return true
	default:
		return false
	}
}

// Done is closed when ffmpeg exits; Err says why.
func (d *Decoder) Done() <-chan struct{} { return d.done }
func (d *Decoder) Err() error            { return d.err }

func (d *Decoder) Close() {
	defer func() { recover() }() // queue already closed
	close(d.queue)
	if d.cmd.Process != nil {
		d.cmd.Process.Kill()
	}
}

var aud = []byte{0, 0, 0, 1, 9, 0xf0} // access unit delimiter NAL

// readBMP reads one BMP file from a stream of concatenated BMPs.
func readBMP(r *bufio.Reader) ([]byte, error) {
	hdr := make([]byte, 6)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return nil, err
	}
	n := binary.LittleEndian.Uint32(hdr[2:])
	if string(hdr[:2]) != "BM" || n < 54 || n > 64<<20 {
		return nil, errors.New("not a BMP stream")
	}
	buf := make([]byte, n)
	copy(buf, hdr)
	_, err := io.ReadFull(r, buf[6:])
	return buf, err
}
