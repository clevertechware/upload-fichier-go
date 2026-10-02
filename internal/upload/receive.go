package upload

import (
	"bufio"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"

	"github.com/clevertechware/upload-fichier-go/internal/filecheck"
)

// statusError carries the HTTP status and public message of a rejected upload.
type statusError struct {
	status int
	msg    string
}

func (e *statusError) Error() string {
	return fmt.Sprintf("%d %s", e.status, e.msg)
}

// validatedPart is the first file part of a request, already sniffed and validated. body replays the sniffed bytes
// and must be read instead of part. The caller closes part.
type validatedPart struct {
	part        *multipart.Part
	body        *bufio.Reader
	contentType string
	storedName  string
}

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
		_ = part.Close()
	}
}

// readValidatedPart is the reception shared by the file and S3 pipelines: bound the request size, take the first
// file part, sniff and validate its real content type, and generate the server-side name. Every failure is a
// *statusError that writeError turns into the response.
func readValidatedPart(w http.ResponseWriter, r *http.Request, maxUploadSize int64) (validatedPart, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)

	mr, err := r.MultipartReader()
	if err != nil {
		return validatedPart{}, &statusError{http.StatusBadRequest, "invalid multipart body"}
	}

	part, err := nextFilePart(mr)
	if err != nil {
		return validatedPart{}, &statusError{http.StatusBadRequest, "no file part"}
	}

	br := bufio.NewReader(part)
	contentType, err := filecheck.SniffType(br)
	if err != nil {
		_ = part.Close()
		return validatedPart{}, &statusError{http.StatusBadRequest, "cannot read file header"}
	}
	if err = filecheck.ValidateType(contentType); err != nil {
		_ = part.Close()
		return validatedPart{}, &statusError{http.StatusUnsupportedMediaType, "unsupported file type"}
	}

	storedName, err := filecheck.GenerateStoredName(contentType)
	if err != nil {
		_ = part.Close()
		return validatedPart{}, &statusError{http.StatusInternalServerError, "cannot generate name"}
	}

	return validatedPart{part: part, body: br, contentType: contentType, storedName: storedName}, nil
}

// writeError answers with the status of a statusError, 413 for a body past the size limit and 500 otherwise.
func writeError(w http.ResponseWriter, err error) {
	var statusErr *statusError
	if errors.As(err, &statusErr) {
		http.Error(w, statusErr.msg, statusErr.status)
		return
	}
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		http.Error(w, "file too large", http.StatusRequestEntityTooLarge)
		return
	}
	http.Error(w, "upload failed", http.StatusInternalServerError)
}
