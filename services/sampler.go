package services

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/lomokwa/mc-manager/types"
)

const (
	// Sampling floor and ceiling. The floor stops a short window from turning
	// into a tight poll; the ceiling stops a long one from being so lazy the
	// rule feels broken.
	minSampleInterval = 10 * time.Second
	maxSampleInterval = time.Minute
)

// SampleInterval picks how often to measure, given the window a rule cares
// about.
//
// This is the one number in the feature that costs the JVM rather than the API:
// reading TPS means running `spark tps` on the game server, and reading the
// player count means running `list`. "TPS below 15 for 5 minutes" decides
// identically at 30s samples and costs a tenth as much -- so the interval comes
// from the rule's own window instead of a fixed fast poll.
func SampleInterval(window time.Duration) time.Duration {
	if window <= 0 {
		return minSampleInterval
	}
	interval := window / 10
	if interval < minSampleInterval {
		return minSampleInterval
	}
	if interval > maxSampleInterval {
		return maxSampleInterval
	}
	return interval
}

// StartSampler polls only the measurements some enabled rule asks for, and
// returns a stop function.
//
// needs is the engine's NeedsSampling, read live on every tick: turning a rule
// off has to stop its cost immediately, not at the next restart.
func StartSampler(needs func(types.SampleKind) bool, interval func() time.Duration) func() {
	stop := make(chan struct{})
	go func() {
		current := interval()
		ticker := time.NewTicker(current)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				sampleOnce(needs)
				// Re-read after each tick. Editing a rule's window, or
				// disabling the rule that demanded a tight interval, has to
				// change the cost immediately -- otherwise the interval is
				// whatever happened to be configured at boot, which is how
				// SampleInterval ended up computed and then ignored.
				if next := interval(); next != current {
					current = next
					ticker.Reset(current)
				}
			case <-stop:
				return
			}
		}
	}()
	return func() { close(stop) }
}

func sampleOnce(needs func(types.SampleKind) bool) {
	wantPlayers := needs(types.SamplePlayerCount)
	wantTPS := needs(types.SampleTPS)
	wantDisk := needs(types.SampleDiskPercent)
	if !wantPlayers && !wantTPS && !wantDisk {
		// A tick with nothing configured costs three map reads and returns.
		return
	}

	for _, rt := range AllRuntimes() {
		if wantDisk {
			// Disk is measured even for a stopped server: it fills up whether
			// or not the JVM is running, and a full disk is the reason backups
			// start failing.
			if pct, err := DiskPercentUsed(rt.Dir); err == nil {
				types.Bus.Publish(types.SampleEvent{
					ServerID: rt.ID, Kind: types.SampleDiskPercent, Value: pct, At: time.Now(),
				})
			}
		}

		if !rt.IsServerRunning() {
			continue // asking a stopped server anything else is pointless
		}

		if wantPlayers {
			// Reuses GetOnlinePlayers' existing 10s cache rather than opening
			// a second path to `list`.
			if names, err := rt.GetOnlinePlayers(); err == nil {
				types.Bus.Publish(types.SampleEvent{
					ServerID: rt.ID, Kind: types.SamplePlayerCount,
					Value: float64(len(names)), At: time.Now(),
				})
			}
		}

		if wantTPS {
			if tps, err := rt.FetchTPS(); err == nil {
				types.Bus.Publish(types.SampleEvent{
					ServerID: rt.ID, Kind: types.SampleTPS, Value: tps, At: time.Now(),
				})
			}
		}
	}
}

// sparkTPSLine matches a spark TPS reading: at least two comma-separated
// decimals, each optionally starred when that window is degraded.
//
// It is anchored to the REAL hub line, prefix included -- the same shape
// listResponseLine uses for `list`. Every line the hub delivers carries
// "[HH:MM:SS] [Server thread/INFO]: ", so a pattern that starts at the numbers
// matches nothing in production: FetchTPS would time out on every sample and
// TPS rules would never fire, while each tick stalled five seconds per server.
//
// The prefix also does security work. Chat arrives as
// "[HH:MM:SS] [Server thread/INFO]: <Name> text", and requiring the reading to
// fill the whole line after the prefix is what stops a player typing
// "1.0, 2.0, 3.0, 4.0, 5.0" from being read as a catastrophic TPS -- which
// would let them trigger a rule that restarts the server.
var sparkTPSLine = regexp.MustCompile(
	`^\[\d{2}:\d{2}:\d{2}\] \[Server thread/INFO\]:\s*(?:\[⚡\]\s*)?\*?(\d+\.\d+)(?:,\s*\*?\d+\.\d+){1,}$`)

// ParseSparkTPS reads the shortest (5s) window from a spark tps reply, which is
// the one that reacts fast enough to alert on.
func ParseSparkTPS(line string) (float64, bool) {
	m := sparkTPSLine.FindStringSubmatch(strings.TrimSpace(line))
	if m == nil {
		return 0, false
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// FetchTPS runs `spark tps` and reads the reply off this runtime's hub, the
// same shape fetchOnlinePlayers uses for `list`.
//
// This is the measurement that costs the JVM rather than the API, which is why
// nothing calls it unless an enabled rule asked for TPS.
func (rt *ServerRuntime) FetchTPS() (float64, error) {
	hub := rt.Hub
	if hub == nil {
		return 0, fmt.Errorf("log hub not available")
	}

	ch := hub.Subscribe()
	defer hub.Unsubscribe(ch)

	// Drain the replay buffer: a stale reading from a previous sample would
	// otherwise be answered instantly and be minutes old.
draining:
	for {
		select {
		case <-ch:
		default:
			break draining
		}
	}

	if err := rt.SendCommand("spark tps"); err != nil {
		return 0, err
	}

	deadline := time.After(5 * time.Second)
	for {
		select {
		case line := <-ch:
			if tps, ok := ParseSparkTPS(line); ok {
				return tps, nil
			}
		case <-deadline:
			// spark may not be installed. Failing quietly is right: the rule
			// simply never fires, rather than the sampler logging every tick.
			return 0, fmt.Errorf("timed out waiting for a spark tps reply")
		}
	}
}
