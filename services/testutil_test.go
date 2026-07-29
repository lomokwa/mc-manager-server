package services

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lomokwa/mc-manager/db"
)

// setupServerDir creates ServerDir (a fixed relative path) fresh for a test
// and removes it during cleanup. Tests using this must not run with
// t.Parallel() since ServerDir is a shared, fixed path.
func setupServerDir(t *testing.T) string {
	t.Helper()
	if err := os.RemoveAll(ServerDir); err != nil {
		t.Fatalf("failed to clear server dir: %v", err)
	}
	if err := os.MkdirAll(ServerDir, 0755); err != nil {
		t.Fatalf("failed to create server dir: %v", err)
	}
	t.Cleanup(func() {
		os.RemoveAll(ServerDir)
	})
	return ServerDir
}

// writeServerFile writes a file relative to ServerDir, creating any parent
// directories.
func writeServerFile(t *testing.T, relPath, content string) string {
	t.Helper()
	full := filepath.Join(ServerDir, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		t.Fatalf("failed to create parent dir for %s: %v", relPath, err)
	}
	if err := os.WriteFile(full, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write %s: %v", relPath, err)
	}
	return full
}

// setupBackupDir creates BackupDir fresh for a test and removes it during
// cleanup.
func setupBackupDir(t *testing.T) string {
	t.Helper()
	if err := os.RemoveAll(BackupDir); err != nil {
		t.Fatalf("failed to clear backup dir: %v", err)
	}
	if err := os.MkdirAll(BackupDir, 0755); err != nil {
		t.Fatalf("failed to create backup dir: %v", err)
	}
	t.Cleanup(func() {
		os.RemoveAll(BackupDir)
	})
	return BackupDir
}

// setupTestDB points db.DB at a fresh temp-file sqlite database so tests
// that exercise DB-backed services don't touch any real database.
func setupTestDB(t *testing.T) {
	t.Helper()
	prev := db.DB
	dbPath := filepath.Join(t.TempDir(), "test.db")
	if err := db.Init(dbPath); err != nil {
		t.Fatalf("failed to init test db: %v", err)
	}
	t.Cleanup(func() {
		db.DB.Close()
		db.DB = prev
	})
}

// clearStatusFile removes any status file left by a prior test so
// IsServerRunning starts from a known "not running" state.
func clearStatusFile(t *testing.T) {
	t.Helper()
	os.Remove(StatusFilePath)
}
