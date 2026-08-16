package automation

import (
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/lomokwa/mc-manager/services"
	"github.com/lomokwa/mc-manager/types"
)

// errNoSuchServer is what a resolver returns for a rule bound to a server that
// is no longer in the registry.
var errNoSuchServer = errors.New("server not found")

// Engine subscribes to the bus, decides with the matcher, and executes with the
// runner.
//
// It owns the mutable state the matcher reads but must not fetch for itself:
// threshold windows, deaf windows, the per-minute firing count, and what is
// currently in flight. Evaluate stays free of clocks and I/O; this is where
// both live.
type Engine struct {
	bus     *types.EventBus
	resolve func(serverID string) (ActionRunner, error)

	mu    sync.Mutex
	rules []Rule
	state MatchState

	// Injected so tests can collapse a five-minute restart sequence and
	// control the clock.
	sleep func(time.Duration)
	now   func() time.Time

	wg       sync.WaitGroup
	stopOnce sync.Once
	stop     chan struct{}
}

func NewEngine(bus *types.EventBus, resolve func(string) (ActionRunner, error)) *Engine {
	return &Engine{
		bus:     bus,
		resolve: resolve,
		state: MatchState{
			ConditionSince:    map[int]time.Time{},
			DeafUntil:         map[int]time.Time{},
			FiringsThisMinute: map[int]int{},
			InFlight:          map[int]bool{},
			KnownPlayers:      map[string]bool{},
		},
		sleep: time.Sleep,
		now:   time.Now,
		stop:  make(chan struct{}),
	}
}

// ReloadRules re-reads the enabled rules. Called at boot and whenever a rule
// changes, so an edit takes effect without restarting the API.
func (e *Engine) ReloadRules() error {
	rules, err := ListEnabledRules()
	if err != nil {
		return err
	}

	// Reading usercache is only worth it when some rule asks "is this their
	// first join". Otherwise this would be a file read per reload that nothing
	// consumes.
	var known map[string]bool
	if anyRuleNeedsKnownPlayers(rules) {
		known = map[string]bool{}
		for _, rt := range services.AllRuntimes() {
			players, err := rt.ListPlayers()
			if err != nil {
				// A server that has never had a player has no usercache, which
				// is a normal state and not worth failing a reload over.
				continue
			}
			for _, p := range players {
				known[strings.ToLower(p.Name)] = true
			}
		}
	}

	e.mu.Lock()
	// The engine's per-rule state is keyed by rule ID, and a reload happens on
	// every write -- so state accumulated before an edit would be read after
	// it. Anything left in `stale` below either vanished from the rule set
	// (deleted, or disabled) or had its trigger changed, and its held-for clock
	// and deaf window describe a condition that no longer exists.
	//
	// Dropped per rule, not wholesale: clearing everything on every reload
	// would be simpler and wrong, because creating one rule would silently
	// restart another rule's five-minute held-for clock with nothing to say
	// why it took ten.
	stale := make(map[int]Rule, len(e.rules))
	for _, r := range e.rules {
		stale[r.ID] = r
	}
	for _, r := range rules {
		if before, ok := stale[r.ID]; ok && sameTrigger(before, r) {
			delete(stale, r.ID)
		}
	}
	for id := range stale {
		delete(e.state.ConditionSince, id)
		delete(e.state.DeafUntil, id)
		delete(e.state.FiringsThisMinute, id)
		// InFlight is deliberately NOT cleared. It is owned by a running action
		// chain and cleared when that chain finishes; clearing it here would let
		// a second chain start while the first is still going -- two overlapping
		// restarts, each with its own warning countdown.
	}

	e.rules = rules
	if known != nil {
		e.state.KnownPlayers = known
	}
	e.mu.Unlock()
	return nil
}

// sameTrigger reports whether two versions of a rule describe the same
// condition, which is what decides whether the state accumulated under the old
// one still means anything. Deaf window counts: it is armed as an absolute
// deadline, so shortening it from ten minutes to five has to take effect on the
// window already running, not just the next one.
func sameTrigger(before, after Rule) bool {
	if before.TriggerKind != after.TriggerKind ||
		before.DeafWindowSeconds != after.DeafWindowSeconds {
		return false
	}
	b, berr := json.Marshal(before.TriggerConfig)
	a, aerr := json.Marshal(after.TriggerConfig)
	if berr != nil || aerr != nil {
		// Two configs that cannot be compared are treated as different.
		// Dropping state is always the safe direction: the cost is one delayed
		// firing, and the cost of keeping it is a firing that should not happen.
		return false
	}
	return string(b) == string(a)
}

func anyRuleNeedsKnownPlayers(rules []Rule) bool {
	for _, r := range rules {
		if r.TriggerKind != "join" {
			continue
		}
		if first, _ := r.TriggerConfig["first_time_only"].(bool); first {
			return true
		}
	}
	return false
}

// NeedsSampling reports whether any enabled rule depends on this measurement.
//
// The sampler asks before doing anything, because TPS and player count are not
// free: reading them makes the JVM run `spark tps` / `list`. A rule that is
// turned off must stop costing that immediately, which is why this reads the
// live rule set rather than a flag captured at boot.
func (e *Engine) NeedsSampling(k types.SampleKind) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, r := range e.rules {
		if sampleMatchesKind(r.TriggerKind, k) {
			return true
		}
	}
	return false
}

// TightestSampleWindow reports the shortest "for at least N" any enabled
// threshold rule asks for, which is what the sampler derives its interval from.
//
// Zero means nothing needs sampling; the sampler treats that as its floor and
// keeps ticking cheaply (each tick with nothing configured is three map reads).
// Reading the live rule set matters: editing a window, or disabling the rule
// that demanded a tight one, has to change the cost immediately.
func (e *Engine) TightestSampleWindow() time.Duration {
	e.mu.Lock()
	defer e.mu.Unlock()

	var tightest time.Duration
	for _, r := range e.rules {
		switch r.TriggerKind {
		case "tps", "count", "disk":
		default:
			continue
		}
		secs, _ := r.TriggerConfig["for_seconds"].(float64)
		w := services.SampleInterval(time.Duration(secs) * time.Second)
		if tightest == 0 || w < tightest {
			tightest = w
		}
	}
	if tightest == 0 {
		return services.SampleInterval(0)
	}
	return tightest
}

// Start subscribes to the bus and begins consuming. Safe to call once.
func (e *Engine) Start() {
	ch := e.bus.Subscribe()
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		defer e.bus.Unsubscribe(ch)

		minute := time.NewTicker(time.Minute)
		defer minute.Stop()

		for {
			select {
			case ev, ok := <-ch:
				if !ok {
					return
				}
				e.HandleEvent(ev)

			case <-minute.C:
				e.mu.Lock()
				e.state.FiringsThisMinute = map[int]int{}
				e.mu.Unlock()
				// The same tick drives `sched` rules. Nothing external
				// publishes ScheduleTick -- a schedule has no source but the
				// clock -- and dueBySchedule decides from last_fired_at, so
				// minute granularity is enough and an API restart neither
				// skips a daily job nor fires it twice.
				e.HandleEvent(types.ScheduleTick{At: e.now()})

			case <-e.stop:
				return
			}
		}
	}()
}

// Stop ends the consumer and waits for in-flight firings. Idempotent, because
// a double Stop closing the same channel would panic.
func (e *Engine) Stop() {
	e.stopOnce.Do(func() { close(e.stop) })
	e.wg.Wait()
}

// HandleEvent evaluates every enabled rule against one event and fires what
// matches.
func (e *Engine) HandleEvent(ev types.Event) {
	e.mu.Lock()
	if len(e.rules) == 0 {
		e.mu.Unlock()
		return // nothing configured: cost nothing
	}

	e.state.Now = e.now()
	rules := append([]Rule(nil), e.rules...)

	// Decide under the lock, execute outside it: an action can take minutes,
	// and holding the mutex through a restart sequence would freeze every
	// other event behind it.
	var firing []struct {
		rule Rule
		dec  Decision
	}
	for i, r := range rules {
		d := Evaluate(r, ev, e.state)
		if !d.Fire {
			continue
		}
		e.state.InFlight[r.ID] = true
		e.state.FiringsThisMinute[r.ID]++

		// Stamp the CACHED rule here, not only the database row later.
		// Evaluate reads LastFiredAt off e.rules, and e.rules is only
		// refreshed by ReloadRules -- so persisting alone left the cooldown
		// and every schedule reading a value from boot. That made a 1h
		// cooldown allow five firings in a second, and an every-6h schedule
		// run on every minute tick.
		firedAt := e.state.Now
		rules[i].LastFiredAt = &firedAt
		for j := range e.rules {
			if e.rules[j].ID == r.ID {
				e.rules[j].LastFiredAt = &firedAt
			}
		}

		// A threshold that is still held must not re-fire on the next sample.
		// Clearing the window restarts it, so the rule needs another full
		// "for at least N" before it can fire again. Without this, "TPS below
		// 15 -> restart" with the schema's default cooldown of 0 is a restart
		// loop on a live server.
		delete(e.state.ConditionSince, r.ID)

		// A join has happened, so the player is no longer new. Recording it
		// here rather than at the next reload is what makes first_time_only
		// mean once, instead of once per reload.
		if player := d.Vars["player"]; player != "" && r.TriggerKind == "join" {
			e.state.KnownPlayers[strings.ToLower(player)] = true
		}

		firing = append(firing, struct {
			rule Rule
			dec  Decision
		}{rules[i], d})
	}
	e.mu.Unlock()

	for _, f := range firing {
		e.fire(f.rule, f.dec)
	}
}

func (e *Engine) fire(r Rule, d Decision) {
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		defer func() {
			e.mu.Lock()
			delete(e.state.InFlight, r.ID)
			e.mu.Unlock()
		}()

		run, err := e.resolve(r.ServerID)
		if err != nil {
			slog.Error("automation: cannot resolve the rule's server",
				"rule", r.ID, "name", r.Name, "server", r.ServerID, "err", err)
			return
		}

		// Persist the stamp HandleEvent already applied to the cached rule.
		// Using r.LastFiredAt rather than a fresh now() keeps the two copies
		// identical, so a reload cannot move a cooldown that already started.
		stamp := e.now()
		if r.LastFiredAt != nil {
			stamp = *r.LastFiredAt
		}
		if err := MarkFired(r.ID, stamp); err != nil {
			slog.Error("automation: failed to record last_fired_at", "rule", r.ID, "err", err)
		}

		// {time} is in the design's variable table, and no matcher populated it:
		// every trigger wants the clock and none of them has a reason to know
		// about it. Added here rather than in eleven matcher branches, and
		// added UNDER what the matcher produced so a trigger that ever carries
		// its own "time" keeps it.
		//
		// This is not cosmetic. Interpolate leaves an unknown name literal --
		// right for a typo, and exactly wrong here: a message written with the
		// documented variable shipped with "{time}" visible in it.
		vars := make(map[string]string, len(d.Vars)+1)
		vars["time"] = stamp.Format("15:04:05")
		for k, v := range d.Vars {
			vars[k] = v
		}

		results := RunActions(r, vars, run, e.sleep)

		// Only a rule that writes to the console can hear itself.
		if r.DeafWindowSeconds > 0 && usesConsole(r) {
			deafUntil := e.now().Add(time.Duration(r.DeafWindowSeconds) * time.Second)
			e.mu.Lock()
			e.state.DeafUntil[r.ID] = deafUntil
			e.mu.Unlock()
		}

		outcome, err := json.Marshal(results)
		if err != nil {
			slog.Error("automation: failed to encode the firing outcome", "rule", r.ID, "err", err)
			outcome = []byte("[]")
		}
		if err := RecordFiring(Firing{RuleID: r.ID, Trigger: d.TriggerSummary, Outcome: string(outcome)}); err != nil {
			slog.Error("automation: failed to record the firing", "rule", r.ID, "err", err)
		}

		failed := 0
		for _, res := range results {
			if !res.OK {
				failed++
			}
		}
		slog.Info("automation fired",
			"rule", r.ID, "name", r.Name, "server", r.ServerID,
			"trigger", d.TriggerSummary, "actions", len(results), "failed", failed)
	}()
}

// usesConsole reports whether a rule pushes anything into the console, which is
// the only way it can end up hearing itself.
func usesConsole(r Rule) bool {
	for _, a := range r.Actions {
		switch a.Type {
		case "command", "spark", "restart":
			return true
		}
	}
	return false
}

// waitIdle blocks until every in-flight firing has finished. Test-only.
func (e *Engine) waitIdle() { e.wg.Wait() }
