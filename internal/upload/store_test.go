package upload_test

import (
	"path/filepath"
	"testing"

	"github.com/clevertechware/upload-fichier-go/internal/upload"
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
			name, err := upload.GenerateStoredName(tt.contentType)
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
	if _, err := upload.GenerateStoredName("application/x-sh"); err == nil {
		t.Fatal("expected an error for a content type outside AllowedTypes, got nil")
	}
}
