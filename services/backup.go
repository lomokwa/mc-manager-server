package services

import (
	"archive/zip"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lomokwa/mc-manager/db"
	"github.com/lomokwa/mc-manager/types"
	"github.com/lomokwa/mc-manager/utils"
)

// backupMu serializes backup creation/restore/delete so a scheduled tick
// can never race a manual request (or vice versa) against the same files.
var backupMu sync.Mutex

// backupNameRE matches the exact filenames CreateBackup generates. Any
// externally-supplied "name" (restore/delete) is validated against this
// before it ever touches the filesystem, which rules out path traversal.
var backupNameRE = regexp.MustCompile(`^world-\d{4}-\d{2}-\d{2}T\d{2}-\d{2}-\d{2}Z\.zip$`)

// ListBackups returns all backups in BackupDir, newest first.
func ListBackups() ([]types.BackupInfo, error) {
	entries, err := os.ReadDir(BackupDir)
	if err != nil {
		if os.IsNotExist(err) {
			// No backups dir yet means no backups have been made — not an error.
			return []types.BackupInfo{}, nil
		}
		return nil, err
	}

	backups := make([]types.BackupInfo, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".zip") {
			continue // ignore subdirs, temp files, anything not a finished backup
		}

		info, err := e.Info()
		if err != nil {
			continue // skip unreadable entries rather than failing the whole list
		}

		backups = append(backups, types.BackupInfo{
			Name:    e.Name(),
			Size:    info.Size(),
			Created: info.ModTime(),
		})
	}

	sort.Slice(backups, func(i, j int) bool {
		return backups[i].Created.After(backups[j].Created)
	})

	return backups, nil
}

// resolveBackupPath validates name against backupNameRE and returns its
// absolute path inside BackupDir. This is the only way a caller-supplied
// name should ever be turned into a filesystem path.
func resolveBackupPath(name string) (string, error) {
	if !backupNameRE.MatchString(name) {
		return "", fmt.Errorf("invalid backup name")
	}
	return filepath.Join(BackupDir, name), nil
}

// CreateBackup snapshots the world directory (plus key config files) into a
// new timestamped zip archive under BackupDir.
func CreateBackup() (types.BackupInfo, error) {
	backupMu.Lock()
	defer backupMu.Unlock()

	worldPath := filepath.Join(ServerDir, "world")
	if !utils.FileExists(worldPath) {
		return types.BackupInfo{}, fmt.Errorf("no world to back up, start the server at least once first")
	}

	if err := os.MkdirAll(BackupDir, 0755); err != nil {
		return types.BackupInfo{}, fmt.Errorf("failed to create backups directory: %w", err)
	}

	// If the server is running, flush pending chunk writes and pause
	// autosave so the zip doesn't capture a half-written region file.
	running := IsServerRunning()
	if running {
		if err := SendCommand("save-off"); err != nil {
			log.Printf("backup: failed to send save-off: %v", err)
		}
		if err := SendCommand("save-all flush"); err != nil {
			log.Printf("backup: failed to send save-all flush: %v", err)
		}
		// Give the server a moment to finish flushing before we start reading files.
		time.Sleep(2 * time.Second)
		defer func() {
			if err := SendCommand("save-on"); err != nil {
				log.Printf("backup: failed to send save-on: %v", err)
			}
		}()
	}

	name := fmt.Sprintf("world-%s.zip", time.Now().UTC().Format("2006-01-02T15-04-05Z"))
	finalPath := filepath.Join(BackupDir, name)
	tmpPath := finalPath + ".tmp"

	if err := writeBackupZip(tmpPath, worldPath); err != nil {
		os.Remove(tmpPath)
		return types.BackupInfo{}, fmt.Errorf("failed to create backup: %w", err)
	}

	if err := os.Rename(tmpPath, finalPath); err != nil {
		os.Remove(tmpPath)
		return types.BackupInfo{}, fmt.Errorf("failed to finalize backup: %w", err)
	}

	info, err := os.Stat(finalPath)
	if err != nil {
		return types.BackupInfo{}, fmt.Errorf("backup created but could not be stat'd: %w", err)
	}

	return types.BackupInfo{
		Name:    name,
		Size:    info.Size(),
		Created: info.ModTime(),
	}, nil
}

// configFiles are copied alongside world/ so a restore returns the server to
// a fully working state, not just the map data.
var configFiles = []string{
	"server.properties",
	"ops.json",
	"whitelist.json",
	"banned-players.json",
	"banned-ips.json",
}

// writeBackupZip streams worldPath and the known config files into a zip at
// destPath. Files are copied via io.Copy so large worlds aren't loaded into
// memory.
func writeBackupZip(destPath, worldPath string) error {
	out, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer out.Close()

	zw := zip.NewWriter(out)

	err = filepath.Walk(worldPath, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if fi.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(ServerDir, path)
		if err != nil {
			return err
		}
		return addFileToZip(zw, path, filepath.ToSlash(rel))
	})
	if err != nil {
		zw.Close()
		return err
	}

	for _, f := range configFiles {
		src := filepath.Join(ServerDir, f)
		if !utils.FileExists(src) {
			continue
		}
		if err := addFileToZip(zw, src, f); err != nil {
			zw.Close()
			return err
		}
	}

	if err := zw.Close(); err != nil {
		return err
	}
	return out.Close()
}

func addFileToZip(zw *zip.Writer, srcPath, zipPath string) error {
	in, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer in.Close()

	w, err := zw.Create(zipPath)
	if err != nil {
		return err
	}

	_, err = io.Copy(w, in)
	return err
}

// RestoreBackup replaces the live world/ directory with the contents of the
// named backup. The server must be stopped first (enforced by the caller).
// The existing world is moved aside rather than deleted outright, so a
// failed extraction doesn't destroy the live world.
func RestoreBackup(name string) error {
	backupMu.Lock()
	defer backupMu.Unlock()

	if IsServerRunning() {
		return fmt.Errorf("stop the server before restoring a backup")
	}

	archivePath, err := resolveBackupPath(name)
	if err != nil {
		return err
	}
	if !utils.FileExists(archivePath) {
		return fmt.Errorf("backup %q not found", name)
	}

	worldPath := filepath.Join(ServerDir, "world")
	backupWorldPath := worldPath + fmt.Sprintf(".bak-%d", time.Now().UnixNano())

	if utils.FileExists(worldPath) {
		if err := os.Rename(worldPath, backupWorldPath); err != nil {
			return fmt.Errorf("failed to move existing world aside: %w", err)
		}
	}

	if err := extractBackupZip(archivePath, ServerDir); err != nil {
		// Roll back: restore the original world so we don't leave the
		// server without any world at all.
		os.RemoveAll(worldPath)
		if utils.FileExists(backupWorldPath) {
			os.Rename(backupWorldPath, worldPath)
		}
		return fmt.Errorf("failed to restore backup: %w", err)
	}

	if utils.FileExists(backupWorldPath) {
		os.RemoveAll(backupWorldPath)
	}

	return nil
}

// extractBackupZip extracts archivePath into destDir, guarding against
// zip-slip path traversal from a malformed or malicious archive.
func extractBackupZip(archivePath, destDir string) error {
	r, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer r.Close()

	destAbs, err := filepath.Abs(destDir)
	if err != nil {
		return err
	}

	for _, f := range r.File {
		target := filepath.Join(destAbs, f.Name)
		if !strings.HasPrefix(target, destAbs+string(os.PathSeparator)) && target != destAbs {
			return fmt.Errorf("illegal file path in backup archive: %s", f.Name)
		}

		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}

		if err := extractZipFile(f, target); err != nil {
			return err
		}
	}

	return nil
}

func extractZipFile(f *zip.File, target string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()

	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, rc)
	return err
}

// DeleteBackup removes a single backup archive.
func DeleteBackup(name string) error {
	backupMu.Lock()
	defer backupMu.Unlock()

	path, err := resolveBackupPath(name)
	if err != nil {
		return err
	}
	if !utils.FileExists(path) {
		return fmt.Errorf("backup %q not found", name)
	}
	return os.Remove(path)
}

// BackupFilePath validates name and returns the absolute path to the backup
// archive within BackupDir, for handlers that need to serve the file itself
// (e.g. download). Read-only, so it doesn't need backupMu.
func BackupFilePath(name string) (string, error) {
	path, err := resolveBackupPath(name)
	if err != nil {
		return "", err
	}
	if !utils.FileExists(path) {
		return "", fmt.Errorf("backup %q not found", name)
	}
	return path, nil
}

// PruneBackups deletes the oldest backups beyond the keep count. keep <= 0
// means "keep everything" (no pruning).
func PruneBackups(keep int) error {
	if keep <= 0 {
		return nil
	}

	backups, err := ListBackups() // already newest-first
	if err != nil {
		return err
	}

	if len(backups) <= keep {
		return nil
	}

	var firstErr error
	for _, b := range backups[keep:] {
		path := filepath.Join(BackupDir, b.Name)
		if err := os.Remove(path); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// LoadBackupConfig reads the single backup_config row, returning sane
// defaults if it hasn't been created yet.
func LoadBackupConfig() (types.BackupConfig, error) {
	cfg := types.BackupConfig{Enabled: false, IntervalMinutes: 1440, Keep: 7}

	row := db.DB.QueryRow(`SELECT enabled, interval_minutes, keep FROM backup_config WHERE id = 1`)
	err := row.Scan(&cfg.Enabled, &cfg.IntervalMinutes, &cfg.Keep)
	if err != nil {
		if err.Error() == "sql: no rows in result set" {
			return cfg, nil
		}
		return cfg, fmt.Errorf("failed to load backup config: %w", err)
	}

	return cfg, nil
}

// SaveBackupConfig upserts the single backup_config row.
func SaveBackupConfig(cfg types.BackupConfig) error {
	_, err := db.DB.Exec(`
		INSERT INTO backup_config (id, enabled, interval_minutes, keep)
		VALUES (1, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			enabled = excluded.enabled,
			interval_minutes = excluded.interval_minutes,
			keep = excluded.keep
	`, cfg.Enabled, cfg.IntervalMinutes, cfg.Keep)
	if err != nil {
		return fmt.Errorf("failed to save backup config: %w", err)
	}
	return nil
}
