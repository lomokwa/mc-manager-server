package services

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/lomokwa/mc-manager/types"
)

func writeStatus(t *testing.T, status types.ServerRuntimeStatus) {
	t.Helper()
	data, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("failed to marshal status: %v", err)
	}
	writeServerFile(t, ".mcmanager/status.json", string(data))
}

func TestIsServerRunning_NoStatusFile(t *testing.T) {
	setupServerDir(t)

	if IsServerRunning() {
		t.Error("expected IsServerRunning to be false with no status file")
	}
}

func TestIsServerRunning_MalformedStatusFile(t *testing.T) {
	setupServerDir(t)
	writeServerFile(t, ".mcmanager/status.json", "{not json")

	if IsServerRunning() {
		t.Error("expected IsServerRunning to be false with malformed status file")
	}
}

func TestIsServerRunning_RunningFreshHeartbeat(t *testing.T) {
	setupServerDir(t)
	writeStatus(t, types.ServerRuntimeStatus{Running: true, Heartbeat: time.Now()})

	if !IsServerRunning() {
		t.Error("expected IsServerRunning to be true with a fresh heartbeat")
	}
}

func TestIsServerRunning_RunningStaleHeartbeat(t *testing.T) {
	setupServerDir(t)
	writeStatus(t, types.ServerRuntimeStatus{Running: true, Heartbeat: time.Now().Add(-30 * time.Second)})

	if IsServerRunning() {
		t.Error("expected IsServerRunning to be false with a stale heartbeat")
	}
}

func TestIsServerRunning_NotRunningFlag(t *testing.T) {
	setupServerDir(t)
	writeStatus(t, types.ServerRuntimeStatus{Running: false, Heartbeat: time.Now()})

	if IsServerRunning() {
		t.Error("expected IsServerRunning to be false when Running=false")
	}
}

func TestSendCommand_ServerNotRunning(t *testing.T) {
	setupServerDir(t)

	if err := SendCommand("say hello"); err == nil {
		t.Error("expected an error when the server is not running")
	}
}

func TestStartServerProcess_AlreadyRunning(t *testing.T) {
	setupServerDir(t)
	writeStatus(t, types.ServerRuntimeStatus{Running: true, Heartbeat: time.Now()})

	if _, err := StartServerProcess(); err == nil {
		t.Error("expected an error when the server is already running")
	}
}

func TestStopServerProcess_NotRunning(t *testing.T) {
	setupServerDir(t)

	if _, err := StopServerProcess(); err == nil {
		t.Error("expected an error when the server is not running")
	}
}
