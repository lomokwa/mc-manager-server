package handlers

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/lomokwa/mc-manager/db"
	"github.com/lomokwa/mc-manager/services"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// newTestRouter returns a bare gin engine suitable for mounting a single
// handler under test.
func newTestRouter() *gin.Engine {
	return gin.New()
}

// setupServerDir creates services.ServerDir (a fixed relative path) fresh for
// a test and removes it during cleanup. Handler tests must not run with
// t.Parallel() against this helper since ServerDir is a shared, fixed path.
func setupServerDir(t *testing.T) string {
	t.Helper()
	if err := os.RemoveAll(services.ServerDir); err != nil {
		t.Fatalf("failed to clear server dir: %v", err)
	}
	if err := os.MkdirAll(services.ServerDir, 0755); err != nil {
		t.Fatalf("failed to create server dir: %v", err)
	}
	t.Cleanup(func() {
		os.RemoveAll(services.ServerDir)
	})
	return services.ServerDir
}

// writeServerFile writes a file relative to services.ServerDir, creating any
// parent directories.
func writeServerFile(t *testing.T, relPath, content string) string {
	t.Helper()
	full := filepath.Join(services.ServerDir, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		t.Fatalf("failed to create parent dir for %s: %v", relPath, err)
	}
	if err := os.WriteFile(full, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write %s: %v", relPath, err)
	}
	return full
}

// setupBackupDir creates services.BackupDir fresh for a test and removes it
// during cleanup.
func setupBackupDir(t *testing.T) string {
	t.Helper()
	if err := os.RemoveAll(services.BackupDir); err != nil {
		t.Fatalf("failed to clear backup dir: %v", err)
	}
	if err := os.MkdirAll(services.BackupDir, 0755); err != nil {
		t.Fatalf("failed to create backup dir: %v", err)
	}
	t.Cleanup(func() {
		os.RemoveAll(services.BackupDir)
	})
	return services.BackupDir
}

// setupTestDB points db.DB at a fresh temp-file sqlite database so tests that
// exercise the DB-backed services don't touch any real database.
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
// services.IsServerRunning starts from a known "not running" state.
func clearStatusFile(t *testing.T) {
	t.Helper()
	os.Remove(services.StatusFilePath)
}
