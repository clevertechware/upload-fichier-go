package upload

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// sniffLen is the number of bytes http.DetectContentType looks at.
const sniffLen = 512

// ErrUnsupportedType is returned by ValidateType when the sniffed content
// type isn't in AllowedTypes.
var ErrUnsupportedType = errors.New("unsupported content type")

// AllowedTypes maps each MIME type accepted after content sniffing to the
// file extension used when storing it. A single map keeps the allow-list and
// the extension table from drifting apart.
var AllowedTypes = map[string]string{
	"image/png":       ".png",
	"image/jpeg":      ".jpg",
	"application/pdf": ".pdf",
}

// SniffType peeks at the first sniffLen bytes of br without consuming them and
// returns the MIME type detected by http.DetectContentType.
func SniffType(br *bufio.Reader) (string, error) {
	head, err := br.Peek(sniffLen)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("peek content: %w", err)
	}
	return http.DetectContentType(head), nil
}

// ValidateType returns ErrUnsupportedType if contentType isn't in AllowedTypes.
func ValidateType(contentType string) error {
	if _, ok := AllowedTypes[contentType]; !ok {
		return fmt.Errorf("%w: %s", ErrUnsupportedType, contentType)
	}
	return nil
}
