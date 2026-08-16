package services

import (
	"fmt"
	"sync"
	"time"

	"github.com/lomokwa/mc-manager/types"
)

// ServerRuntime holds the per-server state that used to be process-wide
// globals: one log hub, one console-write mutex, one backup mutex, one
// online-players cache -- all of it scattered across logtail.go, process.go,
// backup.go and minecraft.go as package-level vars before Phase 1. Always
// used as a pointer (never copied) since it embeds sync.Mutex/sync.Once
// fields.
type ServerRuntime struct {
	ID string
	// Dir is this server's root directory exactly as stored in the
	// registry -- for the default runtime that's the ServerDir constant
	// itself (a relative path, resolved against the process's CWD like
	// today), not necessarily an absolute path. Callers that need an
	// absolute path for a security boundary check (see handlers/files.go's
	// safePath) resolve it with filepath.Abs themselves, same as before.
	Dir string
	Hub *types.LogHub // set once by startRuntimeTailer; nil until then

	tailOnce sync.Once  // ensures startRuntimeTailer launches tailLoopFor at most once per runtime
	stdinMu  sync.Mutex // serializes this runtime's own writes to its console FIFO
	backupMu sync.Mutex // serializes this runtime's backup create/restore/delete

	// onlinePlayers* back GetOnlinePlayers' cache (minecraft.go), scoped per
	// runtime so two servers' player lists can never bleed into each other.
	// Zero value (empty slice, zero time.Time) means "cache is empty",
	// which is exactly right for a freshly-constructed runtime.
	onlinePlayersMu     sync.Mutex
	onlinePlayersCache  []string
	onlinePlayersCached time.Time
}

// Path derivation below mirrors the plain string concatenation
// constants.go already uses (ServerJarPath = ServerDir + "/server.jar",
// etc.) rather than filepath.Join. That choice is deliberate: for the
// default runtime, whose Dir is exactly the ServerDir constant, every
// method here must produce a result that is byte-identical to its
// constants.go counterpart -- filepath.Join would silently clean away the
// leading "./" and break that equality (functionally harmless for file
// I/O, but it would defeat the regression test that proves nothing moved;
// see runtime_test.go).

// ControlDir is where mc-supervisor and this API exchange FIFOs/status.json
// for this server (see constants.go's ControlDir doc for the full contract).
func (rt *ServerRuntime) ControlDir() string { return rt.Dir + "/" + ControlDirName }

func (rt *ServerRuntime) ConsoleFifoPath() string { return rt.ControlDir() + "/console.in" }
func (rt *ServerRuntime) ControlFifoPath() string { return rt.ControlDir() + "/control.in" }
func (rt *ServerRuntime) StatusFilePath() string  { return rt.ControlDir() + "/status.json" }
func (rt *ServerRuntime) LatestLogPath() string   { return rt.Dir + "/logs/latest.log" }
func (rt *ServerRuntime) ServerJarPath() string   { return rt.Dir + "/server.jar" }
func (rt *ServerRuntime) ServerMetaPath() string  { return rt.Dir + "/server-meta.json" }

// BackupDir is the one path that does NOT derive from Dir the way the
// others do. PLAN-multi-server.md (D1) is explicit that the default
// server's backups stay exactly where they already are on the live
// deployment (BackupDir, i.e. "./backups") rather than moving under an
// id-namespaced subdirectory -- that directory already has real backups in
// it. Only non-default servers, which start with nothing, get the
// id-namespaced path.
func (rt *ServerRuntime) BackupDir() string {
	if rt.ID == DefaultServerID {
		return BackupDir
	}
	return "./backups/" + rt.ID
}

var (
	runtimesMu sync.RWMutex
	runtimes   = map[string]*ServerRuntime{}
)

// getOrCreateRuntime returns the cached runtime for id, creating and
// caching one rooted at dir the first time id is seen. This lazy path is
// what lets DefaultRuntime() (and therefore every pre-Phase-1 package-level
// wrapper: SendCommand, IsServerRunning, CreateBackup, ...) keep working
// even for a caller -- true of nearly the entire test suite -- that never
// calls LoadRuntimes first.
func getOrCreateRuntime(id, dir string) *ServerRuntime {
	runtimesMu.RLock()
	rt, ok := runtimes[id]
	runtimesMu.RUnlock()
	if ok {
		return rt
	}

	runtimesMu.Lock()
	defer runtimesMu.Unlock()
	if rt, ok := runtimes[id]; ok { // lost a race with another caller
		return rt
	}
	rt = &ServerRuntime{ID: id, Dir: dir}
	runtimes[id] = rt
	return rt
}

// LoadRuntimes reads the servers registry and builds a ServerRuntime for
// each row, starting that server's own log tailer (see logtail.go's
// startRuntimeTailer). Call once at boot, after EnsureDefaultServer. Safe
// to call again later -- an already-known id's tailer is never started
// twice (tailOnce), and its Dir is never overwritten by a second call.
func LoadRuntimes() error {
	servers, err := ListServers()
	if err != nil {
		return fmt.Errorf("failed to load servers for runtime init: %w", err)
	}

	for _, s := range servers {
		rt := getOrCreateRuntime(s.ID, s.Dir)
		startRuntimeTailer(rt)
	}
	return nil
}

// DefaultRuntime returns the runtime for DefaultServerID, creating it from
// the fixed ServerDir constant if the registry hasn't been loaded yet (e.g.
// most of the existing test suite, which predates the registry entirely).
// This is the compatibility shim every pre-Phase-1 exported function calls
// internally -- it's what lets every existing caller keep compiling and
// behaving identically, since as far as they know there is still exactly
// one server, at exactly the path it has always been at.
func DefaultRuntime() *ServerRuntime {
	return getOrCreateRuntime(DefaultServerID, ServerDir)
}

// RuntimeForID resolves id to its *ServerRuntime, the way Phase 3's
// middleware.ResolveServer turns a request's :sid into the runtime a
// namespaced handler operates on (see PLAN-multi-server.md D3/D4).
//
// Security: id is used ONLY as a lookup key into GetServer's parameterized
// query -- never as, or as part of, a filesystem path. The Dir on the
// returned runtime always comes from the matched row's own dir column, not
// from id itself, so a traversal-shaped id (e.g. "../../etc") simply fails
// to match any row and comes back as GetServer's ordinary "not found" error
// -- there is no code path where such a string ever reaches the filesystem.
// Unlike DefaultRuntime, this never fabricates a runtime for an id that
// doesn't exist in the registry.
// AllRuntimes returns every loaded runtime, for callers that must act across
// all servers rather than one -- the automation sampler and its "has this
// player joined before" lookup are the first.
//
// Returns a fresh slice: the caller must not be able to reach the registry map
// and mutate it while another goroutine reads.
func AllRuntimes() []*ServerRuntime {
	runtimesMu.RLock()
	defer runtimesMu.RUnlock()
	out := make([]*ServerRuntime, 0, len(runtimes))
	for _, rt := range runtimes {
		out = append(out, rt)
	}
	return out
}

func RuntimeForID(id string) (*ServerRuntime, error) {
	s, err := GetServer(id)
	if err != nil {
		return nil, err
	}
	return getOrCreateRuntime(s.ID, s.Dir), nil
}
