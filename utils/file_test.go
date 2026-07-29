package utils

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestFileExists_ReturnsTrueForExistingFile(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "exists.txt")
	if err := os.WriteFile(filePath, []byte("content"), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	if !FileExists(filePath) {
		t.Error("expected FileExists to return true for existing file")
	}
}

func TestFileExists_ReturnsFalseForMissingFile(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "missing.txt")

	if FileExists(filePath) {
		t.Error("expected FileExists to return false for missing file")
	}
}

func TestFileExists_ReturnsTrueForDirectory(t *testing.T) {
	dir := t.TempDir()

	if !FileExists(dir) {
		t.Error("expected FileExists to return true for an existing directory")
	}
}

func TestWriteFile_CreatesFileWithContent(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "output.txt")
	content := []byte("hello world")

	if err := WriteFile(filePath, content); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	got, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("failed to read written file: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("expected content %q, got %q", content, got)
	}
}

func TestWriteFile_CreatesMissingParentDirectories(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "nested", "sub", "output.txt")

	if err := WriteFile(filePath, []byte("data")); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if !FileExists(filePath) {
		t.Error("expected nested file to be created")
	}
}

func TestWriteFile_OverwritesExistingFile(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "output.txt")

	if err := WriteFile(filePath, []byte("first")); err != nil {
		t.Fatalf("unexpected error on first write: %v", err)
	}
	if err := WriteFile(filePath, []byte("second")); err != nil {
		t.Fatalf("unexpected error on second write: %v", err)
	}

	got, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("failed to read written file: %v", err)
	}
	if string(got) != "second" {
		t.Errorf("expected content to be overwritten with 'second', got %q", got)
	}
}

func TestDownloadFile_DownloadsContentSuccessfully(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("downloaded content"))
	}))
	defer server.Close()

	dir := t.TempDir()
	filePath := filepath.Join(dir, "downloaded.txt")

	if err := DownloadFile(server.URL, filePath); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	got, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("failed to read downloaded file: %v", err)
	}
	if string(got) != "downloaded content" {
		t.Errorf("expected 'downloaded content', got %q", got)
	}
}

func TestDownloadFile_CreatesMissingParentDirectories(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("data"))
	}))
	defer server.Close()

	dir := t.TempDir()
	filePath := filepath.Join(dir, "nested", "sub", "downloaded.txt")

	if err := DownloadFile(server.URL, filePath); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !FileExists(filePath) {
		t.Error("expected nested file to be created")
	}
}

func TestDownloadFile_InvalidURL(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "output.txt")

	err := DownloadFile("http://invalid.local.invalid:0/nonexistent", filePath)
	if err == nil {
		t.Error("expected an error for an invalid URL")
	}
}
