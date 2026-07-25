//go:build linux

package services

import (
	"fmt"
	"os"
	"syscall"
)

// writeFifo writes payload to the named pipe at path. It opens non-blocking
// first so a missing reader (the minecraft container down, or mc-supervisor
// not up yet) returns ENXIO immediately instead of hanging the caller's HTTP
// request; once a reader is confirmed present, the actual write is a normal
// blocking write of a payload well under PIPE_BUF, which POSIX guarantees is
// atomic — a crash mid-write can never deliver a corrupt partial line.
func writeFifo(path, payload string) error {
	fd, err := syscall.Open(path, syscall.O_WRONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return fmt.Errorf("server control channel unavailable: %w", err)
	}
	if err := syscall.SetNonblock(fd, false); err != nil {
		syscall.Close(fd)
		return fmt.Errorf("server control channel unavailable: %w", err)
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	_, err = f.WriteString(payload)
	return err
}
