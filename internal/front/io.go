package front

import "io"

// LimitReaderFrom is a reader that reads from R at Offset but stops with EOF
// after N bytes.
type LimitReaderFrom struct {
	R      io.ReaderAt
	N      int64
	Offset int64
}

func (l *LimitReaderFrom) Read(p []byte) (n int, err error) {
	if l.N <= 0 {
		// Already read everything.
		return 0, io.EOF
	}
	if int64(len(p)) > l.N {
		p = p[:l.N] // limit p to l.N bytes
	}
	n, err = l.R.ReadAt(p, l.Offset)
	l.Offset += int64(n) // advance the offset
	l.N -= int64(n)      // decrement the remaining bytes
	return
}
