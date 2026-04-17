package codientcli

import "io"

// chanReader implements io.Reader on top of a string channel.
// Each received string is treated as a complete line (a "\n" is appended).
// When the channel is closed, subsequent reads return io.EOF.
type chanReader struct {
	ch  <-chan string
	buf []byte
}

func (r *chanReader) Read(p []byte) (int, error) {
	if len(r.buf) == 0 {
		line, ok := <-r.ch
		if !ok {
			return 0, io.EOF
		}
		r.buf = []byte(line + "\n")
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}
