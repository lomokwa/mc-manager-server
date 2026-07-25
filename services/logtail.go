package services

import (
	"bufio"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/lomokwa/mc-manager/types"
)

const logPollInterval = 200 * time.Millisecond

var (
	tailHub  *types.LogHub
	tailOnce sync.Once
)

// StartLogTailer creates the process-wide log hub and begins following
// LatestLogPath. Call once at API startup. Unlike the old in-process
// exec.Cmd's stdout pump, this hub is never closed on server stop — the
// minecraft container (and its log file) can outlive any single API process,
// so the hub's lifetime now matches the API's, not the JVM's.
func StartLogTailer() {
	tailOnce.Do(func() {
		tailHub = types.NewLogHub()
		go tailLoop()
	})
}

// GetLogHub returns the long-lived hub fed by the tailer. Non-nil once
// StartLogTailer has been called (which main.go does at boot), so callers no
// longer need to guard against a nil hub between server starts.
func GetLogHub() *types.LogHub {
	return tailHub
}

// tailLoop follows LatestLogPath, broadcasting each complete line to tailHub.
// Minecraft rotates this file on every JVM start (a fresh file replaces the
// old one) — this has bitten the project before, so rotation is detected two
// ways rather than trusting file position alone: the file's identity
// (os.SameFile) or its size shrinking under our read offset (truncation).
// Either signals "reopen from the top", since the new session's own readiness
// line ("Done (...)") must not be missed.
func tailLoop() {
	var (
		f       *os.File
		reader  *bufio.Reader
		info    os.FileInfo
		partial []byte
	)

	// Seek to the end only if the file already exists right now — i.e. we're
	// attaching to a server that may already be running, so its prior output
	// shouldn't replay. A file that doesn't exist yet is a fresh session:
	// read it from the start once it appears, so the "Done" line isn't missed.
	seekToEndOnOpen := fileExists(LatestLogPath)

	openCurrent := func() bool {
		nf, err := os.Open(LatestLogPath)
		if err != nil {
			return false
		}
		fi, err := nf.Stat()
		if err != nil {
			nf.Close()
			return false
		}
		if seekToEndOnOpen {
			nf.Seek(0, io.SeekEnd)
			seekToEndOnOpen = false
		}
		f, reader, info, partial = nf, bufio.NewReader(nf), fi, nil
		return true
	}

	for {
		if f == nil {
			if !openCurrent() {
				time.Sleep(time.Second)
				continue
			}
		}

		for {
			chunk, err := reader.ReadBytes('\n')
			if len(chunk) > 0 {
				partial = append(partial, chunk...)
				if partial[len(partial)-1] == '\n' {
					tailHub.Broadcast(strings.TrimRight(string(partial), "\r\n"))
					partial = nil
				}
			}
			if err != nil {
				break // caught up (EOF) or a real read error — either way, stop draining
			}
		}

		if rotated(f, info) {
			f.Close()
			f = nil // reopened on the next loop iteration, from offset 0
			continue
		}
		time.Sleep(logPollInterval)
	}
}

func rotated(f *os.File, openedInfo os.FileInfo) bool {
	pathInfo, err := os.Stat(LatestLogPath)
	if err != nil {
		return false // briefly missing mid-rotation — wait rather than thrash
	}
	if !os.SameFile(openedInfo, pathInfo) {
		return true // the path now points at a different file
	}
	curInfo, err := f.Stat()
	if err != nil {
		return true
	}
	offset, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		return true
	}
	return curInfo.Size() < offset // truncated in place
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
