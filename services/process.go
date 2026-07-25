package services

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/lomokwa/mc-manager/types"
)

// stdinMu serializes this API process's own writes to the console FIFO. Cross
// -container write atomicity for a single line is already guaranteed by POSIX
// (writes under PIPE_BUF are atomic), but this keeps one API instance's own
// command order tidy.
var stdinMu sync.Mutex

func writeControl(verb string) error {
	return writeFifo(ControlFifoPath, verb+"\n")
}

// SendCommand forwards a raw console command to Minecraft via the console
// FIFO the "minecraft" container's mc-supervisor reads. Never blocks waiting
// for the JVM: if the server isn't running, it fails fast instead of hanging
// the caller's HTTP request.
func SendCommand(cmd string) error {
	if !IsServerRunning() {
		return fmt.Errorf("server is not running")
	}
	stdinMu.Lock()
	defer stdinMu.Unlock()
	return writeFifo(ConsoleFifoPath, cmd+"\n")
}

func readStatus() (types.ServerRuntimeStatus, bool) {
	b, err := os.ReadFile(StatusFilePath)
	if err != nil {
		return types.ServerRuntimeStatus{}, false
	}
	var st types.ServerRuntimeStatus
	if err := json.Unmarshal(b, &st); err != nil {
		return types.ServerRuntimeStatus{}, false
	}
	return st, true
}

// IsServerRunning reports whether the JVM is up, per mc-supervisor's status
// file. A stale heartbeat (the minecraft container itself is dead, not just
// the JVM) is treated as not-running, so a crashed container can never be
// mistaken for a healthy server.
func IsServerRunning() bool {
	st, ok := readStatus()
	if !ok || !st.Running {
		return false
	}
	return time.Since(st.Heartbeat) < 10*time.Second
}

// StartServerProcess signals mc-supervisor to start the JVM and waits for the
// world to finish loading, returning the same "Done (...)" line the old
// in-process implementation returned.
func StartServerProcess() (string, error) {
	if IsServerRunning() {
		return "", fmt.Errorf("server already running")
	}

	hub := GetLogHub()
	if hub == nil {
		return "", fmt.Errorf("log system not ready")
	}
	ch := hub.Subscribe()
	defer hub.Unsubscribe(ch)

	// Discard whatever's still buffered from a previous session before
	// signaling start, so a stale "Done" line can't be mistaken for this one.
drain:
	for {
		select {
		case <-ch:
		default:
			break drain
		}
	}

	if err := writeControl("START"); err != nil {
		return "", fmt.Errorf("failed to signal start: %w", err)
	}

	for {
		select {
		case line, ok := <-ch:
			if !ok {
				return "", fmt.Errorf("log stream closed before the server became ready")
			}
			if strings.Contains(line, "]: Done (") {
				return line, nil
			}
		case <-time.After(120 * time.Second):
			_ = writeControl("KILL")
			return "", fmt.Errorf("server failed to start within 120 seconds")
		}
	}
}

// StopServerProcess signals a graceful stop and waits for mc-supervisor to
// report the JVM down. The supervisor owns the actual "stop, wait, then kill"
// sequence (see cmd/supervisor) — this just waits comfortably past that
// timeout for the status file to catch up.
func StopServerProcess() (string, error) {
	if !IsServerRunning() {
		return "", fmt.Errorf("server is not running")
	}
	if err := writeControl("STOP"); err != nil {
		return "", fmt.Errorf("failed to signal stop: %w", err)
	}

	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		if !IsServerRunning() {
			return "server stopped", nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return "", fmt.Errorf("server did not stop in time")
}
