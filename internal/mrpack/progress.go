package mrpack

import "io"

// Progress observes a single file download. Add reports newly received bytes;
// Done releases whatever the tracker holds. Both must tolerate being called
// with no bytes at all: a skipped or failed download still ends the tracker.
type Progress interface {
	Add(int64)
	Done()
}

// ProgressFunc starts tracking the named file, whose expected size is total
// bytes. It may return nil for a silent transfer — skipped files never start
// one at all, so nil only means the caller asked for quiet.
type ProgressFunc func(name string, total int64) Progress

// progressReader advances a tracker as the body streams in, so progress moves
// while bytes arrive instead of snapping to full at the end.
type progressReader struct {
	src io.Reader
	p   Progress
}

func (r progressReader) Read(b []byte) (int, error) {
	n, err := r.src.Read(b)
	if n > 0 && r.p != nil {
		r.p.Add(int64(n))
	}
	return n, err
}

// progressTotal picks the byte count for a tracker. The server's
// Content-Length wins over the size the manifest advertised, because it is
// what is actually being sent; a stale size would otherwise stop the bar
// short of full.
func progressTotal(contentLength, hint int64) int64 {
	if contentLength > 0 {
		return contentLength
	}
	return hint
}
