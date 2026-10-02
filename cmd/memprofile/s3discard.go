package main

import (
	"context"
	"errors"
	"io"
	"mime/multipart"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/clevertechware/upload-fichier-go/internal/genfile"
)

// pngSignature is the 8-byte magic http.DetectContentType matches to report
// image/png; the sniffing pipeline only ever looks at these bytes.
var pngSignature = []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}

// s3MultipartBody streams a single-file multipart body of size bytes,
// starting with the PNG signature so NewS3PipelineHandler's content sniffing
// accepts it, through an io.Pipe so the generator never holds more than one
// write buffer's worth of the payload at a time.
func s3MultipartBody(fieldName, filename string, size int64) (io.ReadCloser, string) {
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	contentType := mw.FormDataContentType()

	go func() {
		part, err := mw.CreateFormFile(fieldName, filename)
		if err != nil {
			pw.CloseWithError(err)
			return
		}
		if _, err := part.Write(pngSignature); err != nil {
			pw.CloseWithError(err)
			return
		}
		if _, err := io.Copy(part, genfile.NewPatternReader(size-int64(len(pngSignature)))); err != nil {
			pw.CloseWithError(err)
			return
		}
		pw.CloseWithError(mw.Close())
	}()

	return pr, contentType
}

// discardS3Client implements transfermanager.S3APIClient without any
// network round trip or storage: every body is drained with
// io.Copy(io.Discard, ...) and dropped. Unlike an in-memory fake S3 server
// running in this same process, it never retains the uploaded bytes, so it
// doesn't inflate the heap measurement it's used for.
type discardS3Client struct{}

func (discardS3Client) PutObject(
	_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options),
) (*s3.PutObjectOutput, error) {
	if _, err := io.Copy(io.Discard, in.Body); err != nil {
		return nil, err
	}
	return &s3.PutObjectOutput{}, nil
}

func (discardS3Client) UploadPart(
	_ context.Context, in *s3.UploadPartInput, _ ...func(*s3.Options),
) (*s3.UploadPartOutput, error) {
	if _, err := io.Copy(io.Discard, in.Body); err != nil {
		return nil, err
	}
	etag := "\"discarded\""
	return &s3.UploadPartOutput{ETag: &etag}, nil
}

func (discardS3Client) CreateMultipartUpload(
	_ context.Context, in *s3.CreateMultipartUploadInput, _ ...func(*s3.Options),
) (*s3.CreateMultipartUploadOutput, error) {
	uploadID := "discarded-upload"
	return &s3.CreateMultipartUploadOutput{UploadId: &uploadID, Bucket: in.Bucket, Key: in.Key}, nil
}

func (discardS3Client) CompleteMultipartUpload(
	_ context.Context, in *s3.CompleteMultipartUploadInput, _ ...func(*s3.Options),
) (*s3.CompleteMultipartUploadOutput, error) {
	return &s3.CompleteMultipartUploadOutput{Bucket: in.Bucket, Key: in.Key}, nil
}

func (discardS3Client) AbortMultipartUpload(
	_ context.Context, _ *s3.AbortMultipartUploadInput, _ ...func(*s3.Options),
) (*s3.AbortMultipartUploadOutput, error) {
	return &s3.AbortMultipartUploadOutput{}, nil
}

func (discardS3Client) GetObject(
	context.Context, *s3.GetObjectInput, ...func(*s3.Options),
) (*s3.GetObjectOutput, error) {
	return nil, errNotImplemented
}

func (discardS3Client) HeadObject(
	context.Context, *s3.HeadObjectInput, ...func(*s3.Options),
) (*s3.HeadObjectOutput, error) {
	return nil, errNotImplemented
}

func (discardS3Client) ListObjectsV2(
	context.Context, *s3.ListObjectsV2Input, ...func(*s3.Options),
) (*s3.ListObjectsV2Output, error) {
	return nil, errNotImplemented
}

var errNotImplemented = errors.New("discardS3Client: only upload operations are implemented")
