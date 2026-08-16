package services

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/lomokwa/mc-manager/types"
)

// writeControl signals mc-supervisor over this runtime's own control FIFO.
func (rt *ServerRuntime) writeControl(verb string) error {
	return writeFifo(rt.ControlFifoPath(), verb+"\n")
}

// SendCommand forwards a raw console command to Minecraft via the console
// FIFO this runtime's own "minecraft" container's mc-supervisor reads.
// Never blocks waiting for the JVM: if the server isn't running, it fails
// fast instead of hanging the caller's HTTP request.
func (rt *ServerRuntime) SendCommand(cmd string) error {
	if !rt.IsServerRunning() {
		return fmt.Errorf("server is not running")
	}
	rt.stdinMu.Lock()
	defer rt.stdinMu.Unlock()
	return writeFifo(rt.ConsoleFifoPath(), cmd+"\n")
}

// RestartServerProcess asks mc-supervisor to cycle the JVM. The supervisor has
// always understood the RESTART verb (see cmd/supervisor); nothing in Go had an
// exported path to it until the automation restart action needed one.
//
// Refuses when the server is already down: RESTART on a stopped server would
// START it, which is the opposite of what a rule that says "restart" means --
// and it would silently undo an operator's deliberate shutdown.
func (rt *ServerRuntime) RestartServerProcess() error {
	if !rt.IsServerRunning() {
		return fmt.Errorf("server is not running")
	}
	return rt.writeControl("RESTART")
}

// ReadStatus reads and decodes this runtime's raw status file as
// mc-supervisor last wrote it. Exported (not just an IsServerRunning
// implementation detail) because handlers/servers.go's server list needs
// the PID/Since fields too, not just the running bool -- see
// handlers.statusItemFor. ok is false for a missing or corrupt file, which
// simply means "no status to report" rather than an error worth failing a
// request over.
func (rt *ServerRuntime) ReadStatus() (types.ServerRuntimeStatus, bool) {
	b, err := os.ReadFile(rt.StatusFilePath())
	if err != nil {
		return types.ServerRuntimeStatus{}, false
	}
	var st types.ServerRuntimeStatus
	if err := json.Unmarshal(b, &st); err != nil {
		return types.ServerRuntimeStatus{}, false
	}
	return st, true
}

// IsServerRunning reports whether this runtime's JVM is up, per
// mc-supervisor's status file. A stale heartbeat (the minecraft container
// itself is dead, not just the JVM) is treated as not-running, so a crashed
// container can never be mistaken for a healthy server.
func (rt *ServerRuntime) IsServerRunning() bool {
	st, ok := rt.ReadStatus()
	if !ok || !st.Running {
		return false
	}
	return time.Since(st.Heartbeat) < 10*time.Second
}

// StartServerProcess signals mc-supervisor to start this runtime's JVM and
// waits for the world to finish loading, returning the same "Done (...)"
// line the old in-process implementation returned.
func (rt *ServerRuntime) StartServerProcess() (string, error) {
	if rt.IsServerRunning() {
		return "", fmt.Errorf("server already running")
	}

	hub := rt.Hub
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

	if err := rt.writeControl("START"); err != nil {
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
			_ = rt.writeControl("KILL")
			return "", fmt.Errorf("server failed to start within 120 seconds")
		}
	}
}

// StopServerProcess signals a graceful stop and waits for mc-supervisor to
// report this runtime's JVM down. The supervisor owns the actual "stop,
// wait, then kill" sequence (see cmd/supervisor) — this just waits
// comfortably past that timeout for the status file to catch up.
func (rt *ServerRuntime) StopServerProcess() (string, error) {
	if !rt.IsServerRunning() {
		return "", fmt.Errorf("server is not running")
	}
	if err := rt.writeControl("STOP"); err != nil {
		return "", fmt.Errorf("failed to signal stop: %w", err)
	}

	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		if !rt.IsServerRunning() {
			return "server stopped", nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return "", fmt.Errorf("server did not stop in time")
}

// --- Package-level wrappers over the default runtime ------------------------
//
// Everything below existed before Phase 1 introduced ServerRuntime and must
// keep behaving identically for the single server that exists today (see
// DefaultRuntime in runtime.go). A later phase's namespaced
// (/api/servers/:sid/...) handlers will call the ServerRuntime methods
// above directly instead of adding more of these.

func SendCommand(cmd string) error {
	return DefaultRuntime().SendCommand(cmd)
}

func IsServerRunning() bool {
	return DefaultRuntime().IsServerRunning()
}

func StartServerProcess() (string, error) {
	return DefaultRuntime().StartServerProcess()
}

func StopServerProcess() (string, error) {
	return DefaultRuntime().StopServerProcess()
}
