package automation

import (
	"fmt"
	"sort"
	"time"

	"github.com/lomokwa/mc-manager/services"
)

// ActionRunner is everything the executor needs from a server.
//
// An interface rather than *services.ServerRuntime for one reason: the restart
// action disconnects players, and the only responsible way to exercise it is
// without a JVM. Every guard around it -- descending warnings, aborting when
// the server went down mid-sequence -- is a unit test because of this seam.
type ActionRunner interface {
	SendCommand(cmd string) error
	CreateBackup() error
	IsRunning() bool
	Restart() error
}

type runtimeRunner struct{ rt *services.ServerRuntime }

func (r runtimeRunner) SendCommand(cmd string) error { return r.rt.SendCommand(cmd) }
func (r runtimeRunner) IsRunning() bool              { return r.rt.IsServerRunning() }
func (r runtimeRunner) Restart() error               { return r.rt.RestartServerProcess() }

func (r runtimeRunner) CreateBackup() error {
	_, err := r.rt.CreateBackup()
	return err
}

// NewRuntimeRunner adapts a live server for the executor.
func NewRuntimeRunner(rt *services.ServerRuntime) ActionRunner { return runtimeRunner{rt: rt} }

// ActionResult is one row of a firing's outcome. Recorded per action so the UI
// can say which step failed instead of "it errored".
type ActionResult struct {
	Type string `json:"action"`
	OK   bool   `json:"ok"`
	Err  string `json:"error,omitempty"`
}

// RunActions executes a rule's actions in order.
//
// sleep is injected so the restart warning sequence -- minutes of real
// waiting -- collapses to nothing under test.
func RunActions(r Rule, vars map[string]string, run ActionRunner, sleep func(time.Duration)) []ActionResult {
	results := make([]ActionResult, 0, len(r.Actions))
	for _, a := range r.Actions {
		err := runOne(a, vars, run, sleep)
		res := ActionResult{Type: a.Type, OK: err == nil}
		if err != nil {
			res.Err = err.Error()
		}
		results = append(results, res)

		// stop_on_failure defaults to true, and the case it protects is
		// [warn on Discord] -> [restart] with a dead webhook: nobody gets
		// disconnected without having been told.
		if err != nil && r.StopOnFailure {
			break
		}
	}
	return results
}

func runOne(a Action, vars map[string]string, run ActionRunner, sleep func(time.Duration)) error {
	switch a.Type {
	case "discord":
		url, err := WebhookURL(a.WebhookID)
		if err != nil {
			return err
		}
		status, err := PostDiscord(url, Interpolate(a.Message, vars, false), a.Mention)
		// Record the attempt either way: a 401 has to become visible in the
		// UI, and a delivery that worked has to clear a previous failure.
		if recErr := RecordWebhookResult(a.WebhookID, status, errString(err)); recErr != nil && err == nil {
			return recErr
		}
		return err

	case "command":
		return run.SendCommand(Interpolate(a.Command, vars, true))

	case "backup":
		return run.CreateBackup()

	case "spark":
		// Fixed string, no interpolation: nothing here is caller-controlled,
		// so there is nothing to sanitise and nothing to get wrong.
		return run.SendCommand("spark profiler --timeout 30")

	case "restart":
		return runRestart(a, run, sleep)
	}
	return fmt.Errorf("unknown action type %q", a.Type)
}

// runRestart broadcasts each configured warning, longest first, waiting the gap
// between them, and only then restarts.
//
// Two things exist only because the sequence takes minutes. The warnings are
// sorted rather than trusted: the UI's multi-select does not promise order, and
// "15s" arriving before "5 min" is worse than no warning at all. And IsRunning
// is re-checked before the restart lands -- if the operator stopped the server
// meanwhile, restarting would START something they had just deliberately shut
// down.
func runRestart(a Action, run ActionRunner, sleep func(time.Duration)) error {
	warns := append([]int(nil), a.Warnings...)
	sort.Sort(sort.Reverse(sort.IntSlice(warns)))

	prev := 0
	for i, w := range warns {
		if i > 0 {
			sleep(time.Duration(prev-w) * time.Second)
		}
		if !run.IsRunning() {
			return fmt.Errorf("server stopped during the warning sequence -- restart aborted")
		}
		if err := run.SendCommand(fmt.Sprintf("say Reinício em %s", humanSeconds(w))); err != nil {
			return err
		}
		prev = w
	}
	if len(warns) > 0 {
		sleep(time.Duration(prev) * time.Second)
	}

	if !run.IsRunning() {
		return fmt.Errorf("server stopped during the warning sequence -- restart aborted")
	}
	return run.Restart()
}

func humanSeconds(s int) string {
	if s >= 60 && s%60 == 0 {
		return fmt.Sprintf("%d min", s/60)
	}
	return fmt.Sprintf("%d s", s)
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
