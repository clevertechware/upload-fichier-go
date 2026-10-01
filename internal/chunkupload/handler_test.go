package chunkupload_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/clevertechware/upload-fichier-go/internal/chunkupload"
)

func TestHandlerRejectsChunkOverMaxSize(t *testing.T) {
	handler := chunkupload.NewHandler()

	oversized := bytes.Repeat([]byte("a"), 5<<20+1)
	req := httptest.NewRequest(http.MethodPut, "/chunks", bytes.NewReader(oversized))
	req.Header.Set("X-Chunk-Index", "0")
	req.ContentLength = int64(len(oversized))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusRequestEntityTooLarge)
	}
}

func TestHandlerAcceptsChunkAtMaxSize(t *testing.T) {
	handler := chunkupload.NewHandler()

	exact := bytes.Repeat([]byte("a"), 5<<20)
	req := httptest.NewRequest(http.MethodPut, "/chunks", bytes.NewReader(exact))
	req.Header.Set("X-Chunk-Index", "0")
	req.ContentLength = int64(len(exact))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}
