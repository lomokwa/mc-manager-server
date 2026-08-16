package automation

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/lomokwa/mc-manager/types"
)

// maxFiringsPerMinute is the backstop for a loop nobody predicted. The deaf
// window closes the one loop we know about (a rule hearing the console echo of
// its own command); this catches the rest. Excess is dropped and counted
// rather than silently allowed.
const maxFiringsPerMinute = 12

// MatchState is everything Evaluate needs beyond the rule and the event.
//
// It is passed in rather than read from a clock or a package global so the
// whole decision layer is testable without waiting: "TPS below 15 for five
// minutes" is a microsecond test, not a five-minute one. On a feature whose
// actions restart servers with players on them, being able to exercise the
// decision in isolation is the difference between believing it works and
// knowing.
type MatchState struct {
	Now time.Time

	// ConditionSince[ruleID] is when a threshold rule's condition most
	// recently became true. Absent means the condition is not currently held.
	ConditionSince map[int]time.Time

	// DeafUntil[ruleID] is set after the rule pushes anything into the
	// console: until then it ignores console lines, so it cannot hear its own
	// echo and fire itself.
	DeafUntil map[int]time.Time

	FiringsThisMinute map[int]int
	InFlight          map[int]bool

	// KnownPlayers holds lowercased names that have joined before, for the
	// "only first-time joins" option. Populated by the engine only when some
	// rule actually asks.
	KnownPlayers map[string]bool
}

// Decision is the matcher's answer. Reason explains a refusal, and it is not
// decoration: it reaches the log and the UI, so "why didn't my rule fire" has
// an answer that does not require reading this file.
type Decision struct {
	Fire           bool
	Reason         string
	TriggerSummary string
	Vars           map[string]string
}

// Evaluate decides whether a rule fires. No I/O and no clock read -- the time
// arrives in st -- which is what makes every window, cooldown and cap testable
// in microseconds instead of in real minutes.
//
// It is NOT pure: a threshold trigger has to remember when its condition became
// true, so thresholdHeld writes st.ConditionSince. That state is owned by the
// engine and deliberately lives in memory only. After a restart the API cannot
// know what happened while it was down, so inheriting a window nobody observed
// would be inventing data.
//
// One consequence worth knowing: a rule inside its cooldown returns before
// matchesTrigger runs, so its threshold window is not tracked during the
// cooldown and restarts from the next sample afterwards. That is the intended
// reading -- firing the instant a cooldown lifts, on a window accumulated while
// the rule was suppressed, is not what "for at least N minutes" promises.
func Evaluate(r Rule, ev types.Event, st MatchState) Decision {
	if !r.Enabled {
		return Decision{Reason: "rule disabled"}
	}
	if st.InFlight[r.ID] {
		return Decision{Reason: "a previous firing is still running"}
	}
	if st.FiringsThisMinute[r.ID] >= maxFiringsPerMinute {
		return Decision{Reason: "firing cap reached for this minute"}
	}
	// Deafness silences the console only. That is the single source a rule can
	// trigger itself through; a scheduled tick or a backup result is never an
	// echo, and swallowing one would be a different bug in the same clothes.
	if _, isLine := ev.(types.ConsoleLineEvent); isLine {
		if until, ok := st.DeafUntil[r.ID]; ok && st.Now.Before(until) {
			return Decision{Reason: "inside the rule's own deaf window"}
		}
	}
	if r.LastFiredAt != nil && r.CooldownSeconds > 0 {
		cooldown := time.Duration(r.CooldownSeconds) * time.Second
		if elapsed := st.Now.Sub(*r.LastFiredAt); elapsed < cooldown {
			return Decision{Reason: fmt.Sprintf("cooling down for another %s", (cooldown - elapsed).Round(time.Second))}
		}
	}

	fired, summary, vars := matchesTrigger(r, ev, st)
	if !fired {
		return Decision{Reason: "trigger did not match"}
	}
	return Decision{Fire: true, TriggerSummary: summary, Vars: vars}
}

func matchesTrigger(r Rule, ev types.Event, st MatchState) (bool, string, map[string]string) {
	switch e := ev.(type) {
	case types.ConsoleLineEvent:
		if e.ServerID != r.ServerID {
			return false, "", nil
		}
		if r.TriggerKind == "join" || r.TriggerKind == "leave" {
			return matchesPlayerEvent(r, e, st)
		}
		if r.TriggerKind != "console" {
			return false, "", nil
		}
		pattern, _ := r.TriggerConfig["pattern"].(string)
		if !lineMatches(pattern, e.Line) {
			return false, "", nil
		}
		return true, "console: " + pattern, map[string]string{
			"line":   e.Line,
			"server": r.ServerID,
		}

	case types.ServerLifecycleEvent:
		if e.ServerID != r.ServerID {
			return false, "", nil
		}
		switch {
		case r.TriggerKind == "start" && e.Started:
			return true, "server started", map[string]string{"server": r.ServerID}
		// "stops unexpectedly": a stop the panel asked for is not news.
		case r.TriggerKind == "stop" && !e.Started && !e.Expected:
			return true, "server stopped unexpectedly", map[string]string{"server": r.ServerID}
		}
		return false, "", nil

	case types.BackupEvent:
		if e.ServerID != r.ServerID {
			return false, "", nil
		}
		switch {
		case r.TriggerKind == "backup-ok" && !e.Failed:
			return true, "backup completed: " + e.Name, map[string]string{"server": r.ServerID}
		case r.TriggerKind == "backup-fail" && e.Failed:
			return true, "backup failed: " + e.Err, map[string]string{"server": r.ServerID}
		}
		return false, "", nil

	case types.SampleEvent:
		if e.ServerID != r.ServerID || !sampleMatchesKind(r.TriggerKind, e.Kind) {
			return false, "", nil
		}
		return thresholdHeld(r, e, st)

	case types.ScheduleTick:
		if r.TriggerKind != "sched" || !dueBySchedule(r.TriggerConfig, r.LastFiredAt, st.Now) {
			return false, "", nil
		}
		return true, "schedule", map[string]string{"server": r.ServerID}
	}
	return false, "", nil
}

func sampleMatchesKind(triggerKind string, k types.SampleKind) bool {
	return (triggerKind == "tps" && k == types.SampleTPS) ||
		(triggerKind == "count" && k == types.SamplePlayerCount) ||
		(triggerKind == "disk" && k == types.SampleDiskPercent)
}

// thresholdHeld implements "value crossed X for at least N seconds".
//
// It MUTATES st.ConditionSince, which is why the engine owns that map: the
// window has to survive between events. Both directions are real -- TPS is
// "below", player count and disk are "above" -- so the comparison is chosen by
// which key the config carries, not assumed.
func thresholdHeld(r Rule, e types.SampleEvent, st MatchState) (bool, string, map[string]string) {
	var held bool
	var limit float64
	if above, ok := r.TriggerConfig["above"].(float64); ok {
		held, limit = e.Value > above, above
	} else {
		below, _ := r.TriggerConfig["below"].(float64)
		held, limit = e.Value < below, below
	}

	if !held {
		delete(st.ConditionSince, r.ID)
		return false, "", nil
	}
	since, ok := st.ConditionSince[r.ID]
	if !ok {
		st.ConditionSince[r.ID] = st.Now
		return false, "", nil
	}

	var window time.Duration
	if secs, ok := r.TriggerConfig["for_seconds"].(float64); ok {
		window = time.Duration(secs) * time.Second
	}
	if st.Now.Sub(since) < window {
		return false, "", nil
	}

	value := strconv.FormatFloat(e.Value, 'f', -1, 64)
	return true,
		fmt.Sprintf("%s %s held past %s (threshold %s)", e.Kind, value, window, strconv.FormatFloat(limit, 'f', -1, 64)),
		map[string]string{"server": r.ServerID, string(e.Kind): value}
}

// lineMatches implements the contract the UI states: plain text is a literal
// substring search, /.../ is a regular expression.
//
// The distinction matters because ordinary console text is full of regex
// punctuation -- "Can't keep up!" is the line people most want to match, and
// treating it as a pattern would quietly change what it means.
func lineMatches(pattern, line string) bool {
	if pattern == "" {
		return false
	}
	if len(pattern) >= 2 && strings.HasPrefix(pattern, "/") && strings.HasSuffix(pattern, "/") {
		re, err := regexp.Compile(pattern[1 : len(pattern)-1])
		if err != nil {
			// An invalid pattern matches NOTHING until its author fixes it.
			// The alternative -- matching everything -- would hand a rule that
			// runs console commands to every line the server prints.
			return false
		}
		return re.MatchString(line)
	}
	return strings.Contains(line, pattern)
}

// playerEventRe anchors to the real "[HH:MM:SS] [Server thread/INFO]: " prefix
// AND to a bare name filling the rest of the line.
//
// Both anchors are load-bearing. Vanilla chat is "<Name> text", so a player
// can type "Notch joined the game" verbatim; without anchoring, that chat line
// fires a join rule, and a join rule can run console commands. The Discord bot
// in this project learned the same lesson the hard way.
var playerEventRe = regexp.MustCompile(
	`^\[\d{2}:\d{2}:\d{2}\] \[Server thread/INFO\]: ([A-Za-z0-9_]{1,16}) (joined|left) the game$`)

func parsePlayerEvent(line string) (name string, joined bool, ok bool) {
	m := playerEventRe.FindStringSubmatch(line)
	if m == nil {
		return "", false, false
	}
	return m[1], m[2] == "joined", true
}

func matchesPlayerEvent(r Rule, e types.ConsoleLineEvent, st MatchState) (bool, string, map[string]string) {
	name, joined, ok := parsePlayerEvent(e.Line)
	if !ok {
		return false, "", nil
	}
	if joined != (r.TriggerKind == "join") {
		return false, "", nil
	}
	if first, _ := r.TriggerConfig["first_time_only"].(bool); first && st.KnownPlayers[strings.ToLower(name)] {
		return false, "", nil
	}
	verb := "joined"
	if !joined {
		verb = "left"
	}
	return true, name + " " + verb, map[string]string{"player": name, "server": r.ServerID}
}

// dueBySchedule answers "should this run now", given when it last ran.
//
// The tick arrives every minute and this decides, so the schedule is driven by
// last_fired_at rather than an in-memory timer: an API restart neither skips a
// daily job nor fires it twice. Anything unparseable is never due -- a typo in
// a cron field must not become an hourly restart.
func dueBySchedule(cfg map[string]any, last *time.Time, now time.Time) bool {
	switch mode, _ := cfg["mode"].(string); mode {
	case "every":
		hours, _ := cfg["hours"].(float64)
		if hours <= 0 {
			return false
		}
		return last == nil || now.Sub(*last) >= time.Duration(hours*float64(time.Hour))

	case "daily":
		hhmm, _ := cfg["time"].(string)
		target, err := time.Parse("15:04", hhmm)
		if err != nil {
			return false
		}
		due := time.Date(now.Year(), now.Month(), now.Day(), target.Hour(), target.Minute(), 0, 0, now.Location())
		if now.Before(due) {
			return false
		}
		// Without this, a daily 05:00 job fires again at 05:01, 05:02, and
		// every minute until midnight.
		return last == nil || last.Before(due)
	}
	return false
}
