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

// destDir is where handleUpload and handleUploadWithLimit store files. It
// only exists so those two functions can keep the exact signature shown in
// the article; every other handler in this package takes its destination
// directory as a parameter instead.
var destDir = os.TempDir()

func destPath(name string) string {
	return filepath.Join(destDir, filepath.Base(name))
}

// handleUpload is the minimal streaming handler from the first article: it
// reads the multipart body part by part instead of buffering it.
func handleUpload(w http.ResponseWriter, r *http.Request) {
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

	dst, err := os.Create(destPath(part.FileName()))
	if err != nil {
		http.Error(w, "cannot store file", http.StatusInternalServerError)
		return
	}
	defer dst.Close()

	if _, err := io.Copy(dst, part); err != nil {
		http.Error(w, "upload failed", http.StatusInternalServerError)
		return
	}
}

// handleUploadWithLimit adds the size guard from the article's second
// example: http.MaxBytesReader and the errors.As check that turns a
// *http.MaxBytesError into a 413 response.
func handleUploadWithLimit(maxUploadSize int64) http.HandlerFunc {
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

		dst, err := os.Create(destPath(part.FileName()))
		if err != nil {
			http.Error(w, "cannot store file", http.StatusInternalServerError)
			return
		}
		defer dst.Close()

		if _, err := io.Copy(dst, part); err != nil {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				http.Error(w, "file too large", http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, "upload failed", http.StatusInternalServerError)
			return
		}
	}
}

// NewMultipartReaderHandler builds the streaming handler used for the memory
// comparison, storing files under dest and rejecting bodies past
// maxUploadSize with a 413.
func NewMultipartReaderHandler(dest string, maxUploadSize int64) http.HandlerFunc {
	destDir = dest
	return handleUploadWithLimit(maxUploadSize)
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

		reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
		part, err := reader.NextPart()
		if err != nil {
			http.Error(w, "no file part", http.StatusBadRequest)
			return
		}
		defer part.Close()

		dst, err := os.Create(filepath.Join(dest, filepath.Base(part.FileName())))
		if err != nil {
			http.Error(w, "cannot store file", http.StatusInternalServerError)
			return
		}
		defer dst.Close()

		if _, err := io.Copy(dst, part); err != nil {
			http.Error(w, "upload failed", http.StatusInternalServerError)
			return
		}
	}
}

// NewFormFileHandler stores an upload received through ParseMultipartForm /
// FormFile, the "half fix" described in the first article.
func NewFormFileHandler(dest string, maxMemory int64) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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

		dst, err := os.Create(filepath.Join(dest, filepath.Base(header.Filename)))
		if err != nil {
			http.Error(w, "cannot store file", http.StatusInternalServerError)
			return
		}
		defer dst.Close()

		if _, err := io.Copy(dst, file); err != nil {
			http.Error(w, "upload failed", http.StatusInternalServerError)
			return
		}
	}
}
