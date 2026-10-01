package upload

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"os"

	"github.com/clevertechware/upload-fichier-go/streamio"
)

// Logf matches log.Printf's signature, so callers can pass log.Printf
// directly or a no-op for tests.
type Logf func(format string, args ...any)

// nextFilePart scans parts until it finds one carrying a filename, skipping
// plain form fields along the way. Callers get exactly the same guarantee
// r.FormFile gives them: the returned part is never a text field mistaken
// for a file because it happened to arrive first.
func nextFilePart(mr *multipart.Reader) (*multipart.Part, error) {
	for {
		part, err := mr.NextPart()
		if err != nil {
			return nil, err
		}
		if part.FileName() != "" {
			return part, nil
		}
		part.Close()
	}
}

// wrapReaderFunc lets newPipelineHandler add behavior around the hashing
// reader (e.g. progress tracking) right before the final copy, without the
// validation pipeline itself knowing that behavior exists.
type wrapReaderFunc func(ctx context.Context, r io.Reader, storedName string) io.Reader

// newPipelineHandler builds the shared pipeline: bound the request size,
// take the first file part, sniff and validate its real content type, hash
// it in passing, and write it under a server-generated name confined to
// root. wrap is applied to the hashing reader right before the copy; a nil
// wrap copies from it unchanged.
func newPipelineHandler(root *os.Root, maxUploadSize int64, logf Logf, wrap wrapReaderFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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
		contentType, err := SniffType(br)
		if err != nil {
			http.Error(w, "cannot read file header", http.StatusBadRequest)
			return
		}
		if err := ValidateType(contentType); err != nil {
			http.Error(w, "unsupported file type", http.StatusUnsupportedMediaType)
			return
		}

		storedName, err := GenerateStoredName(contentType)
		if err != nil {
			http.Error(w, "cannot generate name", http.StatusInternalServerError)
			return
		}

		dst, err := CreateInRoot(root, storedName)
		if err != nil {
			http.Error(w, "cannot store file", http.StatusInternalServerError)
			return
		}

		hashed := NewHashingReader(br)
		var source io.Reader = hashed
		if wrap != nil {
			source = wrap(r.Context(), source, storedName)
		}

		if _, err := io.Copy(dst, source); err != nil {
			cleanupFailedUpload(root, dst, storedName, logf)

			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				http.Error(w, "file too large", http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, "upload failed", http.StatusInternalServerError)
			return
		}

		if err := dst.Close(); err != nil {
			cleanupFailedUpload(root, nil, storedName, logf)
			http.Error(w, "upload failed", http.StatusInternalServerError)
			return
		}

		fmt.Fprintf(w, "stored %s as %s (sha256 %s)\n", part.FileName(), storedName, hashed.Sum())
	}
}

// cleanupFailedUpload closes dst if it hasn't been closed yet (a no-op if
// dst is nil) and removes the partial file. Closing before removing matters
// on Windows, which refuses to delete a file that's still open.
func cleanupFailedUpload(root *os.Root, dst *os.File, storedName string, logf Logf) {
	if dst != nil {
		if cerr := dst.Close(); cerr != nil {
			logf("close partial upload %s: %v", storedName, cerr)
		}
	}
	if rmErr := root.Remove(storedName); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
		logf("cleanup partial upload %s: %v", storedName, rmErr)
	}
}

// NewValidatingHandler builds the article 2 pipeline: bound the request
// size, stream the multipart part, sniff and validate its real content
// type, hash it in passing, and write it under a server-generated name
// confined to root. A failed or oversized copy removes the partial file.
func NewValidatingHandler(root *os.Root, maxUploadSize int64, logf Logf) http.HandlerFunc {
	return newPipelineHandler(root, maxUploadSize, logf, nil)
}

// NewTrackedPipelineHandler adds the article 3 TrackedReader on top of the
// same pipeline: r.Context() is passed through so a client disconnect or
// server-side cancellation stops the copy instead of running to completion.
// The total size passed to the tracker is unknown (-1): r.ContentLength
// covers the whole multipart body, not the size of this one file part.
func NewTrackedPipelineHandler(root *os.Root, maxUploadSize int64, logf Logf) http.HandlerFunc {
	return newPipelineHandler(root, maxUploadSize, logf, func(ctx context.Context, r io.Reader, storedName string) io.Reader {
		return streamio.NewTrackedReader(ctx, r, -1, func(read, total int64) {
			logf("upload %s: %d/%d octets", storedName, read, total)
		})
	})
}
