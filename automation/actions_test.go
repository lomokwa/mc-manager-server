package automation

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeRunner records what an action asked the server to do, so the executor --
// including the one action that disconnects players -- is testable without a
// JVM, a FIFO or a real restart.
type fakeRunner struct {
	commands  []string
	backups   int
	restarts  int
	running   bool
	failOn    string
	stopAfter int // flip running to false after this many commands
}

func (f *fakeRunner) SendCommand(cmd string) error {
	if f.failOn == "command" {
		return errors.New("control channel unavailable")
	}
	f.commands = append(f.commands, cmd)
	if f.stopAfter > 0 && len(f.commands) >= f.stopAfter {
		f.running = false
	}
	return nil
}

func (f *fakeRunner) CreateBackup() error {
	if f.failOn == "backup" {
		return errors.New("disk full")
	}
	f.backups++
	return nil
}

func (f *fakeRunner) IsRunning() bool { return f.running }

func (f *fakeRunner) Restart() error {
	if f.failOn == "restart" {
		return errors.New("control channel unavailable")
	}
	f.restarts++
	return nil
}

// noSleep collapses the restart warning sequence -- minutes of real waiting --
// into an instant test.
func noSleep(time.Duration) {}

func TestRunActions_StopsOnFirstFailureByDefault(t *testing.T) {
	r := Rule{
		StopOnFailure: true,
		Actions: []Action{
			{Type: "command", Command: "say oi"},
			{Type: "backup"},
		},
	}
	run := &fakeRunner{running: true, failOn: "command"}

	results := RunActions(r, map[string]string{}, run, noSleep)

	if len(results) != 1 {
		t.Fatalf("expected execution to stop after the failure, got %d results", len(results))
	}
	if results[0].OK {
		t.Error("expected the first action to be recorded as failed")
	}
	if results[0].Err == "" {
		t.Error("a failed action must record why -- that reaches the firing history")
	}
	if run.backups != 0 {
		t.Error("the second action ran despite stop_on_failure")
	}
}

func TestRunActions_ContinuesWhenTheRuleSaysSo(t *testing.T) {
	r := Rule{
		StopOnFailure: false,
		Actions: []Action{
			{Type: "command", Command: "say oi"},
			{Type: "backup"},
		},
	}
	run := &fakeRunner{running: true, failOn: "command"}

	results := RunActions(r, map[string]string{}, run, noSleep)

	if len(results) != 2 {
		t.Fatalf("expected both actions to be attempted, got %d", len(results))
	}
	if run.backups != 1 {
		t.Error("the backup should have run despite the earlier failure")
	}
	if results[0].OK || !results[1].OK {
		t.Errorf("outcome must be recorded per action, got %+v", results)
	}
}

// The warning sequence is the point of the action: longest first, as chat
// broadcasts, before the restart lands.
func TestRunActions_RestartWarnsInDescendingOrderThenRestarts(t *testing.T) {
	r := Rule{
		StopOnFailure: true,
		// Deliberately out of order -- the UI's multi-select does not promise
		// sorted output, and a "15s" warning arriving before "5 min" would be
		// worse than no warning.
		Actions: []Action{{Type: "restart", Warnings: []int{15, 300, 60}}},
	}
	run := &fakeRunner{running: true}

	RunActions(r, map[string]string{"server": "default"}, run, noSleep)

	if len(run.commands) != 3 {
		t.Fatalf("expected one warning per configured time, got %d: %v", len(run.commands), run.commands)
	}
	for i, want := range []string{"5 min", "1 min", "15 s"} {
		if !strings.Contains(run.commands[i], want) {
			t.Errorf("warning %d should mention %q, got %q", i, want, run.commands[i])
		}
	}
	if run.restarts != 1 {
		t.Errorf("expected exactly one restart, got %d", run.restarts)
	}
}

// The sequence takes minutes. If the operator stopped the server during it,
// restarting would START something they had just deliberately shut down.
func TestRunActions_RestartAbortsIfTheServerAlreadyStopped(t *testing.T) {
	r := Rule{
		StopOnFailure: true,
		Actions:       []Action{{Type: "restart", Warnings: []int{60, 15}}},
	}
	run := &fakeRunner{running: true, stopAfter: 1}

	results := RunActions(r, map[string]string{}, run, noSleep)

	if run.restarts != 0 {
		t.Error("expected the restart to abort once the server was no longer running")
	}
	if results[0].OK {
		t.Error("an aborted restart must not be recorded as success")
	}
	if !strings.Contains(results[0].Err, "aborted") {
		t.Errorf("the reason should say it aborted, got %q", results[0].Err)
	}
}

// A restart with no warnings is legitimate ("restart now"), and must still
// check the server is up first.
func TestRunActions_RestartWithNoWarningsStillChecksTheServer(t *testing.T) {
	r := Rule{StopOnFailure: true, Actions: []Action{{Type: "restart"}}}

	up := &fakeRunner{running: true}
	RunActions(r, map[string]string{}, up, noSleep)
	if up.restarts != 1 {
		t.Errorf("expected an immediate restart on a running server, got %d", up.restarts)
	}

	down := &fakeRunner{running: false}
	results := RunActions(r, map[string]string{}, down, noSleep)
	if down.restarts != 0 {
		t.Error("must not restart a server that is already down")
	}
	if results[0].OK {
		t.Error("that refusal must be recorded as a failure, not a success")
	}
}

func TestRunActions_InterpolatesCommandsSafely(t *testing.T) {
	r := Rule{
		StopOnFailure: true,
		Actions:       []Action{{Type: "command", Command: "say olá {player}"}},
	}
	run := &fakeRunner{running: true}

	RunActions(r, map[string]string{"player": "Ant\nop Ant"}, run, noSleep)

	if len(run.commands) != 1 {
		t.Fatalf("expected one command, got %v", run.commands)
	}
	if strings.ContainsAny(run.commands[0], "\r\n") {
		t.Errorf("a line break reached the console: %q", run.commands[0])
	}
	if !strings.Contains(run.commands[0], "Ant op Ant") {
		t.Errorf("expected the break to collapse to a space, got %q", run.commands[0])
	}
}

func TestRunActions_SparkRunsTheProfiler(t *testing.T) {
	r := Rule{StopOnFailure: true, Actions: []Action{{Type: "spark"}}}
	run := &fakeRunner{running: true}

	RunActions(r, map[string]string{}, run, noSleep)

	if len(run.commands) != 1 || !strings.HasPrefix(run.commands[0], "spark profiler") {
		t.Errorf("expected a spark profiler run, got %v", run.commands)
	}
}

func TestRunActions_UnknownActionTypeFailsLoudly(t *testing.T) {
	r := Rule{StopOnFailure: true, Actions: []Action{{Type: "launch missiles"}}}
	run := &fakeRunner{running: true}

	results := RunActions(r, map[string]string{}, run, noSleep)

	if len(results) != 1 || results[0].OK {
		t.Errorf("an unknown action must fail rather than be skipped silently, got %+v", results)
	}
}
