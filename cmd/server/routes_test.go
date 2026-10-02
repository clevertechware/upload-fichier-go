package main

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	transfermanager "github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
)

var pngSignature = []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}

type drainingUploader struct{}

func (drainingUploader) UploadObject(
	_ context.Context, in *transfermanager.UploadObjectInput, _ ...func(*transfermanager.Options),
) (*transfermanager.UploadObjectOutput, error) {
	_, err := io.Copy(io.Discard, in.Body)
	return &transfermanager.UploadObjectOutput{}, err
}

func newTestRouter(t *testing.T, uploader *drainingUploader) (*router, string) {
	t.Helper()

	dest := t.TempDir()
	cfg := config{dest: dest, maxUploadSize: 1 << 20, logf: t.Logf, s3Bucket: "bucket"}
	if uploader != nil {
		cfg.s3Uploader = uploader
	}
	rt, err := newRouter(cfg)
	if err != nil {
		t.Fatalf("new router: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := rt.close(); closeErr != nil {
			t.Errorf("close router: %v", closeErr)
		}
	})
	return rt, dest
}

func uploadRequest(t *testing.T, path, filename string, content []byte) *http.Request {
	t.Helper()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err = part.Write(content); err != nil {
		t.Fatalf("write content: %v", err)
	}
	if err = writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, path, &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

func serve(rt *router, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	rt.mux.ServeHTTP(rec, req)
	return rec
}

func TestEveryUploadRouteLivesUnderItsArticleSlug(t *testing.T) {
	t.Parallel()

	png := append(append([]byte{}, pngSignature...), bytes.Repeat([]byte{0}, 64)...)
	tests := []struct {
		name     string
		path     string
		content  []byte
		wantCode int
		wantDir  string
	}{
		{"article 1 read-all", "/articles/" + slugMemory + "/read-all", png, http.StatusOK, slugMemory},
		{"article 1 form-file", "/articles/" + slugMemory + "/form-file", png, http.StatusOK, slugMemory},
		{"article 1 multipart-reader", "/articles/" + slugMemory + "/multipart-reader", png, http.StatusOK, slugMemory},
		{"article 2 upload", "/articles/" + slugValidate + "/upload", png, http.StatusOK, slugValidate},
		{"article 3 upload", "/articles/" + slugTracking + "/upload", png, http.StatusOK, slugTracking},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rt, dest := newTestRouter(t, nil)

			rec := serve(rt, uploadRequest(t, tt.path, "image.png", tt.content))

			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tt.wantCode, rec.Body)
			}
			entries, err := os.ReadDir(filepath.Join(dest, tt.wantDir))
			if err != nil {
				t.Fatalf("read article directory: %v", err)
			}
			if len(entries) != 1 {
				t.Fatalf("%s holds %d files, want 1", tt.wantDir, len(entries))
			}
		})
	}
}

func TestValidationRouteRejectsTextFileThatArticle1RouteWouldStore(t *testing.T) {
	t.Parallel()
	rt, _ := newTestRouter(t, nil)
	text := []byte(strings.Repeat("not an image", 10))

	validated := serve(rt, uploadRequest(t, "/articles/"+slugValidate+"/upload", "notes.txt", text))
	naive := serve(rt, uploadRequest(t, "/articles/"+slugMemory+"/multipart-reader", "notes.txt", text))

	if validated.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("article 2 status = %d, want %d", validated.Code, http.StatusUnsupportedMediaType)
	}
	if naive.Code != http.StatusOK {
		t.Fatalf("article 1 status = %d, want %d", naive.Code, http.StatusOK)
	}
}

func TestChunksRouteBelongsToTrackingArticle(t *testing.T) {
	t.Parallel()
	rt, _ := newTestRouter(t, nil)

	req := httptest.NewRequest(http.MethodPut, "/articles/"+slugTracking+"/chunks", strings.NewReader("chunk"))
	req.Header.Set("X-Chunk-Index", "0")
	rec := serve(rt, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestS3RouteAnswers503WhenNoBucketIsConfigured(t *testing.T) {
	t.Parallel()
	rt, _ := newTestRouter(t, nil)

	rec := serve(rt, uploadRequest(t, "/articles/"+slugS3+"/upload", "image.png", pngSignature))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestS3RouteUploadsThroughConfiguredUploader(t *testing.T) {
	t.Parallel()
	rt, _ := newTestRouter(t, &drainingUploader{})

	rec := serve(rt, uploadRequest(t, "/articles/"+slugS3+"/upload", "image.png", pngSignature))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "s3://bucket/") {
		t.Fatalf("body = %q, want the stored s3:// location", rec.Body)
	}
}

func TestLegacyUnprefixedRoutesAreGone(t *testing.T) {
	t.Parallel()
	rt, _ := newTestRouter(t, nil)

	for _, path := range []string{"/upload", "/chunks"} {
		rec := serve(rt, uploadRequest(t, path, "image.png", pngSignature))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want %d", path, rec.Code, http.StatusNotFound)
		}
	}
}
