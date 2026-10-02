package filecheck_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/clevertechware/upload-fichier-go/internal/filecheck"
)

func TestGenerateStoredNameUsesExtensionFromContentType(t *testing.T) {
	t.Parallel()
	tests := []struct {
		contentType string
		wantExt     string
	}{
		{"image/png", ".png"},
		{"image/jpeg", ".jpg"},
		{"application/pdf", ".pdf"},
	}

	for _, tt := range tests {
		t.Run(tt.contentType, func(t *testing.T) {
			t.Parallel()
			name, err := filecheck.GenerateStoredName(tt.contentType)
			if err != nil {
				t.Fatalf("GenerateStoredName(%q): %v", tt.contentType, err)
			}
			if ext := filepath.Ext(name); ext != tt.wantExt {
				t.Fatalf("extension = %q, want %q", ext, tt.wantExt)
			}
		})
	}
}

func TestGenerateStoredNameRejectsContentTypeOutsideAllowlist(t *testing.T) {
	t.Parallel()
	if _, err := filecheck.GenerateStoredName("application/x-sh"); err == nil {
		t.Fatal("expected an error for a content type outside AllowedTypes, got nil")
	}
}

func TestCreateInRootRefusesPathEscape(t *testing.T) {
	t.Parallel()
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	t.Cleanup(func() { _ = root.Close() })

	if _, err = filecheck.CreateInRoot(root, "../../evil.png"); err == nil {
		t.Fatal("expected CreateInRoot to refuse a path escaping the root, got nil error")
	}
}
