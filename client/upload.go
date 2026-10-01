// Package client sends a file to an upload endpoint in fixed-size chunks.
package client

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

const chunkSize = 5 << 20 // 5 MiB

// httpClient is used for every chunk upload instead of http.DefaultClient,
// which has no timeout and would hang forever on a stalled connection.
var httpClient = &http.Client{Timeout: 30 * time.Second}

// UploadInChunks reads f in chunkSize pieces and sends each one to endpoint
// with sendChunk. A short final read (less than chunkSize) is a normal way
// for the file to end, not an error.
func UploadInChunks(ctx context.Context, endpoint string, f io.Reader) error {
	buf := make([]byte, chunkSize)
	chunkIndex := 0

	for {
		n, readErr := io.ReadFull(f, buf)
		if n > 0 {
			if err := sendChunk(ctx, endpoint, chunkIndex, buf[:n]); err != nil {
				return fmt.Errorf("chunk %d failed: %w", chunkIndex, err)
			}
			chunkIndex++
		}
		if readErr == io.EOF || readErr == io.ErrUnexpectedEOF {
			break
		}
		if readErr != nil {
			return fmt.Errorf("read chunk: %w", readErr)
		}
	}

	return nil
}

func sendChunk(ctx context.Context, endpoint string, chunkIndex int, data []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("X-Chunk-Index", strconv.Itoa(chunkIndex))
	req.ContentLength = int64(len(data))

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("send chunk: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server rejected chunk %d: %s", chunkIndex, resp.Status)
	}
	return nil
}
