//go:build linux

package services

import (
	"os"
	"syscall"
	"testing"
	"time"
)

func TestWriteFifo_NoReaderReturnsError(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/test.fifo"
	if err := syscall.Mkfifo(path, 0644); err != nil {
		t.Fatalf("failed to create fifo: %v", err)
	}

	// No reader is open on the other end, so the non-blocking open used by
	// writeFifo must fail fast (ENXIO) instead of hanging.
	done := make(chan error, 1)
	go func() { done <- writeFifo(path, "hello\n") }()

	select {
	case err := <-done:
		if err == nil {
			t.Error("expected an error when no reader is present")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("writeFifo blocked instead of failing fast with no reader")
	}
}

func TestWriteFifo_DeliversPayloadToReader(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/test.fifo"
	if err := syscall.Mkfifo(path, 0644); err != nil {
		t.Fatalf("failed to create fifo: %v", err)
	}

	readDone := make(chan string, 1)
	readErr := make(chan error, 1)
	go func() {
		f, err := os.OpenFile(path, os.O_RDONLY, 0)
		if err != nil {
			readErr <- err
			return
		}
		defer f.Close()
		buf := make([]byte, 64)
		n, err := f.Read(buf)
		if err != nil {
			readErr <- err
			return
		}
		readDone <- string(buf[:n])
	}()

	// Give the reader goroutine a moment to open the FIFO for reading before
	// writeFifo attempts its own non-blocking open.
	time.Sleep(100 * time.Millisecond)

	if err := writeFifo(path, "hello\n"); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	select {
	case got := <-readDone:
		if got != "hello\n" {
			t.Errorf("expected 'hello\\n', got %q", got)
		}
	case err := <-readErr:
		t.Fatalf("reader failed: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the reader to receive the payload")
	}
}

func TestWriteFifo_MissingPath(t *testing.T) {
	if err := writeFifo("/nonexistent/path/to.fifo", "hello\n"); err == nil {
		t.Error("expected an error for a nonexistent path")
	}
}
