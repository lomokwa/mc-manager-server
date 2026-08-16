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

// A reload happens on every write, and the engine's per-rule state is keyed by
// rule ID -- so state from before a write can be read after it. This is only
// reachable now that rules can change at runtime: before the REST layer, rules
// only ever loaded once at boot.
func TestEngine_ReloadDropsHeldStateWhenTheRuleChanges(t *testing.T) {
	setupTestDB(t)
	run := &fakeRunner{running: true}

	r := sampleRule()
	r.TriggerKind = "tps"
	r.TriggerConfig = map[string]any{"below": 15.0, "for_seconds": 300.0}
	r.Actions = []Action{{Type: "backup"}}
	r.CooldownSeconds = 0
	r.DeafWindowSeconds = 0
	id, err := CreateRule(r)
	if err != nil {
		t.Fatalf("CreateRule: %v", err)
	}

	e := newTestEngine(run)
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	e.now = func() time.Time { return base }
	if err := e.ReloadRules(); err != nil {
		t.Fatalf("ReloadRules: %v", err)
	}

	// TPS goes bad. held_for says nothing happens until it stays bad 5 minutes.
	e.HandleEvent(types.SampleEvent{ServerID: "default", Kind: types.SampleTPS, Value: 10, At: base})
	e.waitIdle()
	if run.backups != 0 {
		t.Fatalf("held_for 300s fired on the first bad sample: %d backups", run.backups)
	}

	// Six minutes in -- past the five-minute window -- the operator repurposes
	// the rule: same row, completely different trigger. The elapsed time has to
	// exceed the window, or the inherited clock is not yet old enough to do any
	// damage and the test would pass without the fix.
	base = base.Add(6 * time.Minute)
	r.ID = id
	r.TriggerKind = "count"
	r.TriggerConfig = map[string]any{"above": 20.0, "for_seconds": 300.0}
	if err := UpdateRule(r); err != nil {
		t.Fatalf("UpdateRule: %v", err)
	}
	if err := e.ReloadRules(); err != nil {
		t.Fatalf("ReloadRules: %v", err)
	}

	// The player count crosses for the FIRST time right now, so the new
	// condition has been held for zero seconds, not six minutes.
	e.HandleEvent(types.SampleEvent{ServerID: "default", Kind: types.SamplePlayerCount, Value: 25, At: base})
	e.waitIdle()

	if run.backups != 0 {
		t.Errorf("the new condition inherited the old trigger's 4-minute-old clock and fired immediately")
	}
}

// Turning a rule off and back on must not leave it silently deaf from a firing
// that happened before it was turned off.
func TestEngine_ReloadDropsStateOfADisabledRule(t *testing.T) {
	setupTestDB(t)
	run := &fakeRunner{running: true}

	r := consoleRuleFor("morreu", Action{Type: "command", Command: "say f"})
	r.DeafWindowSeconds = 600 // long, and customisable by design
	id, err := CreateRule(r)
	if err != nil {
		t.Fatalf("CreateRule: %v", err)
	}

	e := newTestEngine(run)
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	e.now = func() time.Time { return base }
	if err := e.ReloadRules(); err != nil {
		t.Fatalf("ReloadRules: %v", err)
	}

	// Fire it, arming a ten-minute deaf window.
	e.HandleEvent(types.ConsoleLineEvent{ServerID: "default", Line: "o gato morreu", At: base})
	e.waitIdle()
	if len(run.commands) != 1 {
		t.Fatalf("expected the rule to fire, got %d commands", len(run.commands))
	}

	for _, enabled := range []bool{false, true} {
		if err := SetRuleEnabled(id, enabled); err != nil {
			t.Fatalf("SetRuleEnabled(%v): %v", enabled, err)
		}
		if err := e.ReloadRules(); err != nil {
			t.Fatalf("ReloadRules: %v", err)
		}
	}

	base = base.Add(time.Second)
	e.HandleEvent(types.ConsoleLineEvent{ServerID: "default", Line: "o cachorro morreu", At: base})
	e.waitIdle()

	if len(run.commands) != 2 {
		t.Errorf("the rule came back still deaf from before it was disabled: commands=%v", run.commands)
	}
}

// The precision half. Dropping state for every rule on every reload would be
// simpler and wrong: editing one rule would silently restart another rule's
// five-minute held-for clock, and nothing would ever say why it took ten.
func TestEngine_ReloadKeepsStateOfARuleThatDidNotChange(t *testing.T) {
	setupTestDB(t)
	run := &fakeRunner{running: true}

	watched := sampleRule()
	watched.TriggerKind = "tps"
	watched.TriggerConfig = map[string]any{"below": 15.0, "for_seconds": 300.0}
	watched.Actions = []Action{{Type: "backup"}}
	watched.CooldownSeconds = 0
	watched.DeafWindowSeconds = 0
	if _, err := CreateRule(watched); err != nil {
		t.Fatalf("CreateRule: %v", err)
	}

	e := newTestEngine(run)
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	e.now = func() time.Time { return base }
	if err := e.ReloadRules(); err != nil {
		t.Fatalf("ReloadRules: %v", err)
	}

	// The TPS condition starts being held.
	e.HandleEvent(types.SampleEvent{ServerID: "default", Kind: types.SampleTPS, Value: 10, At: base})
	e.waitIdle()

	// Someone creates an unrelated rule four minutes later, which reloads.
	base = base.Add(4 * time.Minute)
	if _, err := CreateRule(consoleRuleFor("outra coisa", Action{Type: "command", Command: "say x"})); err != nil {
		t.Fatalf("CreateRule: %v", err)
	}
	if err := e.ReloadRules(); err != nil {
		t.Fatalf("ReloadRules: %v", err)
	}

	// A minute later the TPS rule has genuinely held its condition for five
	// minutes and must fire.
	base = base.Add(time.Minute + time.Second)
	e.HandleEvent(types.SampleEvent{ServerID: "default", Kind: types.SampleTPS, Value: 10, At: base})
	e.waitIdle()

	if run.backups != 1 {
		t.Errorf("an unrelated rule's creation restarted this rule's held-for clock: backups=%d", run.backups)
	}
}

// {time} is in the design's variable table and in this package's own fixtures,
// and nothing ever populated it. Interpolate leaves an unknown name literal by
// design -- correct for a typo, wrong here: every Discord message written with
// the documented variable would have shipped with "{time}" in it.
func TestEngine_EveryFiringCarriesTheClock(t *testing.T) {
	setupTestDB(t)
	run := &fakeRunner{running: true}

	if _, err := CreateRule(consoleRuleFor("caiu",
		Action{Type: "command", Command: "say aconteceu as {time}"})); err != nil {
		t.Fatalf("CreateRule: %v", err)
	}

	e := newTestEngine(run)
	at := time.Date(2026, 1, 1, 17, 45, 12, 0, time.UTC)
	e.now = func() time.Time { return at }
	if err := e.ReloadRules(); err != nil {
		t.Fatalf("ReloadRules: %v", err)
	}

	e.HandleEvent(types.ConsoleLineEvent{ServerID: "default", Line: "o servidor caiu", At: at})
	e.waitIdle()

	if len(run.commands) != 1 {
		t.Fatalf("expected the rule to fire, got %v", run.commands)
	}
	if run.commands[0] != "say aconteceu as 17:45:12" {
		t.Errorf("got %q", run.commands[0])
	}
}

// A trigger that already provides a variable keeps its own value: the clock is
// added to what the matcher produced, not over it.
func TestEngine_TheClockDoesNotOverwriteATriggersOwnVariables(t *testing.T) {
	setupTestDB(t)
	run := &fakeRunner{running: true}

	if _, err := CreateRule(consoleRuleFor("morreu",
		Action{Type: "command", Command: "say {server} viu: {line}. as {time}"})); err != nil {
		t.Fatalf("CreateRule: %v", err)
	}

	e := newTestEngine(run)
	at := time.Date(2026, 1, 1, 9, 5, 0, 0, time.UTC)
	e.now = func() time.Time { return at }
	if err := e.ReloadRules(); err != nil {
		t.Fatalf("ReloadRules: %v", err)
	}

	e.HandleEvent(types.ConsoleLineEvent{ServerID: "default", Line: "alguem morreu", At: at})
	e.waitIdle()

	if len(run.commands) != 1 {
		t.Fatalf("expected the rule to fire, got %v", run.commands)
	}
	if run.commands[0] != "say default viu: alguem morreu. as 09:05:00" {
		t.Errorf("got %q", run.commands[0])
	}
}
