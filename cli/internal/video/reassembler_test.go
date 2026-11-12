package video

import "testing"

// After a new session the board goes on from the decoder's last AU: its first chunk is a P-frame flagged Resume,
// which must not be thrown away as if it were mid-GOP (2026-11-12).
func TestResumeIsAcceptedAfterAGap(t *testing.T) {
	r := NewReassembler()
	p := []byte{1, 2, 3, 4, 9, 9}
	if au := r.Feed(append([]byte{0, 0, Start | End}, p...)); au != nil {
		t.Fatal("a plain P-frame was taken without an IDR")
	}
	au := r.Feed(append([]byte{1, 0, Start | End | Resume}, p...))
	if au == nil || au.TS != 0x04030201 {
		t.Fatalf("resume AU: %+v", au)
	}
}
