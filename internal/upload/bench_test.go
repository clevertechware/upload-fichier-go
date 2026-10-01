package upload_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/clevertechware/upload-fichier-go/internal/genfile"
	"github.com/clevertechware/upload-fichier-go/internal/upload"
)

var benchSizes = []int64{1 << 20, 64 << 20, 256 << 20}

func BenchmarkReadAllHandler(b *testing.B) {
	benchmarkHandler(b, upload.NewReadAllHandler)
}

func BenchmarkFormFileHandler(b *testing.B) {
	benchmarkHandler(b, func(dest string) http.HandlerFunc {
		return upload.NewFormFileHandler(dest, 32<<20)
	})
}

func BenchmarkMultipartReaderHandler(b *testing.B) {
	benchmarkHandler(b, func(dest string) http.HandlerFunc {
		// Limit set above every benchmarked size so it never interferes with
		// the comparison; the 413 path has its own tests.
		return upload.NewMultipartReaderHandler(dest, 1<<30)
	})
}

// benchmarkHandler drives newHandler with a streamed multipart body (built
// through an io.Pipe by genfile.MultipartBody) so the allocations reported by
// b.ReportAllocs come from the handler under test, not from building the
// request body in memory first.
func benchmarkHandler(b *testing.B, newHandler func(dest string) http.HandlerFunc) {
	for _, size := range benchSizes {
		if testing.Short() && size > 1<<20 {
			continue
		}

		b.Run(sizeLabel(size), func(b *testing.B) {
			dest := b.TempDir()
			handler := newHandler(dest)

			b.ReportAllocs()
			b.SetBytes(size)
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				body, contentType := genfile.MultipartBody("file", "payload.bin", size)
				req := httptest.NewRequest(http.MethodPost, "/upload", body)
				req.Header.Set("Content-Type", contentType)
				rec := httptest.NewRecorder()

				handler(rec, req)

				if rec.Code != http.StatusOK {
					b.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
				}
			}
		})
	}
}

func sizeLabel(size int64) string {
	return fmt.Sprintf("%dMiB", size/(1<<20))
}
