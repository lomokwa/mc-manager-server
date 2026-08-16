package automation

import (
	"strings"
	"testing"
	"time"

	"github.com/lomokwa/mc-manager/types"
)

func at(s string) time.Time {
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return ts
}

func freshState(now time.Time) MatchState {
	return MatchState{
		Now:               now,
		ConditionSince:    map[int]time.Time{},
		DeafUntil:         map[int]time.Time{},
		FiringsThisMinute: map[int]int{},
		InFlight:          map[int]bool{},
		KnownPlayers:      map[string]bool{},
	}
}

func consoleRule(pattern string) Rule {
	return Rule{
		ID: 1, ServerID: "default", Enabled: true,
		TriggerKind:   "console",
		TriggerConfig: map[string]any{"pattern": pattern},
		Actions:       []Action{{Type: "discord", WebhookID: 1, Message: "x"}},
	}
}

// Plain text is a literal substring search -- the UI promises "texto simples =
// busca literal", so a pattern containing regex punctuation must not silently
// behave like a pattern. "Can't keep up!" is the everyday example.
func TestMatch_ConsolePlainTextIsLiteral(t *testing.T) {
	now := at("2026-08-16T12:00:00Z")

	d := Evaluate(consoleRule("Can't keep up"), types.ConsoleLineEvent{
		ServerID: "default",
		Line:     "[12:00:00] [Server thread/WARN]: Can't keep up! Running 2140ms behind",
		At:       now,
	}, freshState(now))
	if !d.Fire {
		t.Errorf("expected a literal match to fire, got %q", d.Reason)
	}

	if d := Evaluate(consoleRule("a.c"), types.ConsoleLineEvent{
		ServerID: "default", Line: "abc", At: now,
	}, freshState(now)); d.Fire {
		t.Error(`"a.c" as plain text must not match "abc" -- that is regex behaviour`)
	}
}

func TestMatch_ConsoleSlashDelimitedIsRegex(t *testing.T) {
	now := at("2026-08-16T12:00:00Z")
	d := Evaluate(consoleRule("/joined the game$/"), types.ConsoleLineEvent{
		ServerID: "default", Line: "Notch joined the game", At: now,
	}, freshState(now))
	if !d.Fire {
		t.Errorf("expected the regex to fire, got %q", d.Reason)
	}
}

// An unparseable pattern must not take the engine down, and must not silently
// match everything either -- the failure mode of "matches everything" on a
// rule that runs console commands is not one to discover in production.
func TestMatch_BrokenRegexNeverFires(t *testing.T) {
	now := at("2026-08-16T12:00:00Z")
	d := Evaluate(consoleRule("/[unclosed/"), types.ConsoleLineEvent{
		ServerID: "default", Line: "anything", At: now,
	}, freshState(now))
	if d.Fire {
		t.Error("a rule with an invalid pattern must not fire")
	}
}

// The rule picked a server. Another server's console must never reach it.
func TestMatch_WrongServerNeverFires(t *testing.T) {
	now := at("2026-08-16T12:00:00Z")
	d := Evaluate(consoleRule("hello"), types.ConsoleLineEvent{
		ServerID: "creative", Line: "hello", At: now,
	}, freshState(now))
	if d.Fire {
		t.Error("a rule bound to 'default' fired on 'creative'")
	}
}

func TestMatch_LifecycleAndBackupTriggers(t *testing.T) {
	now := at("2026-08-16T12:00:00Z")

	stopRule := Rule{ID: 1, ServerID: "default", Enabled: true, TriggerKind: "stop"}
	if d := Evaluate(stopRule, types.ServerLifecycleEvent{
		ServerID: "default", Started: false, Expected: false, At: now,
	}, freshState(now)); !d.Fire {
		t.Error("expected an unexpected stop to fire the stop trigger")
	}
	// The trigger is "server stops UNEXPECTEDLY" -- a stop the panel asked for
	// is not news, and paging someone for it would train them to ignore it.
	if d := Evaluate(stopRule, types.ServerLifecycleEvent{
		ServerID: "default", Started: false, Expected: true, At: now,
	}, freshState(now)); d.Fire {
		t.Error("an expected stop must not fire the unexpected-stop trigger")
	}

	startRule := Rule{ID: 2, ServerID: "default", Enabled: true, TriggerKind: "start"}
	if d := Evaluate(startRule, types.ServerLifecycleEvent{
		ServerID: "default", Started: true, At: now,
	}, freshState(now)); !d.Fire {
		t.Error("expected a start to fire the start trigger")
	}

	failRule := Rule{ID: 3, ServerID: "default", Enabled: true, TriggerKind: "backup-fail"}
	if d := Evaluate(failRule, types.BackupEvent{
		ServerID: "default", Failed: true, Err: "disk full", At: now,
	}, freshState(now)); !d.Fire {
		t.Error("expected a failed backup to fire backup-fail")
	}
	if d := Evaluate(failRule, types.BackupEvent{
		ServerID: "default", Failed: false, Name: "world-x.zip", At: now,
	}, freshState(now)); d.Fire {
		t.Error("a successful backup must not fire backup-fail")
	}

	okRule := Rule{ID: 4, ServerID: "default", Enabled: true, TriggerKind: "backup-ok"}
	if d := Evaluate(okRule, types.BackupEvent{
		ServerID: "default", Failed: false, Name: "world-x.zip", At: now,
	}, freshState(now)); !d.Fire {
		t.Error("expected a successful backup to fire backup-ok")
	}
}

// "TPS below 15 for at least 5 minutes" -- the duration is the whole point.
// A single dip is not an incident, and alerting on one is how an alert becomes
// noise nobody reads.
func TestMatch_ThresholdRequiresTheFullDuration(t *testing.T) {
	start := at("2026-08-16T12:00:00Z")
	r := Rule{
		ID: 1, ServerID: "default", Enabled: true, TriggerKind: "tps",
		TriggerConfig: map[string]any{"below": 15.0, "for_seconds": 300.0},
	}
	st := freshState(start)

	if d := Evaluate(r, types.SampleEvent{
		ServerID: "default", Kind: types.SampleTPS, Value: 12, At: start,
	}, st); d.Fire {
		t.Error("must not fire on the first sample below the threshold")
	}

	st.Now = start.Add(4 * time.Minute)
	if d := Evaluate(r, types.SampleEvent{
		ServerID: "default", Kind: types.SampleTPS, Value: 12, At: st.Now,
	}, st); d.Fire {
		t.Error("must not fire before the window elapses")
	}

	st.Now = start.Add(5*time.Minute + time.Second)
	if d := Evaluate(r, types.SampleEvent{
		ServerID: "default", Kind: types.SampleTPS, Value: 12, At: st.Now,
	}, st); !d.Fire {
		t.Error("expected it to fire once the window elapsed")
	}
}

// Recovering resets the clock: TPS back above the threshold means the next dip
// starts counting from zero, not from the first one.
func TestMatch_ThresholdResetsWhenTheConditionClears(t *testing.T) {
	start := at("2026-08-16T12:00:00Z")
	r := Rule{
		ID: 1, ServerID: "default", Enabled: true, TriggerKind: "tps",
		TriggerConfig: map[string]any{"below": 15.0, "for_seconds": 300.0},
	}
	st := freshState(start)

	Evaluate(r, types.SampleEvent{ServerID: "default", Kind: types.SampleTPS, Value: 12, At: start}, st)

	st.Now = start.Add(time.Minute)
	Evaluate(r, types.SampleEvent{ServerID: "default", Kind: types.SampleTPS, Value: 20, At: st.Now}, st)

	st.Now = start.Add(6 * time.Minute)
	if d := Evaluate(r, types.SampleEvent{
		ServerID: "default", Kind: types.SampleTPS, Value: 12, At: st.Now,
	}, st); d.Fire {
		t.Error("the window must restart after the condition cleared")
	}
}

// Player count and disk cross UPWARDS. Sharing thresholdHeld with TPS is only
// safe if the direction is actually honoured.
func TestMatch_AboveThresholdsCrossTheOtherWay(t *testing.T) {
	start := at("2026-08-16T12:00:00Z")
	r := Rule{
		ID: 1, ServerID: "default", Enabled: true, TriggerKind: "count",
		TriggerConfig: map[string]any{"above": 20.0, "for_seconds": 60.0},
	}
	st := freshState(start)

	Evaluate(r, types.SampleEvent{ServerID: "default", Kind: types.SamplePlayerCount, Value: 25, At: start}, st)
	st.Now = start.Add(61 * time.Second)
	if d := Evaluate(r, types.SampleEvent{
		ServerID: "default", Kind: types.SamplePlayerCount, Value: 25, At: st.Now,
	}, st); !d.Fire {
		t.Error("expected a count above the threshold to fire once the window elapsed")
	}

	// Below the threshold must not fire, which is the bug a shared helper
	// invites if it only ever compares one way.
	st2 := freshState(start)
	Evaluate(r, types.SampleEvent{ServerID: "default", Kind: types.SamplePlayerCount, Value: 3, At: start}, st2)
	st2.Now = start.Add(61 * time.Second)
	if d := Evaluate(r, types.SampleEvent{
		ServerID: "default", Kind: types.SamplePlayerCount, Value: 3, At: st2.Now,
	}, st2); d.Fire {
		t.Error("a count BELOW an 'above' threshold must not fire")
	}
}

// A sample of the wrong kind must not satisfy a rule -- disk usage of 12 must
// never be read as a TPS of 12.
func TestMatch_SampleKindMustAgreeWithTheTrigger(t *testing.T) {
	now := at("2026-08-16T12:00:00Z")
	r := Rule{
		ID: 1, ServerID: "default", Enabled: true, TriggerKind: "tps",
		TriggerConfig: map[string]any{"below": 15.0, "for_seconds": 0.0},
	}
	st := freshState(now)

	if d := Evaluate(r, types.SampleEvent{
		ServerID: "default", Kind: types.SampleDiskPercent, Value: 12, At: now,
	}, st); d.Fire {
		t.Error("a disk sample fired a TPS rule")
	}
}

func TestMatch_JoinAndLeaveComeFromConsoleLines(t *testing.T) {
	now := at("2026-08-16T12:00:00Z")
	join := Rule{ID: 1, ServerID: "default", Enabled: true, TriggerKind: "join", TriggerConfig: map[string]any{}}
	leave := Rule{ID: 2, ServerID: "default", Enabled: true, TriggerKind: "leave", TriggerConfig: map[string]any{}}

	joined := types.ConsoleLineEvent{ServerID: "default", Line: "[12:00:00] [Server thread/INFO]: Notch joined the game", At: now}
	left := types.ConsoleLineEvent{ServerID: "default", Line: "[12:00:00] [Server thread/INFO]: Notch left the game", At: now}

	d := Evaluate(join, joined, freshState(now))
	if !d.Fire {
		t.Fatalf("expected a join to fire the join rule: %q", d.Reason)
	}
	if d.Vars["player"] != "Notch" {
		t.Errorf("expected {player} to be available, got %v", d.Vars)
	}
	if Evaluate(join, left, freshState(now)).Fire {
		t.Error("a leave fired the join rule")
	}
	if !Evaluate(leave, left, freshState(now)).Fire {
		t.Error("expected a leave to fire the leave rule")
	}
}

// Vanilla chat is "<Name> text", so a player can type "Notch joined the game"
// verbatim. Without anchoring to the real server-thread prefix AND a bare
// name, that chat line fires a join rule -- and a join rule can run console
// commands. Same anchoring lesson the Discord bot had to learn.
func TestMatch_ChatCannotImpersonateAJoin(t *testing.T) {
	now := at("2026-08-16T12:00:00Z")
	join := Rule{ID: 1, ServerID: "default", Enabled: true, TriggerKind: "join", TriggerConfig: map[string]any{}}

	for _, hostile := range []string{
		"[12:00:00] [Server thread/INFO]: <Ant_Redstone> Notch joined the game",
		"[12:00:00] [Server thread/INFO]: <Ant> hey Notch joined the game lol",
	} {
		if Evaluate(join, types.ConsoleLineEvent{ServerID: "default", Line: hostile, At: now}, freshState(now)).Fire {
			t.Errorf("chat impersonated a join: %q", hostile)
		}
	}
}

func TestMatch_FirstTimeOnlySkipsAKnownPlayer(t *testing.T) {
	now := at("2026-08-16T12:00:00Z")
	r := Rule{
		ID: 1, ServerID: "default", Enabled: true, TriggerKind: "join",
		TriggerConfig: map[string]any{"first_time_only": true},
	}
	line := types.ConsoleLineEvent{ServerID: "default", Line: "[12:00:00] [Server thread/INFO]: Notch joined the game", At: now}

	st := freshState(now)
	st.KnownPlayers = map[string]bool{"notch": true} // lowercased on purpose
	if Evaluate(r, line, st).Fire {
		t.Error("first_time_only fired for a player who has joined before")
	}

	fresh := freshState(now)
	if !Evaluate(r, line, fresh).Fire {
		t.Error("first_time_only should fire for a genuinely new player")
	}
}

func TestSchedule_EveryNHours(t *testing.T) {
	cfg := map[string]any{"mode": "every", "hours": 6.0}
	now := at("2026-08-16T12:00:00Z")

	if !dueBySchedule(cfg, nil, now) {
		t.Error("a schedule that has never run should be due")
	}
	recent := now.Add(-time.Hour)
	if dueBySchedule(cfg, &recent, now) {
		t.Error("not due yet: only an hour has passed of six")
	}
	old := now.Add(-7 * time.Hour)
	if !dueBySchedule(cfg, &old, now) {
		t.Error("due: more than six hours have passed")
	}
}

func TestSchedule_DailyAtATimeFiresOncePerDay(t *testing.T) {
	cfg := map[string]any{"mode": "daily", "time": "05:00"}

	before := at("2026-08-16T04:59:00Z")
	if dueBySchedule(cfg, nil, before) {
		t.Error("not due before the configured time")
	}
	after := at("2026-08-16T05:00:30Z")
	if !dueBySchedule(cfg, nil, after) {
		t.Error("due at the configured time")
	}

	// The tick arrives every minute. Without the last-run check, a daily 05:00
	// restart would fire again at 05:01, 05:02, and every minute until
	// midnight.
	ranToday := at("2026-08-16T05:00:10Z")
	if dueBySchedule(cfg, &ranToday, after) {
		t.Error("a daily schedule fired twice in one day")
	}
	tomorrow := at("2026-08-17T05:00:30Z")
	if !dueBySchedule(cfg, &ranToday, tomorrow) {
		t.Error("expected it to be due again the next day")
	}
}

func TestSchedule_GarbageConfigNeverFires(t *testing.T) {
	now := at("2026-08-16T12:00:00Z")
	for _, cfg := range []map[string]any{
		{},
		{"mode": "every", "hours": 0.0},
		{"mode": "daily", "time": "not a time"},
		{"mode": "whatever"},
	} {
		if dueBySchedule(cfg, nil, now) {
			t.Errorf("an unusable schedule config must never be due: %v", cfg)
		}
	}
}

func TestGuard_CooldownBlocksThenExpires(t *testing.T) {
	now := at("2026-08-16T12:00:00Z")
	fired := now.Add(-30 * time.Second)
	r := consoleRule("boom")
	r.CooldownSeconds = 60
	r.LastFiredAt = &fired

	d := Evaluate(r, types.ConsoleLineEvent{ServerID: "default", Line: "boom", At: now}, freshState(now))
	if d.Fire {
		t.Error("expected the cooldown to block")
	}
	// The reason reaches the UI. "cooling down for another 30s" is the
	// difference between a user understanding their rule and filing a bug.
	if !strings.Contains(d.Reason, "cooling down") {
		t.Errorf("expected the refusal to name the cooldown, got %q", d.Reason)
	}

	later := now.Add(40 * time.Second)
	if d := Evaluate(r, types.ConsoleLineEvent{
		ServerID: "default", Line: "boom", At: later,
	}, freshState(later)); !d.Fire {
		t.Error("expected it to fire once the cooldown expired")
	}
}

// The loop this closes: a rule runs a console command, the command prints to
// the console, that line matches the same pattern, and it fires again. On a
// live server, forever.
func TestGuard_DeafWindowSilencesTheRulesOwnEcho(t *testing.T) {
	now := at("2026-08-16T12:00:00Z")
	r := consoleRule("Saved the game")
	st := freshState(now)
	st.DeafUntil[r.ID] = now.Add(5 * time.Second)

	if d := Evaluate(r, types.ConsoleLineEvent{
		ServerID: "default", Line: "Saved the game", At: now,
	}, st); d.Fire {
		t.Errorf("expected the deaf window to block, got fire (reason %q)", d.Reason)
	}

	st.Now = now.Add(6 * time.Second)
	if d := Evaluate(r, types.ConsoleLineEvent{
		ServerID: "default", Line: "Saved the game", At: st.Now,
	}, st); !d.Fire {
		t.Error("expected it to hear again once the deaf window passed")
	}
}

// Deafness is about a rule hearing ITSELF. A scheduled tick or a backup
// result is never an echo, so silencing one would be a different bug wearing
// the same clothes.
func TestGuard_DeafWindowOnlySilencesConsole(t *testing.T) {
	now := at("2026-08-16T12:00:00Z")
	st := freshState(now)

	backup := Rule{ID: 1, ServerID: "default", Enabled: true, TriggerKind: "backup-fail"}
	st.DeafUntil[backup.ID] = now.Add(time.Minute)
	if d := Evaluate(backup, types.BackupEvent{
		ServerID: "default", Failed: true, Err: "disk full", At: now,
	}, st); !d.Fire {
		t.Errorf("a backup event must not be silenced by the deaf window: %q", d.Reason)
	}

	sched := Rule{ID: 2, ServerID: "default", Enabled: true, TriggerKind: "sched",
		TriggerConfig: map[string]any{"mode": "every", "hours": 1.0}}
	st.DeafUntil[sched.ID] = now.Add(time.Minute)
	if d := Evaluate(sched, types.ScheduleTick{At: now}, st); !d.Fire {
		t.Errorf("a schedule tick must not be silenced by the deaf window: %q", d.Reason)
	}
}

func TestGuard_FiringCapStopsARunaway(t *testing.T) {
	now := at("2026-08-16T12:00:00Z")
	r := consoleRule("spam")
	st := freshState(now)
	st.FiringsThisMinute[r.ID] = maxFiringsPerMinute

	d := Evaluate(r, types.ConsoleLineEvent{ServerID: "default", Line: "spam", At: now}, st)
	if d.Fire {
		t.Error("expected the firing cap to stop a runaway rule")
	}
	if d.Reason == "" {
		t.Error("a blocked decision must say why -- that reason reaches the UI")
	}

	// One below the cap still fires: the guard must not be off-by-one and
	// silently cost the user their last allowed firing.
	st.FiringsThisMinute[r.ID] = maxFiringsPerMinute - 1
	if d := Evaluate(r, types.ConsoleLineEvent{
		ServerID: "default", Line: "spam", At: now,
	}, st); !d.Fire {
		t.Error("expected the last firing under the cap to be allowed")
	}
}

// Restart-with-warnings makes one firing last minutes. A second firing
// starting mid-sequence would make the warnings a lie.
func TestGuard_OneFiringPerRuleAtATime(t *testing.T) {
	now := at("2026-08-16T12:00:00Z")
	r := consoleRule("go")
	st := freshState(now)
	st.InFlight[r.ID] = true

	if d := Evaluate(r, types.ConsoleLineEvent{
		ServerID: "default", Line: "go", At: now,
	}, st); d.Fire {
		t.Error("expected an in-flight firing to block a second one")
	}
}

func TestGuard_DisabledRuleNeverFires(t *testing.T) {
	now := at("2026-08-16T12:00:00Z")
	r := consoleRule("anything")
	r.Enabled = false

	if d := Evaluate(r, types.ConsoleLineEvent{
		ServerID: "default", Line: "anything", At: now,
	}, freshState(now)); d.Fire {
		t.Error("a disabled rule fired")
	}
}

// A cooldown of zero means "no cooldown", not "block forever". Getting this
// backwards would make every rule created with the UI default fire once and
// never again.
func TestGuard_ZeroCooldownDoesNotBlock(t *testing.T) {
	now := at("2026-08-16T12:00:00Z")
	justFired := now.Add(-time.Millisecond)
	r := consoleRule("again")
	r.CooldownSeconds = 0
	r.LastFiredAt = &justFired

	if d := Evaluate(r, types.ConsoleLineEvent{
		ServerID: "default", Line: "again", At: now,
	}, freshState(now)); !d.Fire {
		t.Errorf("a zero cooldown must not block, got %q", d.Reason)
	}
}

// A trigger the matcher supports but the API rejects is exactly as broken as
// the reverse, and both fail silently -- the rule just never works.
func TestIsKnownTrigger_CoversEveryTriggerTheMatcherHandles(t *testing.T) {
	for _, kind := range []string{
		"console", "join", "leave", "start", "stop",
		"tps", "count", "disk", "backup-ok", "backup-fail", "sched",
	} {
		if !IsKnownTrigger(kind) {
			t.Errorf("the matcher handles %q but the API would reject it", kind)
		}
	}
	if IsKnownTrigger("launch-missiles") {
		t.Error("an unknown trigger must be rejected")
	}
}
