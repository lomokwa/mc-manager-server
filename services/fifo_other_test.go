//go:build !linux

package services

import "testing"

// writeFifo is only meaningful on linux (see fifo_other.go); on any other
// platform it must always fail so callers get a clear error instead of a
// silent no-op.
func TestWriteFifo_UnsupportedOnNonLinux(t *testing.T) {
	if err := writeFifo("/tmp/anything", "hello\n"); err == nil {
		t.Error("expected writeFifo to return an error on non-linux platforms")
	}
}
