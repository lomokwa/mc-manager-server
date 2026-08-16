package automation

import (
	"strings"
	"testing"
	"time"

	"github.com/lomokwa/mc-manager/types"
)

func newTestEngine(run ActionRunner) *Engine {
	e := NewEngine(types.NewEventBus(), func(string) (ActionRunner, error) { return run, nil })
	e.sleep = func(time.Duration) {}
	return e
}

func consoleRuleFor(pattern string, actions ...Action) Rule {
	r := sampleRule()
	r.TriggerKind = "console"
	r.TriggerConfig = map[string]any{"pattern": pattern}
	r.Actions = actions
	r.CooldownSeconds = 0
	return r
}

func TestEngine_FiresARuleAndRecordsTheOutcome(t *testing.T) {
	setupTestDB(t)
	run := &fakeRunner{running: true}

	id, err := CreateRule(consoleRuleFor("Can't keep up", Action{Type: "command", Command: "say lag"}))
	if err != nil {
		t.Fatalf("CreateRule: %v", err)
	}

	e := newTestEngine(run)
	if err := e.ReloadRules(); err != nil {
		t.Fatalf("ReloadRules: %v", err)
	}

	e.HandleEvent(types.ConsoleLineEvent{
		ServerID: "default", Line: "Can't keep up! 2140ms behind", At: time.Now(),
	})
	e.waitIdle()

	if len(run.commands) != 1 {
		t.Fatalf("expected the action to run, got %v", run.commands)
	}
	firings, err := ListFirings(id, 10)
	if err != nil {
		t.Fatalf("ListFirings: %v", err)
	}
	if len(firings) != 1 {
		t.Fatalf("expected one recorded firing, got %d", len(firings))
	}
	if firings[0].Trigger == "" {
		t.Error("a firing must record what matched")
	}
	if !strings.Contains(firings[0].Outcome, `"ok":true`) {
		t.Errorf("the outcome must record each action's result, got %q", firings[0].Outcome)
	}
}

// After running a console command the rule must stop hearing the console for
// its configured window -- otherwise the command's own output can match the
// same pattern and fire it again, forever, on a live server.
func TestEngine_SetsTheDeafWindowAfterACommand(t *testing.T) {
	setupTestDB(t)
	run := &fakeRunner{running: true}

	r := consoleRuleFor("echo", Action{Type: "command", Command: "say echo"})
	r.DeafWindowSeconds = 30
	if _, err := CreateRule(r); err != nil {
		t.Fatalf("CreateRule: %v", err)
	}

	e := newTestEngine(run)
	if err := e.ReloadRules(); err != nil {
		t.Fatalf("ReloadRules: %v", err)
	}

	e.HandleEvent(types.ConsoleLineEvent{ServerID: "default", Line: "echo", At: time.Now()})
	e.waitIdle()
	e.HandleEvent(types.ConsoleLineEvent{ServerID: "default", Line: "echo", At: time.Now()})
	e.waitIdle()

	if len(run.commands) != 1 {
		t.Errorf("the rule heard its own echo: %v", run.commands)
	}
}

// A rule with no console-writing action never needs to go deaf, and silencing
// it would cost real firings for no reason.
func TestEngine_NoDeafWindowForARuleThatOnlyPosts(t *testing.T) {
	setupTestDB(t)
	run := &fakeRunner{running: true}

	whID, err := CreateWebhook(Webhook{Name: "w", URL: "http://127.0.0.1:1/never"})
	if err != nil {
		t.Fatalf("CreateWebhook: %v", err)
	}
	r := consoleRuleFor("ping", Action{Type: "discord", WebhookID: whID, Message: "pong"})
	r.StopOnFailure = false
	r.DeafWindowSeconds = 30
	if _, err := CreateRule(r); err != nil {
		t.Fatalf("CreateRule: %v", err)
	}

	e := newTestEngine(run)
	if err := e.ReloadRules(); err != nil {
		t.Fatalf("ReloadRules: %v", err)
	}

	e.HandleEvent(types.ConsoleLineEvent{ServerID: "default", Line: "ping", At: time.Now()})
	e.waitIdle()
	e.HandleEvent(types.ConsoleLineEvent{ServerID: "default", Line: "ping", At: time.Now()})
	e.waitIdle()

	// Two firings recorded, because a Discord-only rule cannot echo itself.
	rules, _ := ListEnabledRules()
	firings, _ := ListFirings(rules[0].ID, 10)
	if len(firings) != 2 {
		t.Errorf("expected both events to fire a post-only rule, got %d firings", len(firings))
	}
}

// The property that makes this safe to merge: no rules means no work.
func TestEngine_DoesNothingWithNoRules(t *testing.T) {
	setupTestDB(t)
	run := &fakeRunner{running: true}

	e := newTestEngine(run)
	if err := e.ReloadRules(); err != nil {
		t.Fatalf("ReloadRules: %v", err)
	}

	e.HandleEvent(types.ConsoleLineEvent{ServerID: "default", Line: "anything at all", At: time.Now()})
	e.waitIdle()

	if len(run.commands) != 0 || run.backups != 0 || run.restarts != 0 {
		t.Error("the engine acted with no rules configured")
	}
	for _, k := range []types.SampleKind{types.SampleTPS, types.SamplePlayerCount, types.SampleDiskPercent} {
		if e.NeedsSampling(k) {
			t.Errorf("no rule asks for %s, so nothing should sample it", k)
		}
	}
}

// Sampling is not free -- reading TPS makes the JVM run `spark tps` -- so it
// must be driven by what an enabled rule actually asks for.
func TestEngine_NeedsSamplingOnlyForKindsAnEnabledRuleUses(t *testing.T) {
	setupTestDB(t)

	r := sampleRule()
	r.TriggerKind = "tps"
	r.TriggerConfig = map[string]any{"below": 15.0, "for_seconds": 60.0}
	if _, err := CreateRule(r); err != nil {
		t.Fatalf("CreateRule: %v", err)
	}

	e := newTestEngine(&fakeRunner{})
	if err := e.ReloadRules(); err != nil {
		t.Fatalf("ReloadRules: %v", err)
	}

	if !e.NeedsSampling(types.SampleTPS) {
		t.Error("a TPS rule exists, so TPS must be sampled")
	}
	if e.NeedsSampling(types.SampleDiskPercent) {
		t.Error("no disk rule exists -- sampling it would be work nobody asked for")
	}
}

// A disabled rule must not keep the sampler running. Otherwise turning a rule
// off leaves its cost behind.
func TestEngine_DisablingTheLastTPSRuleStopsSampling(t *testing.T) {
	setupTestDB(t)

	r := sampleRule()
	r.TriggerKind = "tps"
	r.TriggerConfig = map[string]any{"below": 15.0, "for_seconds": 60.0}
	id, err := CreateRule(r)
	if err != nil {
		t.Fatalf("CreateRule: %v", err)
	}

	e := newTestEngine(&fakeRunner{})
	e.ReloadRules()
	if !e.NeedsSampling(types.SampleTPS) {
		t.Fatal("expected sampling while the rule is enabled")
	}

	if err := SetRuleEnabled(id, false); err != nil {
		t.Fatalf("SetRuleEnabled: %v", err)
	}
	if err := e.ReloadRules(); err != nil {
		t.Fatalf("ReloadRules: %v", err)
	}
	if e.NeedsSampling(types.SampleTPS) {
		t.Error("disabling the only TPS rule must stop the sampling it caused")
	}
}

// Cooldown is stamped at the START of a firing. A restart sequence takes
// minutes, and counting from the end would silently add them to the cooldown.
func TestEngine_CooldownIsStampedWhenTheFiringStarts(t *testing.T) {
	setupTestDB(t)
	run := &fakeRunner{running: true}

	r := consoleRuleFor("go", Action{Type: "restart", Warnings: []int{300, 60}})
	r.CooldownSeconds = 60
	id, err := CreateRule(r)
	if err != nil {
		t.Fatalf("CreateRule: %v", err)
	}

	e := newTestEngine(run)
	e.ReloadRules()

	before := time.Now()
	e.HandleEvent(types.ConsoleLineEvent{ServerID: "default", Line: "go", At: before})
	e.waitIdle()

	got, err := GetRule(id)
	if err != nil {
		t.Fatalf("GetRule: %v", err)
	}
	if got.LastFiredAt == nil {
		t.Fatal("expected the firing to be stamped")
	}
	// The sequence "took" 6 minutes of simulated sleep; the stamp must sit at
	// the start, not after it.
	if got.LastFiredAt.After(before.Add(30 * time.Second)) {
		t.Errorf("cooldown was stamped at the end of the firing, not the start: %v vs %v", *got.LastFiredAt, before)
	}
}

// An event for a server that is not in the registry must be dropped quietly
// rather than crash the engine goroutine.
func TestEngine_UnresolvableServerDoesNotTakeTheEngineDown(t *testing.T) {
	setupTestDB(t)

	if _, err := CreateRule(consoleRuleFor("boom", Action{Type: "command", Command: "say hi"})); err != nil {
		t.Fatalf("CreateRule: %v", err)
	}

	e := NewEngine(types.NewEventBus(), func(string) (ActionRunner, error) {
		return nil, errNoSuchServer
	})
	e.sleep = func(time.Duration) {}
	e.ReloadRules()

	e.HandleEvent(types.ConsoleLineEvent{ServerID: "default", Line: "boom", At: time.Now()})
	e.waitIdle() // must return rather than panic or hang
}

// Start/Stop must be clean: a leaked subscription would keep the bus fanning
// events into a channel nobody drains.
func TestEngine_StartAndStopCleanly(t *testing.T) {
	setupTestDB(t)
	bus := types.NewEventBus()
	e := NewEngine(bus, func(string) (ActionRunner, error) { return &fakeRunner{running: true}, nil })
	e.sleep = func(time.Duration) {}
	e.ReloadRules()

	e.Start()
	if !bus.HasSubscribers() {
		t.Error("expected the engine to subscribe on Start")
	}
	e.Stop()
	if bus.HasSubscribers() {
		t.Error("expected the engine to unsubscribe on Stop")
	}
}
