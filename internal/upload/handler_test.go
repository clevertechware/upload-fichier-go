package upload_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/clevertechware/upload-fichier-go/internal/upload"
)

// tinyPNG is a valid 1x1 PNG, enough for http.DetectContentType to recognize
// "image/png" and small enough to keep tests fast.
var tinyPNG = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d,
	0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4, 0x89, 0x00, 0x00, 0x00,
	0x0d, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9c, 0x62, 0x00, 0x01, 0x00, 0x00,
	0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00, 0x00, 0x00, 0x00, 0x49,
	0x45, 0x4e, 0x44, 0xae, 0x42, 0x60, 0x82,
}

func multipartRequest(t *testing.T, fieldName, filename string, content []byte) *http.Request {
	t.Helper()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile(fieldName, filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("write content: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}

func TestMultipartReaderHandlerRejectsBodyOverLimit(t *testing.T) {
	dest := t.TempDir()
	handler := upload.NewMultipartReaderHandler(dest, 1<<10) // 1 KiB limit

	payload := bytes.Repeat([]byte("a"), 2<<10) // 2 KiB
	req := multipartRequest(t, "file", "big.bin", payload)
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusRequestEntityTooLarge)
	}
}

func TestMultipartReaderHandlerStoresFileUnderLimit(t *testing.T) {
	dest := t.TempDir()
	handler := upload.NewMultipartReaderHandler(dest, 1<<20)

	req := multipartRequest(t, "file", "small.bin", []byte("hello world"))
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	got, err := os.ReadFile(filepath.Join(dest, "small.bin"))
	if err != nil {
		t.Fatalf("read stored file: %v", err)
	}
	if string(got) != "hello world" {
		t.Fatalf("stored content = %q, want %q", got, "hello world")
	}
}

func TestReadAllHandlerStoresFile(t *testing.T) {
	dest := t.TempDir()
	handler := upload.NewReadAllHandler(dest)

	req := multipartRequest(t, "file", "readall.bin", []byte("payload"))
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got, err := os.ReadFile(filepath.Join(dest, "readall.bin")); err != nil || string(got) != "payload" {
		t.Fatalf("stored content = %q, err = %v", got, err)
	}
}

func TestFormFileHandlerStoresFile(t *testing.T) {
	dest := t.TempDir()
	handler := upload.NewFormFileHandler(dest, 32<<20)

	req := multipartRequest(t, "file", "formfile.bin", []byte("form payload"))
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got, err := os.ReadFile(filepath.Join(dest, "formfile.bin")); err != nil || string(got) != "form payload" {
		t.Fatalf("stored content = %q, err = %v", got, err)
	}
}

func TestFormFileHandlerRejectsMissingField(t *testing.T) {
	dest := t.TempDir()
	handler := upload.NewFormFileHandler(dest, 32<<20)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("other", "value"); err != nil {
		t.Fatalf("write field: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestValidatingHandlerStoresPNGWithGeneratedNameAndIntactBytes(t *testing.T) {
	storeDir := t.TempDir()
	root, err := os.OpenRoot(storeDir)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer root.Close()

	handler := upload.NewValidatingHandler(root, int64(len(tinyPNG))+1<<10, t.Logf)

	req := multipartRequest(t, "file", "../../evil.png", tinyPNG)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	entries, err := os.ReadDir(storeDir)
	if err != nil {
		t.Fatalf("read store dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("stored file count = %d, want 1", len(entries))
	}

	storedName := entries[0].Name()
	if storedName == "evil.png" || storedName == "../../evil.png" {
		t.Fatalf("stored name %q should not reuse client-supplied name", storedName)
	}
	if filepath.Ext(storedName) != ".png" {
		t.Fatalf("stored name %q should keep the .png extension", storedName)
	}

	got, err := os.ReadFile(filepath.Join(storeDir, storedName))
	if err != nil {
		t.Fatalf("read stored file: %v", err)
	}
	if !bytes.Equal(got, tinyPNG) {
		t.Fatalf("stored bytes differ from source PNG")
	}

	wantHash := sha256Hex(tinyPNG)
	if !strings.Contains(rec.Body.String(), wantHash) {
		t.Fatalf("response %q does not report sha256 %s", rec.Body.String(), wantHash)
	}

	detected := http.DetectContentType(got)
	if detected != "image/png" {
		t.Fatalf("detected content type = %q, want image/png", detected)
	}
}

func TestValidatingHandlerRejectsTypeOutsideAllowlist(t *testing.T) {
	storeDir := t.TempDir()
	root, err := os.OpenRoot(storeDir)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer root.Close()

	handler := upload.NewValidatingHandler(root, 1<<20, t.Logf)

	script := []byte("#!/bin/sh\necho hi\n")
	req := multipartRequest(t, "file", "payload.sh", script)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnsupportedMediaType)
	}

	entries, err := os.ReadDir(storeDir)
	if err != nil {
		t.Fatalf("read store dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("no file should be left behind, found %d", len(entries))
	}
}

func TestValidatingHandlerRemovesPartialFileOn413(t *testing.T) {
	storeDir := t.TempDir()
	root, err := os.OpenRoot(storeDir)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer root.Close()

	// The PNG signature sits in the first 8 bytes so Peek(512) still detects
	// the type; the limit only bites once io.Copy tries to write the rest.
	oversized := append(append([]byte{}, tinyPNG[:8]...), bytes.Repeat([]byte{0}, 4096)...)
	handler := upload.NewValidatingHandler(root, 1024, t.Logf)

	req := multipartRequest(t, "file", "big.png", oversized)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusRequestEntityTooLarge)
	}

	entries, err := os.ReadDir(storeDir)
	if err != nil {
		t.Fatalf("read store dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("partial file should have been removed, found %d entries", len(entries))
	}
}

func TestValidatingHandlerDerivesExtensionFromDetectedTypeNotClientFilename(t *testing.T) {
	storeDir := t.TempDir()
	root, err := os.OpenRoot(storeDir)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer root.Close()

	handler := upload.NewValidatingHandler(root, int64(len(tinyPNG))+1<<10, t.Logf)

	// The client claims ".html"; the real content, sniffed from the bytes, is
	// a PNG. The stored extension must follow the sniffed type.
	req := multipartRequest(t, "file", "evil.html", tinyPNG)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	entries, err := os.ReadDir(storeDir)
	if err != nil {
		t.Fatalf("read store dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("stored file count = %d, want 1", len(entries))
	}
	if ext := filepath.Ext(entries[0].Name()); ext != ".png" {
		t.Fatalf("stored extension = %q, want .png (client claimed .html)", ext)
	}
}

func multipartRequestWithLeadingField(t *testing.T, fieldName, filename string, content []byte) *http.Request {
	t.Helper()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("note", "not a file"); err != nil {
		t.Fatalf("write leading field: %v", err)
	}
	part, err := mw.CreateFormFile(fieldName, filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("write content: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}

func TestValidatingHandlerSkipsNonFileFieldsBeforeFilePart(t *testing.T) {
	storeDir := t.TempDir()
	root, err := os.OpenRoot(storeDir)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer root.Close()

	handler := upload.NewValidatingHandler(root, int64(len(tinyPNG))+1<<10, t.Logf)

	req := multipartRequestWithLeadingField(t, "file", "photo.png", tinyPNG)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	entries, err := os.ReadDir(storeDir)
	if err != nil {
		t.Fatalf("read store dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("stored file count = %d, want 1 (the text field before it must not count as the file)", len(entries))
	}
}

func TestTrackedPipelineHandlerStoresFileWithGeneratedName(t *testing.T) {
	storeDir := t.TempDir()
	root, err := os.OpenRoot(storeDir)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer root.Close()

	handler := upload.NewTrackedPipelineHandler(root, int64(len(tinyPNG))+1<<10, t.Logf)

	req := multipartRequest(t, "file", "photo.png", tinyPNG)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	entries, err := os.ReadDir(storeDir)
	if err != nil {
		t.Fatalf("read store dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("stored file count = %d, want 1", len(entries))
	}

	got, err := os.ReadFile(filepath.Join(storeDir, entries[0].Name()))
	if err != nil {
		t.Fatalf("read stored file: %v", err)
	}
	if !bytes.Equal(got, tinyPNG) {
		t.Fatal("stored bytes differ from source PNG")
	}
}

func TestCreateInRootRefusesPathEscape(t *testing.T) {
	storeDir := t.TempDir()
	root, err := os.OpenRoot(storeDir)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer root.Close()

	if _, err := upload.CreateInRoot(root, "../../evil.png"); err == nil {
		t.Fatal("expected CreateInRoot to refuse a path escaping the root, got nil error")
	}
}
