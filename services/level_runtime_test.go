package services

import (
	"os"
	"path/filepath"
	"testing"
)

// GetWorldInfo has to work per-runtime, and it has to survive the states a
// real server passes through: no server.properties yet, a properties file
// that doesn't set level-name, a renamed world, and a world whose level.dat
// hasn't been generated. None of those are errors -- the panel calls this on
// every Players load, so anything that isn't "answer with what you know"
// turns into the 404-shaped breakage this endpoint exists to end.

func TestLevelName_FallsBackToVanillaDefault(t *testing.T) {
	dir := t.TempDir()
	rt := &ServerRuntime{ID: "t", Dir: dir}

	// No server.properties at all.
	if got := rt.levelName(); got != "world" {
		t.Errorf("expected the vanilla default with no properties file, got %q", got)
	}

	// Present, but silent about level-name.
	if err := os.WriteFile(filepath.Join(dir, "server.properties"), []byte("motd=hi\npvp=true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := rt.levelName(); got != "world" {
		t.Errorf("expected the vanilla default when level-name is unset, got %q", got)
	}

	// Blank value -- an operator clearing the field must not make us probe "".
	if err := os.WriteFile(filepath.Join(dir, "server.properties"), []byte("level-name=   \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := rt.levelName(); got != "world" {
		t.Errorf("expected a blank level-name to fall back, got %q", got)
	}
}

func TestLevelName_HonoursARenamedWorld(t *testing.T) {
	dir := t.TempDir()
	rt := &ServerRuntime{ID: "t", Dir: dir}
	if err := os.WriteFile(filepath.Join(dir, "server.properties"), []byte("level-name=survival_s3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := rt.levelName(); got != "survival_s3" {
		t.Errorf("expected survival_s3, got %q", got)
	}
}

func TestGetWorldInfo_AnswersWithoutASpawnWhenTheWorldIsUngenerated(t *testing.T) {
	rt := &ServerRuntime{ID: "t", Dir: t.TempDir()}

	info := rt.GetWorldInfo()

	if info.LevelName != "world" {
		t.Errorf("expected the level name to still be reported, got %q", info.LevelName)
	}
	if info.Spawn != nil {
		t.Errorf("expected no spawn for a world that was never generated, got %+v", info.Spawn)
	}
}

// Two runtimes must not read each other's world -- the whole reason this moved
// off the fixed ServerDir.
func TestGetWorldInfo_IsPerRuntime(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(a, "server.properties"), []byte("level-name=alpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b, "server.properties"), []byte("level-name=beta\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rtA := &ServerRuntime{ID: "a", Dir: a}
	rtB := &ServerRuntime{ID: "b", Dir: b}

	if got := rtA.GetWorldInfo().LevelName; got != "alpha" {
		t.Errorf("runtime a: expected alpha, got %q", got)
	}
	if got := rtB.GetWorldInfo().LevelName; got != "beta" {
		t.Errorf("runtime b: expected beta, got %q", got)
	}
}
