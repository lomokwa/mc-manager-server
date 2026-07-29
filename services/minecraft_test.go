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

func TestListPlayers_MissingUserCache(t *testing.T) {
	setupServerDir(t)

	if _, err := ListPlayers(); err == nil {
		t.Error("expected an error when usercache.json is missing")
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
