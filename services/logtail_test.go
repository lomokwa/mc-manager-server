package services

import (
	"os"
	"testing"
)

func TestFileExists(t *testing.T) {
	dir := t.TempDir()
	present := dir + "/present.txt"
	os.WriteFile(present, []byte("x"), 0644)

	if !fileExists(present) {
		t.Error("expected fileExists to be true for an existing file")
	}
	if fileExists(dir + "/missing.txt") {
		t.Error("expected fileExists to be false for a missing file")
	}
}

// rotated() reads from the fixed LatestLogPath constant internally, so these
// tests set up real files there (via setupServerDir/writeServerFile) rather
// than passing in an arbitrary path.

func TestRotated_SameFileNotTruncated(t *testing.T) {
	setupServerDir(t)
	path := writeServerFile(t, "logs/latest.log", "hello\n")

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("failed to open file: %v", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		t.Fatalf("failed to stat file: %v", err)
	}

	if rotated(f, info) {
		t.Error("expected rotated to be false for an untouched file")
	}
}

func TestRotated_DifferentFileDetected(t *testing.T) {
	setupServerDir(t)
	path := writeServerFile(t, "logs/latest.log", "hello\n")

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("failed to open file: %v", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		t.Fatalf("failed to stat file: %v", err)
	}

	// Simulate Minecraft rotating the log: latest.log is removed and a new
	// file takes its place at the same path.
	if err := os.Remove(path); err != nil {
		t.Fatalf("failed to remove file: %v", err)
	}
	writeServerFile(t, "logs/latest.log", "a fresh session\n")

	if !rotated(f, info) {
		t.Error("expected rotated to be true when a new file now occupies the path")
	}
}

func TestRotated_TruncatedInPlace(t *testing.T) {
	setupServerDir(t)
	path := writeServerFile(t, "logs/latest.log", "hello world, this is a long line\n")

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("failed to open file: %v", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		t.Fatalf("failed to stat file: %v", err)
	}
	// Advance the read offset past where the file will be truncated to.
	f.Seek(20, 0)

	if err := os.Truncate(path, 5); err != nil {
		t.Fatalf("failed to truncate file: %v", err)
	}

	if !rotated(f, info) {
		t.Error("expected rotated to be true after in-place truncation")
	}
}

func TestRotated_MissingPathIsNotRotated(t *testing.T) {
	setupServerDir(t)
	path := writeServerFile(t, "logs/latest.log", "hello\n")

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("failed to open file: %v", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		t.Fatalf("failed to stat file: %v", err)
	}

	if err := os.Remove(path); err != nil {
		t.Fatalf("failed to remove file: %v", err)
	}

	// A briefly-missing path mid-rotation should be tolerated, not reported
	// as rotated, so the tailer waits rather than thrashing.
	if rotated(f, info) {
		t.Error("expected rotated to be false when the path is briefly missing")
	}
}

func TestGetLogHub_NonNilAfterStart(t *testing.T) {
	setupServerDir(t)
	StartLogTailer()
	if GetLogHub() == nil {
		t.Error("expected GetLogHub to be non-nil after StartLogTailer")
	}
}
