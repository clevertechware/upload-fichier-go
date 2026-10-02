// Package upload implements the three receiving strategies compared in the
// first article of the series, plus the content-sniffing, hashing and
// storage building blocks added by the second.
package upload

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
)

// storeUnderClientName writes src to dest under the file name sent by the
// client. It is deliberately naive: the second article replaces it with a
// server-generated name.
func storeUnderClientName(dest, clientName string, src io.Reader) error {
	path := filepath.Join(dest, filepath.Base(clientName))
	dst, err := os.Create(path) //nolint:gosec // client-chosen name is the flaw the second article fixes
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}

	if _, err = io.Copy(dst, src); err != nil {
		_ = dst.Close()
		return fmt.Errorf("copy upload: %w", err)
	}
	if err = dst.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}
	return nil
}

// NewMultipartReaderHandler builds the streaming handler: MaxBytesReader
// bounds the body, MultipartReader hands over the file part as it arrives,
// and a body past maxUploadSize is answered with a 413.
func NewMultipartReaderHandler(dest string, maxUploadSize int64) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)

		reader, err := r.MultipartReader()
		if err != nil {
			http.Error(w, "invalid multipart body", http.StatusBadRequest)
			return
		}

		part, err := reader.NextPart()
		if err != nil {
			http.Error(w, "no file part", http.StatusBadRequest)
			return
		}
		defer part.Close()

		if err = storeUnderClientName(dest, part.FileName(), part); err != nil {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				http.Error(w, "file too large", http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, "upload failed", http.StatusInternalServerError)
		}
	}
}

// NewReadAllHandler reproduces the anecdote that opens the series: it reads
// the whole request body into memory before parsing the multipart form.
func NewReadAllHandler(dest string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			http.Error(w, "invalid content type", http.StatusBadRequest)
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "cannot read body", http.StatusInternalServerError)
			return
		}

		part, err := multipart.NewReader(bytes.NewReader(body), params["boundary"]).NextPart()
		if err != nil {
			http.Error(w, "no file part", http.StatusBadRequest)
			return
		}
		defer part.Close()

		if err = storeUnderClientName(dest, part.FileName(), part); err != nil {
			http.Error(w, "upload failed", http.StatusInternalServerError)
		}
	}
}

// NewFormFileHandler stores an upload received through ParseMultipartForm /
// FormFile, the "half fix" described in the first article.
func NewFormFileHandler(dest string, maxMemory int64) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		//nolint:gosec // unbounded on purpose: the article measures what FormFile costs without MaxBytesReader
		if err := r.ParseMultipartForm(maxMemory); err != nil {
			http.Error(w, fmt.Sprintf("invalid multipart form: %v", err), http.StatusBadRequest)
			return
		}

		file, header, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "no file field", http.StatusBadRequest)
			return
		}
		defer file.Close()

		if err = storeUnderClientName(dest, header.Filename, file); err != nil {
			http.Error(w, "upload failed", http.StatusInternalServerError)
		}
	}
}
