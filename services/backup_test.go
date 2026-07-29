package services

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lomokwa/mc-manager/types"
)

func TestListBackups_NoBackupDir(t *testing.T) {
	os.RemoveAll(BackupDir)
	t.Cleanup(func() { os.RemoveAll(BackupDir) })

	backups, err := ListBackups()
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(backups) != 0 {
		t.Errorf("expected empty list, got %+v", backups)
	}
}

func TestListBackups_IgnoresNonZipEntries(t *testing.T) {
	dir := setupBackupDir(t)
	os.WriteFile(filepath.Join(dir, "world-2024-01-01T00-00-00Z.zip"), []byte("a"), 0644)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("a"), 0644)
	os.Mkdir(filepath.Join(dir, "subdir"), 0755)

	backups, err := ListBackups()
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(backups) != 1 {
		t.Fatalf("expected 1 backup, got %+v", backups)
	}
	if backups[0].Name != "world-2024-01-01T00-00-00Z.zip" {
		t.Errorf("unexpected backup name: %s", backups[0].Name)
	}
}

func TestResolveBackupPath_ViaBackupFilePath(t *testing.T) {
	setupBackupDir(t)

	invalidNames := []string{
		"../../etc/passwd",
		"world.zip",
		"world-2024-01-01.zip",
		"",
	}
	for _, name := range invalidNames {
		if _, err := BackupFilePath(name); err == nil {
			t.Errorf("expected %q to be rejected as an invalid backup name", name)
		}
	}
}

func TestCreateBackup_NoWorld(t *testing.T) {
	setupServerDir(t)
	setupBackupDir(t)

	if _, err := CreateBackup(); err == nil {
		t.Error("expected an error when there is no world directory")
	}
}

func TestCreateBackup_Success(t *testing.T) {
	setupServerDir(t)
	setupBackupDir(t)
	writeServerFile(t, "world/level.dat", "world data")
	writeServerFile(t, "server.properties", "gamemode=survival\n")

	info, err := CreateBackup()
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if info.Name == "" {
		t.Error("expected a non-empty backup name")
	}
	if info.Size == 0 {
		t.Error("expected a non-zero backup size")
	}

	if _, err := os.Stat(filepath.Join(BackupDir, info.Name)); err != nil {
		t.Errorf("expected backup file to exist: %v", err)
	}
}

func TestDeleteBackup_InvalidName(t *testing.T) {
	setupBackupDir(t)

	if err := DeleteBackup("../../etc/passwd"); err == nil {
		t.Error("expected an error for an invalid backup name")
	}
}

func TestDeleteBackup_NotFound(t *testing.T) {
	setupBackupDir(t)

	if err := DeleteBackup("world-2024-01-01T00-00-00Z.zip"); err == nil {
		t.Error("expected an error for a missing backup")
	}
}

func TestDeleteBackup_Success(t *testing.T) {
	dir := setupBackupDir(t)
	name := "world-2024-01-01T00-00-00Z.zip"
	os.WriteFile(filepath.Join(dir, name), []byte("a"), 0644)

	if err := DeleteBackup(name); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
		t.Error("expected backup file to be removed")
	}
}

func TestPruneBackups_KeepsNewestN(t *testing.T) {
	dir := setupBackupDir(t)
	names := []string{
		"world-2024-01-01T00-00-00Z.zip",
		"world-2024-01-02T00-00-00Z.zip",
		"world-2024-01-03T00-00-00Z.zip",
	}
	base := time.Now().Add(-time.Hour)
	for i, name := range names {
		p := filepath.Join(dir, name)
		os.WriteFile(p, []byte("a"), 0644)
		mtime := base.Add(time.Duration(i) * time.Minute)
		os.Chtimes(p, mtime, mtime)
	}

	if err := PruneBackups(2); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	remaining, err := ListBackups()
	if err != nil {
		t.Fatalf("failed to list backups: %v", err)
	}
	if len(remaining) != 2 {
		t.Fatalf("expected 2 backups to remain, got %d", len(remaining))
	}
	for _, b := range remaining {
		if b.Name == "world-2024-01-01T00-00-00Z.zip" {
			t.Error("expected the oldest backup to be pruned")
		}
	}
}

func TestPruneBackups_KeepZeroOrNegativeIsNoop(t *testing.T) {
	dir := setupBackupDir(t)
	os.WriteFile(filepath.Join(dir, "world-2024-01-01T00-00-00Z.zip"), []byte("a"), 0644)

	if err := PruneBackups(0); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	remaining, _ := ListBackups()
	if len(remaining) != 1 {
		t.Errorf("expected no pruning with keep=0, got %d remaining", len(remaining))
	}
}

func TestCreateAndRestoreBackup_RoundTrip(t *testing.T) {
	setupServerDir(t)
	setupBackupDir(t)
	writeServerFile(t, "world/level.dat", "original world data")

	info, err := CreateBackup()
	if err != nil {
		t.Fatalf("failed to create backup: %v", err)
	}

	writeServerFile(t, "world/level.dat", "corrupted world data")

	if err := RestoreBackup(info.Name); err != nil {
		t.Fatalf("failed to restore backup: %v", err)
	}

	restored, err := os.ReadFile(filepath.Join(ServerDir, "world", "level.dat"))
	if err != nil {
		t.Fatalf("failed to read restored file: %v", err)
	}
	if string(restored) != "original world data" {
		t.Errorf("expected restored content, got %q", restored)
	}
}

func TestRestoreBackup_ServerRunning(t *testing.T) {
	setupServerDir(t)
	setupBackupDir(t)
	writeStatus(t, types.ServerRuntimeStatus{Running: true, Heartbeat: time.Now()})

	if err := RestoreBackup("world-2024-01-01T00-00-00Z.zip"); err == nil {
		t.Error("expected an error when the server is running")
	}
}

func TestRestoreBackup_NotFound(t *testing.T) {
	setupServerDir(t)
	setupBackupDir(t)

	if err := RestoreBackup("world-2024-01-01T00-00-00Z.zip"); err == nil {
		t.Error("expected an error for a missing backup")
	}
}

func TestLoadBackupConfig_Defaults(t *testing.T) {
	setupTestDB(t)

	cfg, err := LoadBackupConfig()
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if cfg.Enabled || cfg.IntervalMinutes != 1440 || cfg.Keep != 7 {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
}

func TestSaveAndLoadBackupConfig_RoundTrip(t *testing.T) {
	setupTestDB(t)

	want := types.BackupConfig{Enabled: true, IntervalMinutes: 60, Keep: 3}
	if err := SaveBackupConfig(want); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	got, err := LoadBackupConfig()
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}
	if got != want {
		t.Errorf("expected %+v, got %+v", want, got)
	}
}

func TestSaveBackupConfig_UpsertsOnSecondSave(t *testing.T) {
	setupTestDB(t)

	if err := SaveBackupConfig(types.BackupConfig{Enabled: true, IntervalMinutes: 60, Keep: 3}); err != nil {
		t.Fatalf("failed first save: %v", err)
	}
	if err := SaveBackupConfig(types.BackupConfig{Enabled: false, IntervalMinutes: 120, Keep: 5}); err != nil {
		t.Fatalf("failed second save: %v", err)
	}

	got, err := LoadBackupConfig()
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}
	want := types.BackupConfig{Enabled: false, IntervalMinutes: 120, Keep: 5}
	if got != want {
		t.Errorf("expected %+v, got %+v", want, got)
	}
}
