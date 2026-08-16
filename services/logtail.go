package services

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"strings"
	"time"

	"github.com/lomokwa/mc-manager/types"
)

const logPollInterval = 200 * time.Millisecond

const (
	// backlogMaxLines matches the hub's replay buffer size — seeding more
	// than the hub can hold would just be dropped again on subscribe.
	backlogMaxLines = 200
	// backlogWindowBytes bounds how much of a large log is read to find those
	// lines. 256 KiB comfortably covers 200 Minecraft log lines (~100 bytes
	// each is typical; even pathological stack traces fit).
	backlogWindowBytes = 256 * 1024
)

// startRuntimeTailer creates rt's log hub and begins following rt's
// LatestLogPath, exactly once per runtime (rt.tailOnce). Unlike the old
// in-process exec.Cmd's stdout pump, this hub is never closed on server
// stop -- the minecraft container (and its log file) can outlive any single
// API process, so the hub's lifetime now matches the API's, not the JVM's.
func startRuntimeTailer(rt *ServerRuntime) {
	rt.tailOnce.Do(func() {
		rt.Hub = types.NewLogHub()
		go tailLoopFor(rt)
	})
}

// StartLogTailer starts the default server's log tailer. Kept for every
// pre-Phase-1 caller (and its own tests): LoadRuntimes now does this for
// every registry row including the default, so this is just
// startRuntimeTailer(DefaultRuntime()) under a stable, older name.
func StartLogTailer() {
	startRuntimeTailer(DefaultRuntime())
}

// GetLogHub returns the default server's long-lived hub. Non-nil once
// StartLogTailer/LoadRuntimes has run (which main.go does at boot), so
// pre-Phase-1 callers no longer need to guard against a nil hub between
// server starts -- same contract as before Phase 1.
func GetLogHub() *types.LogHub {
	return DefaultRuntime().Hub
}

// tailLoopFor follows rt's LatestLogPath, broadcasting each complete line to
// rt.Hub. Minecraft rotates this file on every JVM start (a fresh file
// replaces the old one) -- this has bitten the project before, so rotation
// is detected two ways rather than trusting file position alone: the
// file's identity (os.SameFile) or its size shrinking under our read offset
// (truncation). Either signals "reopen from the top", since the new
// session's own readiness line ("Done (...)") must not be missed.
func tailLoopFor(rt *ServerRuntime) {
	var (
		f       *os.File
		reader  *bufio.Reader
		info    os.FileInfo
		partial []byte
	)

	path := rt.LatestLogPath()

	// If the file already exists right now, we're attaching to a server that
	// may already be running — seed the hub's replay buffer with the tail of
	// its existing output (so a console opened right after an API restart is
	// never blank while the JVM sits mid-session), then continue tailing from
	// there. A file that doesn't exist yet is a fresh session: read it from
	// the start once it appears, so the "Done" line isn't missed.
	seedBacklogOnOpen := fileExists(path)

	openCurrent := func() bool {
		nf, err := os.Open(path)
		if err != nil {
			return false
		}
		fi, err := nf.Stat()
		if err != nil {
			nf.Close()
			return false
		}
		if seedBacklogOnOpen {
			for _, line := range readBacklog(nf) {
				rt.Hub.Broadcast(line)
			}
			seedBacklogOnOpen = false
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
					line := strings.TrimRight(string(partial), "\r\n")
					rt.Hub.Broadcast(line)
					// Live lines only -- the backlog replay above deliberately
					// stays off the bus. HasSubscribers keeps this free when no
					// automation engine is listening, which is the common case.
					if types.Bus.HasSubscribers() {
						types.Bus.Publish(types.ConsoleLineEvent{ServerID: rt.ID, Line: line, At: time.Now()})
					}
					partial = nil
				}
			}
			if err != nil {
				break // caught up (EOF) or a real read error — either way, stop draining
			}
		}

		if rotated(f, info, path) {
			f.Close()
			f = nil // reopened on the next loop iteration, from offset 0
			continue
		}
		time.Sleep(logPollInterval)
	}
}

// readBacklog returns up to backlogMaxLines complete lines from the end of f,
// reading at most backlogWindowBytes, and positions f so tailing resumes
// exactly where the returned lines stop: at EOF when the file ends in a
// newline, or at the start of the trailing partial line the JVM is still
// writing — that way the partial is later emitted once, whole, by the normal
// tail loop instead of being split across the seed and the tail.
func readBacklog(f *os.File) []string {
	fi, err := f.Stat()
	if err != nil {
		f.Seek(0, io.SeekEnd)
		return nil
	}

	start := fi.Size() - backlogWindowBytes
	windowTruncated := start > 0
	if start < 0 {
		start = 0
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		f.Seek(0, io.SeekEnd)
		return nil
	}
	data, err := io.ReadAll(f) // leaves the offset at EOF
	if err != nil {
		f.Seek(0, io.SeekEnd)
		return nil
	}

	lastNL := bytes.LastIndexByte(data, '\n')
	if lastNL < 0 {
		// No complete line in the window. A small file whose first line is
		// still being written: rewind so the tail loop emits it whole later.
		// (With windowTruncated this would be one >256KiB line — degenerate;
		// leaving the offset at EOF just skips the unreadable fragment.)
		if !windowTruncated {
			f.Seek(start, io.SeekStart)
		}
		return nil
	}
	if lastNL < len(data)-1 {
		// Trailing partial line: hand it back to the tail loop.
		f.Seek(start+int64(lastNL)+1, io.SeekStart)
	}

	lines := strings.Split(string(data[:lastNL]), "\n")
	if windowTruncated && len(lines) > 0 {
		lines = lines[1:] // the window almost certainly opened mid-line
	}
	if len(lines) > backlogMaxLines {
		lines = lines[len(lines)-backlogMaxLines:]
	}
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], "\r")
	}
	return lines
}

// rotated reports whether path no longer refers to the file f was opened
// from. path is passed explicitly (rather than read from a fixed constant)
// so the same check works for every runtime's own LatestLogPath, not just
// the default server's.
func rotated(f *os.File, openedInfo os.FileInfo, path string) bool {
	pathInfo, err := os.Stat(path)
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
