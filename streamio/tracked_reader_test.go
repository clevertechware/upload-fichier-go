package streamio_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/clevertechware/upload-fichier-go/streamio"
)

// drainInChunks reads r to EOF in fixed-size chunks. Reading through a fixed
// small buffer (rather than io.ReadAll, whose adaptive buffer can swallow an
// entire small payload in a single Read) is what makes the throttling below
// actually exercise more than one Read call.
func drainInChunks(t *testing.T, r io.Reader, chunkSize int) {
	t.Helper()

	buf := make([]byte, chunkSize)
	for {
		_, err := r.Read(buf)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				t.Fatalf("unexpected read error: %v", err)
			}
			return
		}
	}
}

func TestTrackedReaderThrottlesProgressCallbacks(t *testing.T) {
	data := bytes.Repeat([]byte("x"), 2<<20) // 2 MiB, read in 4 KiB chunks below.
	src := bytes.NewReader(data)

	var callbacks int
	var lastRead, lastTotal int64
	tr := streamio.NewTrackedReader(context.Background(), src, int64(len(data)), func(read, total int64) {
		callbacks++
		lastRead, lastTotal = read, total
	})

	drainInChunks(t, tr, 4096)

	// The whole read completes in well under the 200ms throttle window, so
	// only the first read and the final EOF should have reported progress.
	if callbacks != 2 {
		t.Fatalf("callbacks = %d, want 2 (first read + EOF)", callbacks)
	}
	if lastRead != int64(len(data)) || lastTotal != int64(len(data)) {
		t.Fatalf("final callback reported %d/%d, want %d/%d", lastRead, lastTotal, len(data), len(data))
	}
}

func TestTrackedReaderReportsFinalCallbackOnEOF(t *testing.T) {
	// Large enough, read through a small fixed buffer, that most of the many
	// intermediate Read calls fall inside the 200ms throttle window and are
	// suppressed: only the EOF branch can make the last callback report the
	// full total. A payload short enough for a single Read (as with
	// io.ReadAll on a few bytes) would pass this test even without that
	// branch, since the very first callback is never throttled either.
	data := bytes.Repeat([]byte("y"), 2<<20)
	src := bytes.NewReader(data)

	var lastRead, lastTotal int64
	tr := streamio.NewTrackedReader(context.Background(), src, int64(len(data)), func(read, total int64) {
		lastRead, lastTotal = read, total
	})

	drainInChunks(t, tr, 4096)

	if lastRead != int64(len(data)) || lastTotal != int64(len(data)) {
		t.Fatalf("final callback reported %d/%d, want %d/%d", lastRead, lastTotal, len(data), len(data))
	}
}

func TestTrackedReaderReadReturnsCancellationBeforeReading(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	underlying := bytes.NewReader([]byte("should never be read"))
	tr := streamio.NewTrackedReader(ctx, underlying, 100, nil)

	n, err := tr.Read(make([]byte, 10))
	if n != 0 {
		t.Fatalf("n = %d, want 0", n)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want wrapped context.Canceled", err)
	}
}
