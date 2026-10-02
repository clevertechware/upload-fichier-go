package upload

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	transfermanager "github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/clevertechware/upload-fichier-go/internal/filecheck"
	"github.com/clevertechware/upload-fichier-go/streamio"
)

// S3Uploader is the subset of *transfermanager.Client used by
// NewS3PipelineHandler, narrow enough to substitute with a test double
// pointed at a fake S3 endpoint.
type S3Uploader interface {
	UploadObject(
		ctx context.Context, in *transfermanager.UploadObjectInput, opts ...func(*transfermanager.Options),
	) (*transfermanager.UploadObjectOutput, error)
}

// NewS3PipelineHandler builds the article 4 pipeline: the same validation
// steps as NewTrackedPipelineHandler (bound the request size with
// MaxBytesReader, take the first file part, sniff and validate its real
// content type, hash it in passing, track its progress) but the destination
// is an S3 object instead of a file confined under root.
//
// r.Context() is passed to UploadObject so that a server-side cancellation
// (or a handler timeout) stops the in-flight UploadPart calls. A client
// disconnect on HTTP/1.1 surfaces first as a read error on the request body,
// which fails the upload the same way. Either way the transfermanager then
// aborts the multipart upload, but with the caller's context: the uploader
// must set Options.FailTimeout, otherwise the abort is sent with an already
// cancelled context, fails, and leaves the multipart upload open.
//
// maxConcurrentUploads (at least 1, otherwise this panics) bounds how many
// uploads run at once: the transfermanager buffers roughly
// MultipartUploadThreshold + (Concurrency+1) * PartSizeBytes per upload, and
// that memory isn't shared across requests. A request that finds no free slot
// within slotWait is answered with 503 and a Retry-After header instead of
// queueing without bound.
func NewS3PipelineHandler(
	uploader S3Uploader, bucket string, maxUploadSize int64, maxConcurrentUploads int, slotWait time.Duration, logf Logf,
) http.HandlerFunc {
	if maxConcurrentUploads < 1 {
		panic(fmt.Sprintf("upload: maxConcurrentUploads must be at least 1, got %d", maxConcurrentUploads))
	}
	sem := make(chan struct{}, maxConcurrentUploads)

	return func(w http.ResponseWriter, r *http.Request) {
		if !acquireSlot(r.Context(), sem, slotWait) {
			w.Header().Set("Retry-After", "5")
			http.Error(w, "too many concurrent uploads", http.StatusServiceUnavailable)
			return
		}
		defer func() { <-sem }()

		r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)

		mr, err := r.MultipartReader()
		if err != nil {
			http.Error(w, "invalid multipart body", http.StatusBadRequest)
			return
		}

		part, err := nextFilePart(mr)
		if err != nil {
			http.Error(w, "no file part", http.StatusBadRequest)
			return
		}
		defer part.Close()

		br := bufio.NewReader(part)
		contentType, err := filecheck.SniffType(br)
		if err != nil {
			http.Error(w, "cannot read file header", http.StatusBadRequest)
			return
		}
		if err = filecheck.ValidateType(contentType); err != nil {
			http.Error(w, "unsupported file type", http.StatusUnsupportedMediaType)
			return
		}

		key, err := filecheck.GenerateStoredName(contentType)
		if err != nil {
			http.Error(w, "cannot generate object key", http.StatusInternalServerError)
			return
		}

		hashed := filecheck.NewHashingReader(br)
		tracked := streamio.NewTrackedReader(r.Context(), hashed, -1, func(read, total int64) {
			if total < 0 {
				logf("upload s3://%s/%s: %d octets", bucket, key, read)
				return
			}
			logf("upload s3://%s/%s: %d/%d octets", bucket, key, read, total)
		})

		_, err = uploader.UploadObject(r.Context(), &transfermanager.UploadObjectInput{
			Bucket:      &bucket,
			Key:         &key,
			Body:        tracked,
			ContentType: &contentType,
		})
		if err != nil {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				http.Error(w, "file too large", http.StatusRequestEntityTooLarge)
				return
			}
			logf("upload s3://%s/%s failed: %v", bucket, key, err)
			http.Error(w, "upload failed", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		//nolint:gosec // G705: served as text/plain
		fmt.Fprintf(w, "stored %s as s3://%s/%s (sha256 %s)\n", part.FileName(), bucket, key, hashed.Sum())
	}
}

// acquireSlot waits up to wait for a free slot in sem and reports whether it
// got one, giving up early if ctx is done.
func acquireSlot(ctx context.Context, sem chan struct{}, wait time.Duration) bool {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case sem <- struct{}{}:
		return true
	case <-timer.C:
		return false
	case <-ctx.Done():
		return false
	}
}
