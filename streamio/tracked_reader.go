// Package streamio provides io.Reader wrappers for observing a stream
// without buffering it.
package streamio

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

// progressInterval throttles progress callbacks: a 2 GiB upload read in 4 KiB buffers would otherwise
// report about 524,000 times.
const progressInterval = 200 * time.Millisecond

// ProgressFunc reports how many of totalSize bytes have been read so far.
// totalSize is -1 when the total size is unknown.
type ProgressFunc func(bytesRead, totalSize int64)

// TrackedReader wraps a reader to report progress at most once per
// minInterval and to honor context cancellation.
type TrackedReader struct {
	reader      io.Reader
	totalSize   int64
	bytesRead   int64
	onProgress  ProgressFunc
	ctx         context.Context //nolint:containedctx // io.Reader.Read has no ctx parameter to carry it
	lastReport  time.Time
	minInterval time.Duration
}

// NewTrackedReader wraps r, throttling calls to onProgress to once every
// 200ms (plus a final call on EOF) and returning a wrapped ctx.Err() instead
// of reading once ctx is done.
func NewTrackedReader(ctx context.Context, r io.Reader, totalSize int64, onProgress ProgressFunc) *TrackedReader {
	return &TrackedReader{
		reader:      r,
		totalSize:   totalSize,
		onProgress:  onProgress,
		ctx:         ctx,
		minInterval: progressInterval,
	}
}

// Read reads from the wrapped reader, reporting progress and stopping once the context is done.
func (tr *TrackedReader) Read(p []byte) (int, error) {
	select {
	case <-tr.ctx.Done():
		return 0, fmt.Errorf("read cancelled: %w", tr.ctx.Err())
	default:
	}

	n, err := tr.reader.Read(p)
	tr.bytesRead += int64(n)

	if tr.onProgress != nil && (time.Since(tr.lastReport) >= tr.minInterval || errors.Is(err, io.EOF)) {
		tr.onProgress(tr.bytesRead, tr.totalSize)
		tr.lastReport = time.Now()
	}

	return n, err
}
