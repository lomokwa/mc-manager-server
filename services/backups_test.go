package services

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func TestValidateBackupName(t *testing.T) {
	good := []string{"world-2026-01-01_00-00-00.zip", "world-x.zip"}
	bad := []string{"", "world", "world.txt", "../evil.zip", "sub/world.zip", "world.zip/.."}
	for _, n := range good {
		if err := validateBackupName(n); err != nil {
			t.Errorf("good name %q rejected: %v", n, err)
		}
	}
	for _, n := range bad {
		if err := validateBackupName(n); err == nil {
			t.Errorf("bad name %q accepted", n)
		}
	}
}

func TestLoadBackupConfigDefaults(t *testing.T) {
	cfg := LoadBackupConfig()
	if cfg.IntervalMinutes <= 0 {
		t.Errorf("interval should be positive, got %d", cfg.IntervalMinutes)
	}
	if cfg.Keep < 0 {
		t.Errorf("keep should be >= 0, got %d", cfg.Keep)
	}
}

func TestZipDir(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(src, "region"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "region", "b.txt"), []byte("world"), 0644); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(t.TempDir(), "out.zip")
	if err := zipDir(src, dest); err != nil {
		t.Fatal(err)
	}

	r, err := zip.OpenReader(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	names := map[string]bool{}
	for _, f := range r.File {
		names[f.Name] = true
	}
	bn := filepath.Base(src)
	if !names[bn+"/a.txt"] || !names[bn+"/region/b.txt"] {
		t.Errorf("zip entries = %v, want %s/a.txt and %s/region/b.txt", names, bn, bn)
	}
}
