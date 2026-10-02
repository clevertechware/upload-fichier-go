package upload

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWriteErrorMapsErrorsToHTTPStatus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		err        error
		wantStatus int
	}{
		{"status error keeps its own status", &statusError{http.StatusUnsupportedMediaType, "unsupported file type"}, 415},
		{"body past the limit is a 413", &http.MaxBytesError{Limit: 10}, http.StatusRequestEntityTooLarge},
		{"wrapped body past the limit is a 413", fmt.Errorf("copy: %w", &http.MaxBytesError{Limit: 10}),
			http.StatusRequestEntityTooLarge},
		{"unknown error is a 500", errors.New("disk on fire"), http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()

			writeError(rec, tt.err)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
		})
	}
}
