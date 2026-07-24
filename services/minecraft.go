package services

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
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
	log.Printf("downloading version manifest")
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

	log.Printf("downloading latest version details")
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

	log.Printf("downloading server jar to %s", destPath)
	err = utils.DownloadFile(serverJarUrl, destPath)
	if err != nil {
		return fmt.Errorf("failed to download server.jar: %s", err)
	}

	log.Printf("server jar download complete")

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

	log.Printf("downloading fabric server jar to %s", destPath)
	if err := utils.DownloadFile(jarURL, destPath); err != nil {
		return fmt.Errorf("failed to download fabric server jar: %w", err)
	}

	log.Printf("fabric server jar download complete")
	return nil
}

func PrepareServerFiles(serverDir string, createLaunchScript bool, configureProperties bool, requestProperties map[string]string) error {
	log.Printf("preparing server files in %s", serverDir)
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
		log.Printf("writing server.properties")
		if err := utils.WriteFile(filepath.Join(serverDir, "server.properties"), propertiesContent); err != nil {
			return err
		}
	}

	if createLaunchScript {
		log.Printf("writing launch scripts")
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

	log.Printf("server file preparation complete")

	return nil
}

func loadUUIDs(filename string) (map[string]bool, error) {
	data, err := os.ReadFile(filepath.Join(ServerDir, filename))
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
		return nil, fmt.Errorf("failed to decode %s: %w", filename, err)
	}

	set := make(map[string]bool, len(entries))
	for _, e := range entries {
		set[e.UUID] = true
	}

	return set, nil
}

// listResponseLine matches vanilla's own "/list" reply, anchored to the real leading
// "[HH:MM:SS] [Server thread/INFO]: " prefix a genuine server-generated console line always carries. The
// previous strings.Contains(line, "players online:") check also matched that exact substring inside a
// PLAYER'S OWN chat message (e.g. someone typing "There are 99 players online: Herobrine" in game chat),
// which would spoof a fake player list back to GetOnlinePlayers' caller. Mirrors the anchoring convention
// selton-mello-bot's own consoleStream.ts already uses for this same class of bug.
var listResponseLine = regexp.MustCompile(`^\[\d{2}:\d{2}:\d{2}\] \[Server thread/INFO\]: There are \d+ of a max of \d+ players online:\s*(.*)$`)

var (
	onlinePlayersMu       sync.Mutex
	onlinePlayersCache    []string
	onlinePlayersCachedAt time.Time
)

// onlinePlayersCacheTTL: fetchOnlinePlayers doesn't just read a file -- it runs a REAL "/list" command
// against the live Minecraft console and waits (up to 5s) for the reply. A caller polling this every few
// seconds (e.g. a Discord bot's presence rotation) was paying that full round trip every single time.
const onlinePlayersCacheTTL = 10 * time.Second

// GetOnlinePlayers returns the currently-online player names, reusing a recent result within
// onlinePlayersCacheTTL instead of re-querying the live server console on every call.
func GetOnlinePlayers() ([]string, error) {
	onlinePlayersMu.Lock()
	if !onlinePlayersCachedAt.IsZero() && time.Since(onlinePlayersCachedAt) < onlinePlayersCacheTTL {
		cached := onlinePlayersCache
		onlinePlayersMu.Unlock()
		return cached, nil
	}
	onlinePlayersMu.Unlock()

	names, err := fetchOnlinePlayers()
	if err != nil {
		return nil, err
	}

	onlinePlayersMu.Lock()
	onlinePlayersCache = names
	onlinePlayersCachedAt = time.Now()
	onlinePlayersMu.Unlock()

	return names, nil
}

// fetchOnlinePlayers is the original always-live implementation, now only ever reached through
// GetOnlinePlayers' cache above: sends "list" to the server console and parses the real reply.
func fetchOnlinePlayers() ([]string, error) {
	hub := GetLogHub()
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

	if err := SendCommand("list"); err != nil {
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

func ListPlayers() ([]types.Player, error) {
	data, err := os.ReadFile(filepath.Join(ServerDir, "usercache.json"))
	if err != nil {
		return nil, err
	}

	var userCache []types.UserCacheEntry
	if err := json.Unmarshal(data, &userCache); err != nil {
		return nil, fmt.Errorf("failed to decode usercache.json: %w", err)
	}

	// Load status set
	opSet, err := loadUUIDs("ops.json")
	if err != nil {
		return nil, err
	}

	whitelistSet, err := loadUUIDs("whitelist.json")
	if err != nil {
		return nil, err
	}

	bannedSet, err := loadUUIDs("banned-players.json")
	if err != nil {
		return nil, err
	}

	// Get online players
	onlineSet := make(map[string]bool)
	if IsServerRunning() {
		names, err := GetOnlinePlayers()
		if err != nil {
			log.Printf("could not find online players")
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

func DeleteServer() error {
	// Remove everything in the server directory except the directory itself
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

func GetServerProperties() (map[string]string, error) {
	data, err := os.ReadFile(filepath.Join(ServerDir, "server.properties"))
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

func UpdateServerProperties(properties map[string]string) error {
	data, err := os.ReadFile(filepath.Join(ServerDir, "server.properties"))
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

	return utils.WriteFile(filepath.Join(ServerDir, "server.properties"), []byte(content.String()))
}
