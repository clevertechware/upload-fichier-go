package upload

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
)

const storedNameBytes = 16

// GenerateStoredName returns a random, collision-resistant filename for
// storing an upload. The extension comes from the sniffed contentType, via
// AllowedTypes, never from the client-supplied name: a PNG uploaded as
// "evil.html" is still stored with a ".png" extension, so a mismatched
// client-chosen name can't turn into a file served with the wrong type.
func GenerateStoredName(contentType string) (string, error) {
	ext, ok := AllowedTypes[contentType]
	if !ok {
		return "", fmt.Errorf("generate stored name: %w: %q", ErrUnsupportedType, contentType)
	}

	buf := make([]byte, storedNameBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate stored name: %w", err)
	}
	return hex.EncodeToString(buf) + ext, nil
}

// CreateInRoot creates name for writing inside root. Unlike a plain
// os.Create with a path built from user input, a name that tries to escape
// root (e.g. "../../evil.png") fails here instead of landing outside the
// upload directory.
func CreateInRoot(root *os.Root, name string) (*os.File, error) {
	f, err := root.Create(name)
	if err != nil {
		return nil, fmt.Errorf("create %s under root: %w", name, err)
	}
	return f, nil
}
