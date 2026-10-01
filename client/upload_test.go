package client

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/clevertechware/upload-fichier-go/internal/chunkupload"
)

func TestUploadInChunksReassemblesExactBytesWithShortReads(t *testing.T) {
	// chunkSize is 5 MiB; use a payload spanning a full chunk plus a short
	// final one so the last read through io.ReadFull is genuinely partial.
	original := make([]byte, chunkSize+1234)
	for i := range original {
		original[i] = byte(i)
	}

	t.Run("OneByteReader", func(t *testing.T) {
		handler := chunkupload.NewHandler()
		server := httptest.NewServer(handler)
		defer server.Close()

		src := iotest.OneByteReader(bytes.NewReader(original))
		if err := UploadInChunks(context.Background(), server.URL, src); err != nil {
			t.Fatalf("UploadInChunks: %v", err)
		}

		got, err := handler.Assemble()
		if err != nil {
			t.Fatalf("assemble: %v", err)
		}
		if !bytes.Equal(got, original) {
			t.Fatalf("reassembled %d bytes differ from the %d original bytes", len(got), len(original))
		}
	})

	t.Run("HalfReader", func(t *testing.T) {
		handler := chunkupload.NewHandler()
		server := httptest.NewServer(handler)
		defer server.Close()

		src := iotest.HalfReader(bytes.NewReader(original))
		if err := UploadInChunks(context.Background(), server.URL, src); err != nil {
			t.Fatalf("UploadInChunks: %v", err)
		}

		got, err := handler.Assemble()
		if err != nil {
			t.Fatalf("assemble: %v", err)
		}
		if !bytes.Equal(got, original) {
			t.Fatalf("reassembled %d bytes differ from the %d original bytes", len(got), len(original))
		}
	})
}

func TestUploadInChunksSendsSequentialChunkIndexHeader(t *testing.T) {
	handler := chunkupload.NewHandler()
	server := httptest.NewServer(handler)
	defer server.Close()

	original := bytes.Repeat([]byte("a"), chunkSize*2+10)

	if err := UploadInChunks(context.Background(), server.URL, bytes.NewReader(original)); err != nil {
		t.Fatalf("UploadInChunks: %v", err)
	}

	got, err := handler.Assemble()
	if err != nil {
		t.Fatalf("assemble (implies indices 0,1,2 all present): %v", err)
	}
	if !bytes.Equal(got, original) {
		t.Fatal("reassembled bytes differ from original")
	}
}

func TestUploadInChunksFailsWhenServerIsUnreachable(t *testing.T) {
	server := httptest.NewServer(nil)
	server.Close() // closed server: every request fails to connect.

	err := UploadInChunks(context.Background(), server.URL, bytes.NewReader([]byte("data")))
	if err == nil {
		t.Fatal("expected an error when the server is unreachable")
	}
}

func TestUploadInChunksReportsServerRejectionWithChunkIndex(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer server.Close()

	err := UploadInChunks(context.Background(), server.URL, bytes.NewReader([]byte("data")))
	if err == nil {
		t.Fatal("expected an error when the server rejects the chunk")
	}
	if !strings.Contains(err.Error(), "server rejected chunk 0") {
		t.Fatalf("error = %q, want it to mention %q", err.Error(), "server rejected chunk 0")
	}
}
