package services

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadPlayerStats(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "stats"), 0755); err != nil {
		t.Fatal(err)
	}
	uuid := "069a79f4-44e9-4726-a5be-fca90e38aaf5"
	body := `{"stats":{"minecraft:custom":{"minecraft:play_time":1512000,"minecraft:deaths":7}}}`
	if err := os.WriteFile(filepath.Join(dir, "stats", uuid+".json"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}

	pt, deaths, ok := readPlayerStats(dir, uuid)
	if !ok {
		t.Fatal("expected ok for an existing stats file")
	}
	if pt != 1512000 {
		t.Errorf("playtime = %d, want 1512000", pt)
	}
	if deaths != 7 {
		t.Errorf("deaths = %d, want 7", deaths)
	}

	if _, _, ok := readPlayerStats(dir, "00000000-0000-0000-0000-000000000000"); ok {
		t.Error("expected ok=false for a missing stats file")
	}
}

func TestReadPlayerStatsLegacyPlaytimeKey(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "stats"), 0755); err != nil {
		t.Fatal(err)
	}
	uuid := "legacy"
	body := `{"stats":{"minecraft:custom":{"minecraft:play_one_minute":1200}}}`
	if err := os.WriteFile(filepath.Join(dir, "stats", uuid+".json"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}

	pt, _, ok := readPlayerStats(dir, uuid)
	if !ok || pt != 1200 {
		t.Errorf("legacy playtime = %d ok=%v, want 1200 true", pt, ok)
	}
}

func TestSessionPatterns(t *testing.T) {
	join := joinPattern.FindStringSubmatch("[12:34:56] [Server thread/INFO]: Steve joined the game")
	if join == nil || join[1] != "Steve" {
		t.Errorf("join match = %v, want capture \"Steve\"", join)
	}

	leave := leavePattern.FindStringSubmatch("[12:34:56] [Server thread/INFO]: Steve left the game")
	if leave == nil || leave[1] != "Steve" {
		t.Errorf("leave match = %v, want capture \"Steve\"", leave)
	}

	// A chat message mentioning the phrase must not be treated as a join: the
	// log's "INFO]: " separator is immediately followed by "<", not a name.
	if joinPattern.MatchString("[12:00:00] [Server thread/INFO]: <Bob> I joined the game yesterday") {
		t.Error("a chat line should not match the join pattern")
	}
}
