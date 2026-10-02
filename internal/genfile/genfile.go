// Package genfile generates synthetic multipart upload bodies without
// holding the whole payload in memory, so benchmarks measure the handler's
// allocations rather than the generator's.
package genfile

import (
	"io"
	"mime/multipart"
)

// PatternReader emits size bytes of a deterministic, repeating pattern.
type PatternReader struct {
	remaining int64
}

// NewPatternReader returns a PatternReader that yields size bytes before EOF.
func NewPatternReader(size int64) *PatternReader {
	return &PatternReader{remaining: size}
}

func (p *PatternReader) Read(buf []byte) (int, error) {
	if p.remaining <= 0 {
		return 0, io.EOF
	}
	n := len(buf)
	if int64(n) > p.remaining {
		n = int(p.remaining)
	}
	for i := range buf[:n] {
		buf[i] = byte(i)
	}
	p.remaining -= int64(n)
	return n, nil
}

// MultipartBody streams a single-file multipart body of size bytes through
// an io.Pipe, so the caller never holds more than one write buffer's worth
// of the payload at a time. It returns the body and the Content-Type header
// value to send with it.
func MultipartBody(fieldName, filename string, size int64) (io.ReadCloser, string) {
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	contentType := mw.FormDataContentType()

	go func() {
		part, err := mw.CreateFormFile(fieldName, filename)
		if err != nil {
			pw.CloseWithError(err)
			return
		}
		if _, err = io.Copy(part, NewPatternReader(size)); err != nil {
			pw.CloseWithError(err)
			return
		}
		pw.CloseWithError(mw.Close())
	}()

	return pr, contentType
}
