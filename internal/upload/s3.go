package upload

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	transfermanager "github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/clevertechware/upload-fichier-go/internal/filecheck"
	"github.com/clevertechware/upload-fichier-go/streamio"
)

const (
	s3PartSize    = 5 << 20
	s3Concurrency = 2
	s3FailTimeout = 30 * time.Second
)

// ConfigureS3Uploader applies the article 4 settings to a transfermanager: threshold and part size at 5 MiB and
// concurrency 2, so an upload holds about 5 + (2+1)*5 = 20 MiB whatever the file size, and a FailTimeout that lets
// the multipart abort go through after the request context is cancelled.
func ConfigureS3Uploader(o *transfermanager.Options) {
	o.PartSizeBytes = s3PartSize
	o.MultipartUploadThreshold = s3PartSize
	o.Concurrency = s3Concurrency
	o.FailTimeout = s3FailTimeout
}

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

		received, err := readValidatedPart(w, r, maxUploadSize)
		if err != nil {
			writeError(w, err)
			return
		}
		defer received.part.Close()
		key, contentType := received.storedName, received.contentType

		hashed := filecheck.NewHashingReader(received.body)
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
			if !errors.As(err, &maxErr) {
				logf("upload s3://%s/%s failed: %v", bucket, key, err)
			}
			writeError(w, err)
			return
		}

		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "stored %s as s3://%s/%s (sha256 %s)\n", received.part.FileName(), bucket, key, hashed.Sum())
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
