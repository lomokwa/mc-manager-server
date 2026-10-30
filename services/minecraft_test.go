package services

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPrepareServerFiles_WritesEulaAndProperties(t *testing.T) {
	dir := t.TempDir()

	if err := PrepareServerFiles(dir, false, true, map[string]string{"gamemode": "creative"}); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	eula, err := os.ReadFile(filepath.Join(dir, "eula.txt"))
	if err != nil {
		t.Fatalf("expected eula.txt to be written: %v", err)
	}
	if string(eula) != "eula=true" {
		t.Errorf("unexpected eula.txt content: %q", eula)
	}

	props, err := os.ReadFile(filepath.Join(dir, "server.properties"))
	if err != nil {
		t.Fatalf("expected server.properties to be written: %v", err)
	}
	content := string(props)
	if !strings.Contains(content, "gamemode=creative") {
		t.Errorf("expected overridden gamemode in properties, got %q", content)
	}
	if !strings.Contains(content, "difficulty=") {
		t.Errorf("expected default properties to be present, got %q", content)
	}
}

func TestPrepareServerFiles_SkipsPropertiesWhenNotConfigured(t *testing.T) {
	dir := t.TempDir()

	if err := PrepareServerFiles(dir, false, false, nil); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "server.properties")); !os.IsNotExist(err) {
		t.Error("expected server.properties to not be created")
	}
}

func TestPrepareServerFiles_CreatesLaunchScripts(t *testing.T) {
	dir := t.TempDir()

	if err := PrepareServerFiles(dir, true, false, nil); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	shPath := filepath.Join(dir, "start-server.sh")
	if _, err := os.Stat(shPath); err != nil {
		t.Fatalf("expected start-server.sh to exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "start-server.bat")); err != nil {
		t.Fatalf("expected start-server.bat to exist: %v", err)
	}

	if runtime.GOOS != "windows" {
		info, err := os.Stat(shPath)
		if err != nil {
			t.Fatalf("failed to stat start-server.sh: %v", err)
		}
		if info.Mode().Perm()&0111 == 0 {
			t.Error("expected start-server.sh to be executable")
		}
	}
}

func TestSaveAndLoadServerMeta_RoundTrip(t *testing.T) {
	setupServerDir(t)

	meta := ServerMeta{ServerType: "fabric", GameVersion: "1.21", LoaderVersion: "0.15.0"}
	if err := SaveServerMeta(meta); err != nil {
		t.Fatalf("failed to save server meta: %v", err)
	}

	loaded, err := LoadServerMeta()
	if err != nil {
		t.Fatalf("failed to load server meta: %v", err)
	}
	if *loaded != meta {
		t.Errorf("expected %+v, got %+v", meta, *loaded)
	}
}

func TestLoadServerMeta_MissingFile(t *testing.T) {
	setupServerDir(t)

	if _, err := LoadServerMeta(); err == nil {
		t.Error("expected an error when server-meta.json is missing")
	}
}

func TestGetServerProperties_Success(t *testing.T) {
	setupServerDir(t)
	writeServerFile(t, "server.properties", "gamemode=survival\n# a comment\n\ndifficulty=hard\n")

	props, err := GetServerProperties()
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if props["gamemode"] != "survival" || props["difficulty"] != "hard" {
		t.Errorf("unexpected properties: %+v", props)
	}
	if len(props) != 2 {
		t.Errorf("expected comments/blank lines to be skipped, got %+v", props)
	}
}

func TestGetServerProperties_MissingFile(t *testing.T) {
	setupServerDir(t)

	if _, err := GetServerProperties(); err == nil {
		t.Error("expected an error when server.properties is missing")
	}
}

func TestUpdateServerProperties_MergesAndPreserves(t *testing.T) {
	setupServerDir(t)
	writeServerFile(t, "server.properties", "gamemode=survival\ndifficulty=easy\n")

	if err := UpdateServerProperties(map[string]string{"gamemode": "creative", "pvp": "false"}); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	props, err := GetServerProperties()
	if err != nil {
		t.Fatalf("failed to reload properties: %v", err)
	}
	if props["gamemode"] != "creative" {
		t.Errorf("expected gamemode to be updated, got %q", props["gamemode"])
	}
	if props["difficulty"] != "easy" {
		t.Errorf("expected difficulty to be preserved, got %q", props["difficulty"])
	}
	if props["pvp"] != "false" {
		t.Errorf("expected new pvp property to be added, got %q", props["pvp"])
	}
}

func TestUpdateServerProperties_MissingFile(t *testing.T) {
	setupServerDir(t)

	if err := UpdateServerProperties(map[string]string{"gamemode": "creative"}); err == nil {
		t.Error("expected an error when server.properties is missing")
	}
}

func TestListPlayers_Success(t *testing.T) {
	setupServerDir(t)
	writeServerFile(t, "usercache.json", `[
		{"uuid": "11111111-1111-1111-1111-111111111111", "name": "Alice", "expiresOn": "2099-01-01"}
	]`)
	writeServerFile(t, "ops.json", `[{"uuid": "11111111-1111-1111-1111-111111111111", "name": "Alice", "level": 4}]`)

	players, err := ListPlayers()
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(players) != 1 {
		t.Fatalf("expected 1 player, got %d", len(players))
	}
	if !players[0].IsOp {
		t.Error("expected Alice to be op")
	}
	if players[0].Online {
		t.Error("expected Alice to be offline (server not running)")
	}
}

// NOTE: this used to be TestListPlayers_MissingUserCache, asserting the exact
// opposite -- that a missing usercache.json produced an error. That behaviour
// is what this change deliberately reverses, so the assertion had to flip with
// it; TestListPlayers_MissingUserCacheIsNotAnError below is its replacement.

func TestDeletePlayer_NotRunning_NeverLoggedIn_RemovesFromEverything(t *testing.T) {
	setupServerDir(t)
	clearStatusFile(t)
	writeServerFile(t, "usercache.json", `[
		{"uuid": "11111111-1111-1111-1111-111111111111", "name": "Alice", "expiresOn": "2099-01-01"}
	]`)
	writeServerFile(t, "ops.json", `[{"uuid": "11111111-1111-1111-1111-111111111111", "name": "Alice", "level": 4}]`)
	writeServerFile(t, "whitelist.json", `[{"uuid": "11111111-1111-1111-1111-111111111111", "name": "Alice"}]`)

	result, err := DefaultRuntime().DeletePlayer("11111111-1111-1111-1111-111111111111")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !result.Deopped {
		t.Error("expected Deopped to be true")
	}
	if !result.Unwhitelisted {
		t.Error("expected Unwhitelisted to be true")
	}
	if !result.UsercacheRemoved {
		t.Error("expected UsercacheRemoved to be true since the player never logged in")
	}
	if result.Kicked {
		t.Error("expected Kicked to be false (server not running)")
	}

	opSet, err := DefaultRuntime().loadUUIDs("ops.json")
	if err != nil || opSet["11111111-1111-1111-1111-111111111111"] {
		t.Errorf("expected Alice to be removed from ops.json, opSet=%v err=%v", opSet, err)
	}
	whitelistSet, err := DefaultRuntime().loadUUIDs("whitelist.json")
	if err != nil || whitelistSet["11111111-1111-1111-1111-111111111111"] {
		t.Errorf("expected Alice to be removed from whitelist.json, whitelistSet=%v err=%v", whitelistSet, err)
	}

	data, err := os.ReadFile(filepath.Join(ServerDir, "usercache.json"))
	if err != nil {
		t.Fatalf("failed to read usercache.json: %v", err)
	}
	if strings.Contains(string(data), "Alice") {
		t.Errorf("expected usercache.json to no longer contain Alice, got %s", data)
	}
}

func TestDeletePlayer_NotRunning_HasLoggedIn_KeepsUsercacheEntry(t *testing.T) {
	setupServerDir(t)
	clearStatusFile(t)
	writeServerFile(t, "usercache.json", `[
		{"uuid": "11111111-1111-1111-1111-111111111111", "name": "Alice", "expiresOn": "2099-01-01"}
	]`)
	writeServerFile(t, "ops.json", `[{"uuid": "11111111-1111-1111-1111-111111111111", "name": "Alice", "level": 4}]`)
	writeServerFile(t, "world/playerdata/11111111-1111-1111-1111-111111111111.dat", "fake nbt data")

	result, err := DefaultRuntime().DeletePlayer("11111111-1111-1111-1111-111111111111")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !result.Deopped {
		t.Error("expected Deopped to be true")
	}
	if result.UsercacheRemoved {
		t.Error("expected UsercacheRemoved to be false since the player has actually logged in")
	}

	data, err := os.ReadFile(filepath.Join(ServerDir, "usercache.json"))
	if err != nil {
		t.Fatalf("failed to read usercache.json: %v", err)
	}
	if !strings.Contains(string(data), "Alice") {
		t.Errorf("expected usercache.json to still contain Alice, got %s", data)
	}
}

func TestDeletePlayer_NotFound(t *testing.T) {
	setupServerDir(t)
	clearStatusFile(t)
	writeServerFile(t, "usercache.json", `[]`)

	if _, err := DefaultRuntime().DeletePlayer("11111111-1111-1111-1111-111111111111"); err == nil {
		t.Error("expected an error for an unknown uuid")
	}
}

func TestDeleteServer_RemovesContentsButKeepsDir(t *testing.T) {
	setupServerDir(t)
	writeServerFile(t, "server.jar", "fake jar")
	writeServerFile(t, "world/level.dat", "data")

	if err := DeleteServer(); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	entries, err := os.ReadDir(ServerDir)
	if err != nil {
		t.Fatalf("expected ServerDir to still exist: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected ServerDir to be empty, got %+v", entries)
	}
}

// usercache.json was the one file whose absence failed the whole listing (500),
// while ops/whitelist/banned all degraded to empty. Two ordinary situations hit
// it -- a server nobody has joined yet, and a read that catches Minecraft
// mid-rewrite -- and both took down the panel's Players page and the Discord
// bot's status line at once.
func TestListPlayers_MissingUserCacheIsNotAnError(t *testing.T) {
	setupServerDir(t)
	clearStatusFile(t)
	rt := &ServerRuntime{Dir: ServerDir}

	players, err := rt.ListPlayers()
	if err != nil {
		t.Fatalf("a missing usercache.json must not fail the listing, got: %v", err)
	}
	if len(players) != 0 {
		t.Errorf("expected an empty list, got %d players", len(players))
	}
}

func TestListPlayers_TruncatedUserCacheIsNotAnError(t *testing.T) {
	setupServerDir(t)
	clearStatusFile(t)
	// What a read caught mid-rewrite actually looks like: valid JSON, cut off.
	writeServerFile(t, "usercache.json", `[{"uuid":"abc","name":"Ste`)
	rt := &ServerRuntime{Dir: ServerDir}

	players, err := rt.ListPlayers()
	if err != nil {
		t.Fatalf("a truncated usercache.json must not fail the listing, got: %v", err)
	}
	if len(players) != 0 {
		t.Errorf("expected an empty list, got %d players", len(players))
	}
}

// The op/whitelist/ban flags still have to come through when usercache is fine.
func TestListPlayers_StillReadsStatusFlags(t *testing.T) {
	setupServerDir(t)
	clearStatusFile(t)
	writeServerFile(t, "usercache.json", `[{"uuid":"uuid-steve","name":"Steve"}]`)
	writeServerFile(t, "ops.json", `[{"uuid":"uuid-steve"}]`)
	rt := &ServerRuntime{Dir: ServerDir}

	players, err := rt.ListPlayers()
	if err != nil {
		t.Fatalf("ListPlayers failed: %v", err)
	}
	if len(players) != 1 {
		t.Fatalf("expected 1 player, got %d", len(players))
	}
	if players[0].Name != "Steve" || !players[0].IsOp {
		t.Errorf("expected Steve to come back as an op, got %+v", players[0])
	}
}

// A corrupt ops.json must not blank the listing either -- and must fail in the
// SAFE direction, reading as not-an-op rather than granting anything.
func TestListPlayers_CorruptOpsFileDegradesToNotAnOp(t *testing.T) {
	setupServerDir(t)
	clearStatusFile(t)
	writeServerFile(t, "usercache.json", `[{"uuid":"uuid-steve","name":"Steve"}]`)
	writeServerFile(t, "ops.json", `[{"uuid":"uuid-ste`)
	rt := &ServerRuntime{Dir: ServerDir}

	players, err := rt.ListPlayers()
	if err != nil {
		t.Fatalf("a corrupt ops.json must not fail the listing, got: %v", err)
	}
	if len(players) != 1 {
		t.Fatalf("expected 1 player, got %d", len(players))
	}
	if players[0].IsOp {
		t.Error("an unreadable ops.json must never grant op")
	}
}
