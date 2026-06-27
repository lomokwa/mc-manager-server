package services

import (
	"regexp"
	"sync"
	"time"

	"github.com/lomokwa/mc-manager/types"
)

var (
	sessionMu    sync.RWMutex
	sessionJoins = make(map[string]time.Time)
)

var (
	joinPattern  = regexp.MustCompile(`: (\w{1,16}) joined the game`)
	leavePattern = regexp.MustCompile(`: (\w{1,16}) left the game`)
)

// trackSessions watches the console stream for join/leave lines and records
// when each online player joined, so the API can report current-session time.
// It subscribes to the given hub and runs until the hub is closed (i.e. the
// server stops), at which point it clears the recorded sessions.
func trackSessions(hub *types.LogHub) {
	ch := hub.Subscribe()
	resetSessions()
	for line := range ch {
		if m := joinPattern.FindStringSubmatch(line); m != nil {
			sessionMu.Lock()
			sessionJoins[m[1]] = time.Now()
			sessionMu.Unlock()
		} else if m := leavePattern.FindStringSubmatch(line); m != nil {
			sessionMu.Lock()
			delete(sessionJoins, m[1])
			sessionMu.Unlock()
		}
	}
	resetSessions()
}

func resetSessions() {
	sessionMu.Lock()
	sessionJoins = make(map[string]time.Time)
	sessionMu.Unlock()
}

// sessionStart returns when the named player joined the current session, if known.
func sessionStart(name string) (time.Time, bool) {
	sessionMu.RLock()
	defer sessionMu.RUnlock()
	t, ok := sessionJoins[name]
	return t, ok
}
