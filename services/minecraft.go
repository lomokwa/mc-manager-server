package services

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/lomokwa/mc-manager/types"
	"github.com/lomokwa/mc-manager/utils"
)

type ServerMeta struct {
	ServerType    string `json:"serverType"`
	GameVersion   string `json:"gameVersion"`
	LoaderVersion string `json:"loaderVersion,omitempty"`
}

func SaveServerMeta(meta ServerMeta) error {
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal server meta: %w", err)
	}
	return utils.WriteFile(ServerMetaPath, data)
}

func LoadServerMeta() (*ServerMeta, error) {
	data, err := os.ReadFile(ServerMetaPath)
	if err != nil {
		return nil, err
	}
	var meta ServerMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("failed to decode server meta: %w", err)
	}
	return &meta, nil
}

func DownloadServerJar(destPath string, releaseVersion string) error {
	slog.Info("downloading version manifest")
	res, err := http.Get("https://launchermeta.mojang.com/mc/game/version_manifest.json")
	if err != nil {
		return fmt.Errorf("failed to fetch version manifest")
	}

	defer res.Body.Close()

	var manifest struct {
		Latest struct {
			Release  string `json:"release"`
			Snapshot string `json:"snapshot"`
		} `json:"latest"`
		Versions []struct {
			ID   string `json:"id"`
			Type string `json:"type"`
			URL  string `json:"url"`
		}
	}

	if err := json.NewDecoder(res.Body).Decode(&manifest); err != nil {
		return fmt.Errorf("failed to decode version manifest")
	}

	var releaseId string
	if releaseVersion != "" {
		releaseId = releaseVersion
	} else {
		releaseId = manifest.Latest.Release
	}

	var versionUrl string
	for _, version := range manifest.Versions {
		if version.ID == releaseId {
			versionUrl = version.URL
			break
		}
	}

	if versionUrl == "" {
		return fmt.Errorf("latest version URL not found")
	}

	slog.Info("downloading latest version details")
	versionRes, err := http.Get(versionUrl)
	if err != nil {
		return fmt.Errorf("failed to fetch latest version details")
	}
	defer versionRes.Body.Close()

	var versionDetails struct {
		Downloads struct {
			Server struct {
				URL string `json:"url"`
			} `json:"server"`
		} `json:"downloads"`
	}

	if err := json.NewDecoder(versionRes.Body).Decode(&versionDetails); err != nil {
		return fmt.Errorf("failed to decode version details")
	}

	serverJarUrl := versionDetails.Downloads.Server.URL

	slog.Info("downloading server jar", "dest", destPath)
	err = utils.DownloadFile(serverJarUrl, destPath)
	if err != nil {
		return fmt.Errorf("failed to download server.jar: %s", err)
	}

	slog.Info("server jar download complete")

	return nil
}

func DownloadFabricJar(destPath string, gameVersion string, loaderVersion string) error {
	// Get latest installer version
	res, err := http.Get("https://meta.fabricmc.net/v2/versions/installer")
	if err != nil {
		return fmt.Errorf("failed to fetch fabric installer versions: %w", err)
	}
	defer res.Body.Close()

	var installers []struct {
		Version string `json:"version"`
		Stable  bool   `json:"stable"`
	}
	if err := json.NewDecoder(res.Body).Decode(&installers); err != nil {
		return fmt.Errorf("failed to decode fabric installer versions: %w", err)
	}

	if len(installers) == 0 {
		return fmt.Errorf("no fabric installer versions found")
	}

	installerVersion := installers[0].Version

	jarURL := fmt.Sprintf(
		"https://meta.fabricmc.net/v2/versions/loader/%s/%s/%s/server/jar",
		gameVersion, loaderVersion, installerVersion,
	)

	slog.Info("downloading fabric server jar", "dest", destPath)
	if err := utils.DownloadFile(jarURL, destPath); err != nil {
		return fmt.Errorf("failed to download fabric server jar: %w", err)
	}

	slog.Info("fabric server jar download complete")
	return nil
}

func PrepareServerFiles(serverDir string, createLaunchScript bool, configureProperties bool, requestProperties map[string]string) error {
	slog.Info("preparing server files", "dir", serverDir)
	if err := utils.WriteFile(filepath.Join(serverDir, "eula.txt"), []byte("eula=true")); err != nil {
		return err
	}

	// Create server.properties file content.
	properties := make(map[string]string, len(DefaultServerProperties))
	for k, v := range DefaultServerProperties {
		properties[k] = v
	}

	for k, v := range requestProperties {
		properties[k] = v
	}

	var content strings.Builder
	for k, v := range properties {
		fmt.Fprintf(&content, "%s=%s\n", k, v)
	}

	propertiesContent := []byte(content.String())
	if configureProperties {
		slog.Debug("writing server.properties")
		if err := utils.WriteFile(filepath.Join(serverDir, "server.properties"), propertiesContent); err != nil {
			return err
		}
	}

	if createLaunchScript {
		slog.Debug("writing launch scripts")
		shellScriptPath := filepath.Join(serverDir, "start-server.sh")
		batScriptPath := filepath.Join(serverDir, "start-server.bat")

		if err := utils.WriteFile(shellScriptPath, []byte(DefaultStartServerShellScript)); err != nil {
			return fmt.Errorf("failed to write start-server.sh: %w", err)
		}

		if err := os.Chmod(shellScriptPath, 0755); err != nil {
			return fmt.Errorf("failed to set executable permission on start-server.sh: %w", err)
		}

		if err := utils.WriteFile(batScriptPath, []byte(DefaultStartServerBatchScript)); err != nil {
			return fmt.Errorf("failed to write start-server.bat: %w", err)
		}
	}

	slog.Info("server file preparation complete")

	return nil
}

// loadUserCache reads the name<->UUID cache Minecraft keeps in usercache.json,
// returning nil rather than an error when it can't.
//
// Every other file the player listing reads already degrades to "empty" when
// it's missing; usercache.json was the one exception, and it failed the whole
// call -- which ListPlayersHandler turns into a 500. That's a full outage for
// both consumers at once: the panel's Players page, and the Discord bot, whose
// status line reads this endpoint. Two ordinary situations reach it. A server
// nobody has joined yet has no usercache.json at all. And Minecraft rewrites
// the file periodically, so a read can catch it mid-write and get truncated
// JSON.
//
// Neither means the server is unreachable -- the online list, op flags and ban
// flags are all still perfectly readable. Missing is silent (that's just a new
// server); unreadable or malformed is logged, since that one may be worth
// seeing even though it no longer takes the endpoint down.
func (rt *ServerRuntime) loadUserCache() []types.UserCacheEntry {
	data, err := os.ReadFile(filepath.Join(rt.Dir, "usercache.json"))
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("could not read usercache.json, listing players without it", "err", err)
		}
		return nil
	}

	var entries []types.UserCacheEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		slog.Warn("usercache.json is not valid JSON, listing players without it", "err", err)
		return nil
	}
	return entries
}

func (rt *ServerRuntime) loadUUIDs(filename string) (map[string]bool, error) {
	data, err := os.ReadFile(filepath.Join(rt.Dir, filename))
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]bool), nil
		}
		return nil, err
	}

	var entries []struct {
		UUID string `json:"uuid"`
	}
	if err := json.Unmarshal(data, &entries); err != nil {
		// Same reasoning as loadUserCache: these files are rewritten live by the
		// server, and one bad read shouldn't blank the whole player list.
		// Degrading to an empty set fails in the safe direction -- an op reads
		// as not-an-op, so permission checks built on this deny rather than allow.
		slog.Warn("could not decode a player list file, treating it as empty", "file", filename, "err", err)
		return make(map[string]bool), nil
	}

	set := make(map[string]bool, len(entries))
	for _, e := range entries {
		set[e.UUID] = true
	}

	return set, nil
}

// removeFromOps removes uuid from ops.json if present. It's the
// server-not-running counterpart to sending "deop <name>" to a live server:
// vanilla Minecraft writes this same file itself when the JVM processes a
// live deop, so this keeps stopped-server behavior consistent with running
// behavior.
func (rt *ServerRuntime) removeFromOps(uuid string) (bool, error) {
	path := filepath.Join(rt.Dir, "ops.json")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	var entries []types.OpEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return false, fmt.Errorf("failed to decode ops.json: %w", err)
	}

	out := entries[:0]
	removed := false
	for _, e := range entries {
		if e.UUID == uuid {
			removed = true
			continue
		}
		out = append(out, e)
	}
	if !removed {
		return false, nil
	}

	updated, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return false, fmt.Errorf("failed to encode ops.json: %w", err)
	}
	if err := utils.WriteFileAtomic(path, updated); err != nil {
		return false, fmt.Errorf("failed to write ops.json: %w", err)
	}
	return true, nil
}

// removeFromWhitelist is removeFromOps' whitelist.json counterpart.
func (rt *ServerRuntime) removeFromWhitelist(uuid string) (bool, error) {
	path := filepath.Join(rt.Dir, "whitelist.json")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	var entries []types.WhitelistEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return false, fmt.Errorf("failed to decode whitelist.json: %w", err)
	}

	out := entries[:0]
	removed := false
	for _, e := range entries {
		if e.UUID == uuid {
			removed = true
			continue
		}
		out = append(out, e)
	}
	if !removed {
		return false, nil
	}

	updated, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return false, fmt.Errorf("failed to encode whitelist.json: %w", err)
	}
	if err := utils.WriteFileAtomic(path, updated); err != nil {
		return false, fmt.Errorf("failed to write whitelist.json: %w", err)
	}
	return true, nil
}

// hasPlayerLoggedIn reports whether uuid has actually connected to this
// server at least once. usercache.json alone isn't proof of that: vanilla
// Minecraft adds a usercache entry the moment it resolves a name to a UUID
// (e.g. for a whitelist/op add by name), well before the player ever joins.
// Player data on disk only exists after a real join, so its presence is
// used as the real signal instead.
func (rt *ServerRuntime) hasPlayerLoggedIn(uuid string) bool {
	levelName := "world"
	if props, err := rt.GetServerProperties(); err == nil {
		if v, ok := props["level-name"]; ok && v != "" {
			levelName = v
		}
	}
	path := filepath.Join(rt.Dir, levelName, "playerdata", uuid+".dat")
	_, err := os.Stat(path)
	return err == nil
}

// listResponseLine matches vanilla's own "/list" reply, anchored to the real leading
// "[HH:MM:SS] [Server thread/INFO]: " prefix a genuine server-generated console line always carries. The
// previous strings.Contains(line, "players online:") check also matched that exact substring inside a
// PLAYER'S OWN chat message (e.g. someone typing "There are 99 players online: Herobrine" in game chat),
// which would spoof a fake player list back to GetOnlinePlayers' caller. Mirrors the anchoring convention
// selton-mello-bot's own consoleStream.ts already uses for this same class of bug.
var listResponseLine = regexp.MustCompile(`^\[\d{2}:\d{2}:\d{2}\] \[Server thread/INFO\]: There are \d+ of a max of \d+ players online:\s*(.*)$`)

// onlinePlayersCacheTTL: fetchOnlinePlayers doesn't just read a file -- it runs a REAL "/list" command
// against the live Minecraft console and waits (up to 5s) for the reply. A caller polling this every few
// seconds (e.g. a Discord bot's presence rotation) was paying that full round trip every single time.
const onlinePlayersCacheTTL = 10 * time.Second

// GetOnlinePlayers returns this runtime's currently-online player names,
// reusing a recent result within onlinePlayersCacheTTL instead of
// re-querying the live server console on every call. The cache lives on rt
// (see runtime.go), so two servers' player lists can never bleed together.
func (rt *ServerRuntime) GetOnlinePlayers() ([]string, error) {
	rt.onlinePlayersMu.Lock()
	if !rt.onlinePlayersCached.IsZero() && time.Since(rt.onlinePlayersCached) < onlinePlayersCacheTTL {
		cached := rt.onlinePlayersCache
		rt.onlinePlayersMu.Unlock()
		return cached, nil
	}
	rt.onlinePlayersMu.Unlock()

	names, err := rt.fetchOnlinePlayers()
	if err != nil {
		return nil, err
	}

	rt.onlinePlayersMu.Lock()
	rt.onlinePlayersCache = names
	rt.onlinePlayersCached = time.Now()
	rt.onlinePlayersMu.Unlock()

	return names, nil
}

// fetchOnlinePlayers is the original always-live implementation, now only ever reached through
// GetOnlinePlayers' cache above: sends "list" to this runtime's server console and parses the real reply.
func (rt *ServerRuntime) fetchOnlinePlayers() ([]string, error) {
	hub := rt.Hub
	if hub == nil {
		return nil, fmt.Errorf("log hub not available")
	}

	ch := hub.Subscribe()
	defer hub.Unsubscribe(ch)

draining:
	for {
		select {
		case <-ch:
		default:
			break draining
		}
	}

	if err := rt.SendCommand("list"); err != nil {
		return nil, err
	}

	for {
		select {
		case line := <-ch:
			match := listResponseLine.FindStringSubmatch(line)
			if match == nil {
				continue
			}
			if match[1] == "" {
				return []string{}, nil
			}

			names := strings.Split(match[1], ", ")
			for i := range names {
				names[i] = strings.TrimSpace(names[i])
			}

			return names, nil

		case <-time.After(5 * time.Second):
			return nil, fmt.Errorf("timed out waiting for player list")
		}
	}
}

func (rt *ServerRuntime) ListPlayers() ([]types.Player, error) {
	userCache := rt.loadUserCache()

	// Load status set
	opSet, err := rt.loadUUIDs("ops.json")
	if err != nil {
		return nil, err
	}

	whitelistSet, err := rt.loadUUIDs("whitelist.json")
	if err != nil {
		return nil, err
	}

	bannedSet, err := rt.loadUUIDs("banned-players.json")
	if err != nil {
		return nil, err
	}

	// Get online players
	onlineSet := make(map[string]bool)
	if rt.IsServerRunning() {
		names, err := rt.GetOnlinePlayers()
		if err != nil {
			slog.Warn("could not read the online player list", "err", err)
			for _, n := range names {
				onlineSet[n] = false
			}
		} else {
			for _, name := range names {
				onlineSet[name] = true
			}
		}
	}

	players := make([]types.Player, 0, len(userCache))
	for _, u := range userCache {
		players = append(players, types.Player{
			UUID:          u.UUID,
			Name:          u.Name,
			Online:        onlineSet[u.Name],
			IsOp:          opSet[u.UUID],
			IsBanned:      bannedSet[u.UUID],
			IsWhitelisted: whitelistSet[u.UUID],
		})
	}
	return players, nil
}

// DeletePlayer fully removes a player from this server. If the server is
// running, it kicks them (if currently online), deops them, and removes
// them from the whitelist via live console commands, so the running JVM's
// own in-memory state and the files it owns stay in sync. If the server is
// stopped, there's no JVM to send commands to, so ops.json/whitelist.json
// are edited directly instead. Either way, the usercache.json entry itself
// is only removed if the player has never actually joined -- see
// hasPlayerLoggedIn -- since a player who has joined has other traces
// (playerdata, stats) that reference their UUID/name, and pruning the
// usercache entry for someone real risks orphaning those references.
func (rt *ServerRuntime) DeletePlayer(uuid string) (types.PlayerDeletionResult, error) {
	data, err := os.ReadFile(filepath.Join(rt.Dir, "usercache.json"))
	if err != nil {
		return types.PlayerDeletionResult{}, err
	}

	var userCache []types.UserCacheEntry
	if err := json.Unmarshal(data, &userCache); err != nil {
		return types.PlayerDeletionResult{}, fmt.Errorf("failed to decode usercache.json: %w", err)
	}

	targetIndex := -1
	for i, player := range userCache {
		if player.UUID == uuid {
			targetIndex = i
			break
		}
	}
	if targetIndex == -1 {
		return types.PlayerDeletionResult{}, fmt.Errorf("no player found with uuid %q", uuid)
	}
	name := userCache[targetIndex].Name

	var result types.PlayerDeletionResult

	if rt.IsServerRunning() {
		online, err := rt.GetOnlinePlayers()
		if err != nil {
			slog.Warn("delete player: could not check online status", "err", err)
		}
		for _, n := range online {
			if n == name {
				if err := rt.SendCommand("kick " + name); err != nil {
					slog.Warn("delete player: failed to kick", "name", name, "err", err)
				} else {
					result.Kicked = true
				}
				break
			}
		}

		if err := rt.SendCommand("deop " + name); err != nil {
			slog.Warn("delete player: failed to deop", "name", name, "err", err)
		} else {
			result.Deopped = true
		}

		if err := rt.SendCommand("whitelist remove " + name); err != nil {
			slog.Warn("delete player: failed to remove from whitelist", "name", name, "err", err)
		} else {
			result.Unwhitelisted = true
		}
	} else {
		deopped, err := rt.removeFromOps(uuid)
		if err != nil {
			return types.PlayerDeletionResult{}, err
		}
		result.Deopped = deopped

		unwhitelisted, err := rt.removeFromWhitelist(uuid)
		if err != nil {
			return types.PlayerDeletionResult{}, err
		}
		result.Unwhitelisted = unwhitelisted
	}

	if !rt.hasPlayerLoggedIn(uuid) {
		userCache = append(userCache[:targetIndex], userCache[targetIndex+1:]...)

		updated, err := json.MarshalIndent(userCache, "", "  ")
		if err != nil {
			return result, fmt.Errorf("failed to encode usercache.json: %w", err)
		}
		if err := utils.WriteFileAtomic(filepath.Join(rt.Dir, "usercache.json"), updated); err != nil {
			return result, fmt.Errorf("failed to write usercache.json: %w", err)
		}
		result.UsercacheRemoved = true
	}

	return result, nil
}

// GetServerProperties reads and parses this runtime's own server.properties
// file (comments and blank lines skipped), rooted at rt.Dir rather than the
// fixed ServerDir constant -- see PLAN-multi-server.md D4.
func (rt *ServerRuntime) GetServerProperties() (map[string]string, error) {
	data, err := os.ReadFile(filepath.Join(rt.Dir, "server.properties"))
	if err != nil {
		return nil, fmt.Errorf("failed to read server.properties: %w", err)
	}

	props := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			props[parts[0]] = parts[1]
		}
	}
	return props, nil
}

// UpdateServerProperties merges properties into this runtime's existing
// server.properties, preserving any existing key not present in properties.
func (rt *ServerRuntime) UpdateServerProperties(properties map[string]string) error {
	data, err := os.ReadFile(filepath.Join(rt.Dir, "server.properties"))
	if err != nil {
		return fmt.Errorf("failed to read server.properties: %w", err)
	}

	existing := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			existing[parts[0]] = parts[1]
		}
	}

	for k, v := range properties {
		existing[k] = v
	}

	var content strings.Builder
	for k, v := range existing {
		fmt.Fprintf(&content, "%s=%s\n", k, v)
	}

	return utils.WriteFile(filepath.Join(rt.Dir, "server.properties"), []byte(content.String()))
}

// --- Package-level wrappers over the default runtime ------------------------
//
// All of these existed before Phase 1 introduced ServerRuntime and must keep
// behaving identically for the single server that exists today (see
// DefaultRuntime in runtime.go) -- in particular, ListPlayers is what
// selton-mello-bot's flat /api/players call ultimately reaches, so this is
// exactly the compatibility path PLAN-multi-server.md D3 requires.

func GetOnlinePlayers() ([]string, error) {
	return DefaultRuntime().GetOnlinePlayers()
}

func ListPlayers() ([]types.Player, error) {
	return DefaultRuntime().ListPlayers()
}

func GetServerProperties() (map[string]string, error) {
	return DefaultRuntime().GetServerProperties()
}

func UpdateServerProperties(properties map[string]string) error {
	return DefaultRuntime().UpdateServerProperties(properties)
}

// DeleteServer removes everything in the (fixed, default-only) ServerDir
// except the directory itself. Deliberately NOT a ServerRuntime method or
// namespaced under /api/servers/:sid: it backs DELETE /api/server, which
// PLAN-multi-server.md's Phase 3 scope explicitly leaves flat-only -- the
// registry has no create/delete/update yet (that needs the Phase 2
// supervisor to actually run more than one JVM first), so there is only
// ever one server whose files this could mean.
func DeleteServer() error {
	entries, err := os.ReadDir(ServerDir)
	if err != nil {
		return fmt.Errorf("failed to read server directory: %w", err)
	}

	for _, entry := range entries {
		path := filepath.Join(ServerDir, entry.Name())
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("failed to remove %s: %w", path, err)
		}
	}

	return nil
}
