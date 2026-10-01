// Package chunkupload receives the chunks sent by the client package so the
// series' tests have a server to talk to. It keeps every chunk in memory,
// has no persistence and no authentication: it is a demonstration harness
// for the article, not something to expose on a real deployment.
package chunkupload

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
)

// maxChunkSize bounds a single request body. It must stay at or above the
// client's own chunk size (5 MiB, see client.chunkSize) or every upload
// through this demo handler would fail.
const maxChunkSize = 5 << 20

// Handler collects chunks in memory, keyed by their index.
type Handler struct {
	mu     sync.Mutex
	chunks map[int][]byte
}

// NewHandler returns an empty Handler.
func NewHandler() *Handler {
	return &Handler{chunks: make(map[int][]byte)}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	index, err := strconv.Atoi(r.Header.Get("X-Chunk-Index"))
	if err != nil {
		http.Error(w, "missing or invalid X-Chunk-Index", http.StatusBadRequest)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxChunkSize)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "chunk too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "cannot read chunk body", http.StatusInternalServerError)
		return
	}

	h.mu.Lock()
	h.chunks[index] = data
	h.mu.Unlock()

	w.WriteHeader(http.StatusOK)
}

// Assemble concatenates the received chunks in index order (0..n-1). It
// fails if any index in that range was never received.
func (h *Handler) Assemble() ([]byte, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	var buf bytes.Buffer
	for i := 0; i < len(h.chunks); i++ {
		chunk, ok := h.chunks[i]
		if !ok {
			return nil, fmt.Errorf("missing chunk %d", i)
		}
		buf.Write(chunk)
	}
	return buf.Bytes(), nil
}
