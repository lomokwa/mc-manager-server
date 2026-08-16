package automation

import (
	"strings"
	"testing"
	"time"

	"github.com/lomokwa/mc-manager/types"
)

// These are the loop the earlier tests missed. Every test above exercises
// Evaluate with hand-built state; none of them fired a rule and then checked
// that the NEXT event saw the consequence. That gap hid three severe bugs at
// once -- cooldown, schedules and thresholds all silently did nothing.

func TestLoop_CooldownHoldsAcrossRepeatedEvents(t *testing.T) {
	setupTestDB(t)
	run := &fakeRunner{running: true}
	whID, err := CreateWebhook(Webhook{Name: "w", URL: "http://127.0.0.1:1/x"})
	if err != nil {
		t.Fatalf("CreateWebhook: %v", err)
	}

	// Discord-only and deaf window off, so nothing but the cooldown can block.
	r := consoleRuleFor("boom", Action{Type: "discord", WebhookID: whID, Message: "m"})
	r.CooldownSeconds = 3600
	r.DeafWindowSeconds = 0
	r.StopOnFailure = false
	id, err := CreateRule(r)
	if err != nil {
		t.Fatalf("CreateRule: %v", err)
	}

	e := newTestEngine(run)
	if err := e.ReloadRules(); err != nil {
		t.Fatalf("ReloadRules: %v", err)
	}
	for i := 0; i < 5; i++ {
		e.HandleEvent(types.ConsoleLineEvent{ServerID: "default", Line: "boom", At: time.Now()})
		e.waitIdle()
	}

	firings, err := ListFirings(id, 20)
	if err != nil {
		t.Fatalf("ListFirings: %v", err)
	}
	if len(firings) != 1 {
		t.Errorf("a 1h cooldown must allow exactly one firing from 5 events, got %d", len(firings))
	}
}

func TestLoop_ScheduleFiresOncePerPeriod(t *testing.T) {
	setupTestDB(t)
	run := &fakeRunner{running: true}

	r := sampleRule()
	r.TriggerKind = "sched"
	r.TriggerConfig = map[string]any{"mode": "every", "hours": 6.0}
	r.Actions = []Action{{Type: "backup"}}
	r.CooldownSeconds = 0
	r.DeafWindowSeconds = 0
	if _, err := CreateRule(r); err != nil {
		t.Fatalf("CreateRule: %v", err)
	}

	e := newTestEngine(run)
	if err := e.ReloadRules(); err != nil {
		t.Fatalf("ReloadRules: %v", err)
	}
	// The tick arrives every minute. Five ticks is five minutes, nowhere near
	// six hours.
	for i := 0; i < 5; i++ {
		e.HandleEvent(types.ScheduleTick{At: time.Now()})
		e.waitIdle()
	}

	if run.backups != 1 {
		t.Errorf("an every-6h rule must back up once in five minutes, got %d", run.backups)
	}
}

// The dangerous one. A threshold that stays held must not re-fire on every
// sample: with cooldown defaulting to 0, "TPS below 15 -> restart" would
// become a restart loop on a live server.
func TestLoop_HeldThresholdDoesNotRefireOnEverySample(t *testing.T) {
	setupTestDB(t)
	run := &fakeRunner{running: true}

	r := sampleRule()
	r.TriggerKind = "tps"
	r.TriggerConfig = map[string]any{"below": 15.0, "for_seconds": 1.0}
	r.Actions = []Action{{Type: "command", Command: "say lag"}}
	r.CooldownSeconds = 0
	r.DeafWindowSeconds = 0
	if _, err := CreateRule(r); err != nil {
		t.Fatalf("CreateRule: %v", err)
	}

	e := newTestEngine(run)
	if err := e.ReloadRules(); err != nil {
		t.Fatalf("ReloadRules: %v", err)
	}

	base := time.Now()
	for i := 0; i < 6; i++ {
		at := base.Add(time.Duration(i*10) * time.Second)
		e.now = func() time.Time { return at }
		e.HandleEvent(types.SampleEvent{ServerID: "default", Kind: types.SampleTPS, Value: 5, At: at})
		e.waitIdle()
	}

	// Sample 1 opens the window; sample 2 fires it. After firing, the window
	// restarts, so the rule may fire again only after another full window --
	// not on every subsequent sample.
	if run.commands == nil || len(run.commands) > 3 {
		t.Errorf("a held threshold re-fired on nearly every sample: %d commands", len(run.commands))
	}
}

// first_time_only must stop welcoming a player the moment they have joined
// once, not at the next reload.
func TestLoop_FirstTimeOnlyStopsAfterTheFirstJoin(t *testing.T) {
	setupTestDB(t)
	run := &fakeRunner{running: true}

	r := sampleRule()
	r.TriggerKind = "join"
	r.TriggerConfig = map[string]any{"first_time_only": true}
	r.Actions = []Action{{Type: "command", Command: "say bem-vindo {player}"}}
	r.CooldownSeconds = 0
	r.DeafWindowSeconds = 0
	if _, err := CreateRule(r); err != nil {
		t.Fatalf("CreateRule: %v", err)
	}

	e := newTestEngine(run)
	if err := e.ReloadRules(); err != nil {
		t.Fatalf("ReloadRules: %v", err)
	}

	line := types.ConsoleLineEvent{
		ServerID: "default",
		Line:     "[12:00:00] [Server thread/INFO]: Notch joined the game",
		At:       time.Now(),
	}
	for i := 0; i < 3; i++ {
		e.HandleEvent(line)
		e.waitIdle()
	}

	if len(run.commands) != 1 {
		t.Errorf("first_time_only welcomed the same player on every relog: %d times", len(run.commands))
	}
}

// SampleInterval existed but nothing called it: the sampler's ticker was
// hardcoded to the floor, so the documented "interval derived from the rule's
// window" cost control did nothing. A 10-minute window was polled six times
// more often than intended, and every one of those polls makes the JVM run
// `spark tps`.
func TestLoop_SamplerIntervalFollowsTheRules(t *testing.T) {
	setupTestDB(t)
	e := newTestEngine(&fakeRunner{})

	if err := e.ReloadRules(); err != nil {
		t.Fatalf("ReloadRules: %v", err)
	}
	if got := e.TightestSampleWindow(); got != 10*time.Second {
		t.Errorf("with no rules the sampler should idle at the floor, got %s", got)
	}

	r := sampleRule()
	r.TriggerKind = "tps"
	r.TriggerConfig = map[string]any{"below": 15.0, "for_seconds": 600.0} // 10 min
	if _, err := CreateRule(r); err != nil {
		t.Fatalf("CreateRule: %v", err)
	}
	if err := e.ReloadRules(); err != nil {
		t.Fatalf("ReloadRules: %v", err)
	}
	if got := e.TightestSampleWindow(); got != time.Minute {
		t.Errorf("a 10-minute window should sample once a minute, got %s", got)
	}

	// A tighter rule wins: the sampler must satisfy the most demanding one.
	tight := sampleRule()
	tight.TriggerKind = "count"
	tight.TriggerConfig = map[string]any{"above": 20.0, "for_seconds": 60.0}
	if _, err := CreateRule(tight); err != nil {
		t.Fatalf("CreateRule: %v", err)
	}
	if err := e.ReloadRules(); err != nil {
		t.Fatalf("ReloadRules: %v", err)
	}
	if got := e.TightestSampleWindow(); got != 10*time.Second {
		t.Errorf("the tightest rule must set the pace, got %s", got)
	}
}

// Interpolation used to loop over the vars map, re-scanning text it had
// already substituted. Whether a "{server}" typed by a player inside {line}
// expanded depended on Go's random map order -- the same input giving
// different output between runs.
func TestLoop_InterpolationIsDeterministic(t *testing.T) {
	vars := map[string]string{
		"line":   "<Ant> olha isso {server} {player}",
		"server": "default",
		"player": "Notch",
	}

	first := Interpolate("chat: {line}", vars, false)
	for i := 0; i < 50; i++ {
		if got := Interpolate("chat: {line}", vars, false); got != first {
			t.Fatalf("same input produced different output: %q vs %q", first, got)
		}
	}
	if strings.Contains(first, "default") {
		t.Errorf("text substituted from a variable must not itself be expanded, got %q", first)
	}
}
