package filecheck

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"io"
)

// HashingReader tees every byte read from the wrapped reader into a sha256
// hash, so the digest is available once the reader has been fully consumed
// without a second pass over the data.
type HashingReader struct {
	io.Reader
	sum hash.Hash
}

// NewHashingReader wraps r with io.TeeReader so reads from the returned
// HashingReader also feed a sha256 hash.
func NewHashingReader(r io.Reader) *HashingReader {
	h := sha256.New()
	return &HashingReader{Reader: io.TeeReader(r, h), sum: h}
}

// Sum returns the hex-encoded sha256 digest of everything read so far. Call
// it only after the wrapped reader has reached EOF.
func (hr *HashingReader) Sum() string {
	return hex.EncodeToString(hr.sum.Sum(nil))
}
