package upload_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	transfermanager "github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"

	"github.com/clevertechware/upload-fichier-go/internal/upload"
)

// newFakeS3 starts an in-memory, in-process S3-compatible server (gofakes3)
// and returns a client pointed at it, plus the bucket it created.
//
// RequestChecksumCalculationWhenRequired disables the CRC32 trailer the SDK
// otherwise adds by default: gofakes3 only decodes the
// STREAMING-AWS4-HMAC-SHA256-PAYLOAD body encoding, not the
// STREAMING-UNSIGNED-PAYLOAD-TRAILER one used to carry that trailer, so
// leaving checksums on WhenSupported would make every request fail to
// decode on gofakes3's side.
func newFakeS3(t *testing.T) (client *s3.Client, bucket string) {
	t.Helper()

	faker := gofakes3.New(s3mem.New())
	server := httptest.NewServer(faker.Server())
	t.Cleanup(server.Close)

	bucket = "test-bucket"
	client = s3.New(s3.Options{
		Region:                     "us-east-1",
		Credentials:                credentials.NewStaticCredentialsProvider("KEY", "SECRET", ""),
		BaseEndpoint:               aws.String(server.URL),
		UsePathStyle:               true,
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
	})

	if _, err := client.CreateBucket(context.Background(), &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	return client, bucket
}

// newTestUploader wraps client in a transfermanager tuned so a payload of a
// few KiB already crosses the multipart threshold, without needing
// megabyte-sized test fixtures.
func newTestUploader(client *s3.Client) upload.S3Uploader {
	return transfermanager.New(client, func(o *transfermanager.Options) {
		o.MultipartUploadThreshold = 1024
		o.PartSizeBytes = 1024
		o.Concurrency = 2
		o.FailTimeout = 2 * time.Second
	})
}

func multipartS3Request(t *testing.T, fieldName, filename string, content []byte) *http.Request {
	t.Helper()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile(fieldName, filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("write content: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}

// bigPNG returns a payload that sniffs as image/png (the PNG signature is
// its first 8 bytes, which is all http.DetectContentType checks) padded to
// size bytes so it crosses a small multipart threshold in tests.
func bigPNG(size int) []byte {
	payload := append([]byte{}, tinyPNG[:8]...)
	return append(payload, bytes.Repeat([]byte{0}, size-len(payload))...)
}

func noPendingMultipartUploads(t *testing.T, client *s3.Client, bucket string) bool {
	t.Helper()
	out, err := client.ListMultipartUploads(context.Background(), &s3.ListMultipartUploadsInput{
		Bucket: aws.String(bucket),
	})
	if err != nil {
		t.Fatalf("list multipart uploads: %v", err)
	}
	return len(out.Uploads) == 0
}

func TestS3PipelineHandlerStoresObjectWithDetectedContentTypeAndHash(t *testing.T) {
	client, bucket := newFakeS3(t)
	uploader := newTestUploader(client)
	handler := upload.NewS3PipelineHandler(uploader, bucket, int64(len(tinyPNG))+1<<10, 4, time.Second, t.Logf)

	req := multipartS3Request(t, "file", "photo.png", tinyPNG)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	out, err := client.ListObjectsV2(context.Background(), &s3.ListObjectsV2Input{Bucket: aws.String(bucket)})
	if err != nil {
		t.Fatalf("list objects: %v", err)
	}
	if len(out.Contents) != 1 {
		t.Fatalf("object count = %d, want 1", len(out.Contents))
	}
	key := *out.Contents[0].Key

	obj, err := client.GetObject(context.Background(), &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		t.Fatalf("get object: %v", err)
	}
	defer obj.Body.Close()

	got, err := io.ReadAll(obj.Body)
	if err != nil {
		t.Fatalf("read object body: %v", err)
	}
	if !bytes.Equal(got, tinyPNG) {
		t.Fatal("stored bytes differ from source PNG")
	}
	if aws.ToString(obj.ContentType) != "image/png" {
		t.Fatalf("stored content type = %q, want image/png", aws.ToString(obj.ContentType))
	}

	sum := sha256.Sum256(tinyPNG)
	wantHash := hex.EncodeToString(sum[:])
	if !strings.Contains(rec.Body.String(), wantHash) {
		t.Fatalf("response %q does not report sha256 %s", rec.Body.String(), wantHash)
	}
}

func TestS3PipelineHandlerRejectsTypeOutsideAllowlistWithoutCallingS3(t *testing.T) {
	client, bucket := newFakeS3(t)
	uploader := newTestUploader(client)
	handler := upload.NewS3PipelineHandler(uploader, bucket, 1<<20, 4, time.Second, t.Logf)

	script := []byte("#!/bin/sh\necho hi\n")
	req := multipartS3Request(t, "file", "payload.sh", script)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnsupportedMediaType)
	}

	out, err := client.ListObjectsV2(context.Background(), &s3.ListObjectsV2Input{Bucket: aws.String(bucket)})
	if err != nil {
		t.Fatalf("list objects: %v", err)
	}
	if len(out.Contents) != 0 {
		t.Fatalf("no object should have been created, found %d", len(out.Contents))
	}
}

func TestS3PipelineHandlerAbortsMultipartUploadOn413(t *testing.T) {
	client, bucket := newFakeS3(t)
	uploader := newTestUploader(client)

	// 8 KiB crosses the 1 KiB test threshold well before the 2 KiB limit is
	// hit, so CreateMultipartUpload has already run by the time
	// MaxBytesReader cuts the body off.
	payload := bigPNG(8 << 10)
	handler := upload.NewS3PipelineHandler(uploader, bucket, 2<<10, 4, time.Second, t.Logf)

	req := multipartS3Request(t, "file", "big.png", payload)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusRequestEntityTooLarge, rec.Body.String())
	}

	out, err := client.ListObjectsV2(context.Background(), &s3.ListObjectsV2Input{Bucket: aws.String(bucket)})
	if err != nil {
		t.Fatalf("list objects: %v", err)
	}
	if len(out.Contents) != 0 {
		t.Fatalf("no object should have been created, found %d", len(out.Contents))
	}
	if !noPendingMultipartUploads(t, client, bucket) {
		t.Fatal("the aborted multipart upload should not be left open")
	}
}

// cancelAfterReader cancels cancel once threshold bytes have been read, then
// keeps delegating to r. It lets tests trigger context cancellation exactly
// once the multipart upload has already started, instead of racing a timer
// against the handler goroutine.
type cancelAfterReader struct {
	r         io.Reader
	remaining int
	cancel    context.CancelFunc
}

func (c *cancelAfterReader) Read(p []byte) (int, error) {
	if c.remaining <= 0 {
		c.cancel()
		c.remaining = -1 // cancel only once
	}
	n, err := c.r.Read(p)
	c.remaining -= n
	return n, err
}

func TestS3PipelineHandlerAbortsMultipartUploadOnContextCancellation(t *testing.T) {
	client, bucket := newFakeS3(t)
	uploader := newTestUploader(client)

	payload := bigPNG(8 << 10)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", "big.png")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write(payload); err != nil {
		t.Fatalf("write content: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req := httptest.NewRequest(http.MethodPost, "/upload", &cancelAfterReader{r: &buf, remaining: 2048, cancel: cancel})
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req = req.WithContext(ctx)

	handler := upload.NewS3PipelineHandler(uploader, bucket, int64(len(payload))+1<<10, 4, time.Second, t.Logf)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code == http.StatusOK {
		t.Fatalf("status = %d, want a failure status after context cancellation", rec.Code)
	}

	out, err := client.ListObjectsV2(context.Background(), &s3.ListObjectsV2Input{Bucket: aws.String(bucket)})
	if err != nil {
		t.Fatalf("list objects: %v", err)
	}
	if len(out.Contents) != 0 {
		t.Fatalf("no object should have been created, found %d", len(out.Contents))
	}
	if !noPendingMultipartUploads(t, client, bucket) {
		t.Fatal("the aborted multipart upload should not be left open")
	}
}

// TestS3PipelineHandlerLimitsConcurrentUploads drives two requests through a
// handler whose semaphore only has room for one: the first blocks mid-read
// (simulating an in-flight upload) and holds the only slot, so the second
// must be turned away with 503 and a Retry-After once its wait for a slot
// runs out.
func TestS3PipelineHandlerLimitsConcurrentUploads(t *testing.T) {
	client, bucket := newFakeS3(t)
	uploader := newTestUploader(client)
	handler := upload.NewS3PipelineHandler(uploader, bucket, 1<<20, 1, 50*time.Millisecond, t.Logf)

	blocking := &blockingReader{started: make(chan struct{}), unblock: make(chan struct{})}
	reqA := httptest.NewRequest(http.MethodPost, "/upload", io.NopCloser(blocking))
	reqA.Header.Set("Content-Type", "multipart/form-data; boundary=x")
	recA := httptest.NewRecorder()

	doneA := make(chan struct{})
	go func() {
		handler(recA, reqA)
		close(doneA)
	}()
	<-blocking.started // request A now holds the only slot

	reqB := multipartS3Request(t, "file", "photo.png", tinyPNG)
	recB := httptest.NewRecorder()
	handler(recB, reqB)

	close(blocking.unblock)
	<-doneA

	if recB.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d while the single upload slot is held", recB.Code, http.StatusServiceUnavailable)
	}
	if recB.Header().Get("Retry-After") == "" {
		t.Fatal("503 response should carry a Retry-After header")
	}
}

func TestS3PipelineHandlerPanicsWhenNoUploadSlotIsAllowed(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected NewS3PipelineHandler to panic when maxConcurrentUploads < 1")
		}
	}()
	upload.NewS3PipelineHandler(&contextCapturingUploader{}, "bucket", 1<<10, 0, time.Second, t.Logf)
}

// contextCapturingUploader records the context UploadObject receives and
// drains the body, so tests can observe what the handler passes down.
type contextCapturingUploader struct {
	ctx context.Context
}

func (c *contextCapturingUploader) UploadObject(
	ctx context.Context, in *transfermanager.UploadObjectInput, _ ...func(*transfermanager.Options),
) (*transfermanager.UploadObjectOutput, error) {
	c.ctx = ctx
	if _, err := io.Copy(io.Discard, in.Body); err != nil {
		return nil, err
	}
	return &transfermanager.UploadObjectOutput{}, nil
}

func TestS3PipelineHandlerPassesRequestContextToUploadObject(t *testing.T) {
	uploader := &contextCapturingUploader{}
	handler := upload.NewS3PipelineHandler(uploader, "bucket", 1<<20, 1, time.Second, t.Logf)

	ctx, cancel := context.WithCancel(context.Background())
	req := multipartS3Request(t, "file", "photo.png", tinyPNG).WithContext(ctx)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if uploader.ctx == nil {
		t.Fatal("UploadObject was not called")
	}

	cancel()
	select {
	case <-uploader.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("cancelling the request context did not cancel the context given to UploadObject")
	}
}

// blockingReader signals started on its first read, then blocks until
// unblock is closed, standing in for a slow, in-flight upload holding the
// only semaphore slot.
type blockingReader struct {
	started chan struct{}
	unblock chan struct{}
	once    sync.Once
}

func (b *blockingReader) Read([]byte) (int, error) {
	b.once.Do(func() { close(b.started) })
	<-b.unblock
	return 0, io.EOF
}

// countMultipartTempFiles counts entries starting with "multipart-" under
// dir, the prefix mime/multipart's formdata.go uses for the temp files it
// spills large parts to.
func countMultipartTempFiles(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read temp dir: %v", err)
	}
	count := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "multipart-") {
			count++
		}
	}
	return count
}

func TestS3PipelineHandlerCreatesNoMultipartTempFiles(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("TMPDIR", tmpDir)

	client, bucket := newFakeS3(t)
	uploader := newTestUploader(client)
	handler := upload.NewS3PipelineHandler(uploader, bucket, 1<<20, 4, time.Second, t.Logf)

	// 8 KiB comfortably crosses the 1 KiB test multipart threshold, so the
	// streamed part is read in several chunks rather than a single buffer.
	payload := bigPNG(8 << 10)
	req := multipartS3Request(t, "file", "big.png", payload)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if n := countMultipartTempFiles(t, tmpDir); n != 0 {
		t.Fatalf("multipart-* temp files created while streaming to S3 = %d, want 0", n)
	}
}

// TestFormFileHandlerCreatesMultipartTempFilesOverMaxMemory is the positive
// control for the test above: FormFile really does spill to disk once a
// part exceeds maxMemory, so a test asserting zero temp files for the S3
// path isn't just failing to look in the right place.
func TestFormFileHandlerCreatesMultipartTempFilesOverMaxMemory(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("TMPDIR", tmpDir)

	dest := t.TempDir()
	handler := upload.NewFormFileHandler(dest, 16) // maxMemory far below the payload below

	payload := bytes.Repeat([]byte("a"), 4096)
	req := multipartRequest(t, "file", "witness.bin", payload)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if n := countMultipartTempFiles(t, tmpDir); n == 0 {
		t.Fatal("expected FormFile to spill at least one multipart-* temp file as a positive control")
	}
}
