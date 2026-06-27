package services

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/lomokwa/mc-manager/types"
)

const backupTimeLayout = "2006-01-02_15-04-05"

var defaultBackupConfig = types.BackupConfig{Enabled: false, IntervalMinutes: 360, Keep: 10}

// backupReload wakes the scheduler when the config changes.
var backupReload = make(chan struct{}, 1)

func backupsDir() string       { return filepath.Join(ServerDir, "backups") }
func backupConfigPath() string { return filepath.Join(ServerDir, "backup-config.json") }

// worldDirName reads level-name from server.properties (default "world").
func worldDirName() string {
	data, err := os.ReadFile(filepath.Join(ServerDir, "server.properties"))
	if err != nil {
		return "world"
	}
	for _, line := range strings.Split(string(data), "\n") {
		if name, ok := strings.CutPrefix(strings.TrimSpace(line), "level-name="); ok {
			if name = strings.TrimSpace(name); name != "" {
				return name
			}
		}
	}
	return "world"
}

// --- Config ---------------------------------------------------------------

func LoadBackupConfig() types.BackupConfig {
	cfg := defaultBackupConfig
	if data, err := os.ReadFile(backupConfigPath()); err == nil {
		_ = json.Unmarshal(data, &cfg)
	}
	if cfg.IntervalMinutes <= 0 {
		cfg.IntervalMinutes = defaultBackupConfig.IntervalMinutes
	}
	if cfg.Keep < 0 {
		cfg.Keep = 0
	}
	return cfg
}

func SaveBackupConfig(cfg types.BackupConfig) error {
	if cfg.IntervalMinutes <= 0 {
		cfg.IntervalMinutes = defaultBackupConfig.IntervalMinutes
	}
	if cfg.Keep < 0 {
		cfg.Keep = 0
	}
	if err := os.MkdirAll(ServerDir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(backupConfigPath(), data, 0644); err != nil {
		return err
	}
	// Wake the scheduler so the new cadence takes effect immediately.
	select {
	case backupReload <- struct{}{}:
	default:
	}
	return nil
}

// --- Create / list / delete / restore -------------------------------------

// CreateBackup snapshots the world directory into a timestamped zip. If the
// server is running it disables auto-save and flushes first so the snapshot is
// consistent, then prunes old backups per the keep setting.
func CreateBackup() (types.BackupInfo, error) {
	world := filepath.Join(ServerDir, worldDirName())
	if _, err := os.Stat(world); err != nil {
		return types.BackupInfo{}, fmt.Errorf("world directory not found")
	}
	if err := os.MkdirAll(backupsDir(), 0755); err != nil {
		return types.BackupInfo{}, err
	}

	release := flushAndHold()
	defer release()

	name := fmt.Sprintf("world-%s.zip", time.Now().Format(backupTimeLayout))
	dest := filepath.Join(backupsDir(), name)
	if err := zipDir(world, dest); err != nil {
		os.Remove(dest)
		return types.BackupInfo{}, err
	}

	pruneBackups(LoadBackupConfig().Keep)

	info, err := os.Stat(dest)
	if err != nil {
		return types.BackupInfo{}, err
	}
	return types.BackupInfo{Name: name, Size: info.Size(), Created: info.ModTime().UTC().Format(time.RFC3339)}, nil
}

func ListBackups() ([]types.BackupInfo, error) {
	entries, err := os.ReadDir(backupsDir())
	if err != nil {
		if os.IsNotExist(err) {
			return []types.BackupInfo{}, nil
		}
		return nil, err
	}
	out := make([]types.BackupInfo, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".zip") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, types.BackupInfo{Name: e.Name(), Size: info.Size(), Created: info.ModTime().UTC().Format(time.RFC3339)})
	}
	// Newest first.
	sort.Slice(out, func(i, j int) bool { return out[i].Created > out[j].Created })
	return out, nil
}

func DeleteBackup(name string) error {
	if err := validateBackupName(name); err != nil {
		return err
	}
	return os.Remove(filepath.Join(backupsDir(), name))
}

// RestoreBackup overlays a backup onto the world directory. The server must be
// stopped first.
func RestoreBackup(name string) error {
	if err := validateBackupName(name); err != nil {
		return err
	}
	if IsServerRunning() {
		return fmt.Errorf("stop the server before restoring a backup")
	}
	return unzipInto(filepath.Join(backupsDir(), name))
}

func validateBackupName(name string) error {
	if name == "" || filepath.Base(name) != name || !strings.HasSuffix(name, ".zip") {
		return fmt.Errorf("invalid backup name")
	}
	return nil
}

func pruneBackups(keep int) {
	if keep <= 0 {
		return
	}
	list, err := ListBackups()
	if err != nil {
		return
	}
	for i := keep; i < len(list); i++ {
		os.Remove(filepath.Join(backupsDir(), list[i].Name))
	}
}

// --- zip helpers ----------------------------------------------------------

func zipDir(srcDir, destZip string) error {
	out, err := os.Create(destZip)
	if err != nil {
		return err
	}
	defer out.Close()
	zw := zip.NewWriter(out)
	defer zw.Close()

	parent := filepath.Dir(srcDir)
	return filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(parent, path)
		if err != nil {
			return err
		}
		w, err := zw.Create(filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		_, err = io.Copy(w, f)
		f.Close()
		return err
	})
}

func unzipInto(zipPath string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer r.Close()

	base, err := filepath.Abs(ServerDir)
	if err != nil {
		return err
	}
	for _, f := range r.File {
		// Guard against zip-slip: every entry must stay inside the server dir.
		dest := filepath.Join(base, filepath.Clean("/"+f.Name))
		if dest != base && !strings.HasPrefix(dest, base+string(os.PathSeparator)) {
			return fmt.Errorf("backup entry escapes the server directory: %s", f.Name)
		}
		if f.FileInfo().IsDir() {
			os.MkdirAll(dest, 0755)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		w, err := os.Create(dest)
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(w, rc)
		w.Close()
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// flushAndHold disables auto-save and flushes the world (when the server is
// running) so the snapshot is consistent, returning a function that re-enables
// auto-save.
func flushAndHold() func() {
	if !IsServerRunning() {
		return func() {}
	}
	_ = SendCommand("save-off")

	hub := GetLogHub()
	if hub == nil {
		_ = SendCommand("save-all flush")
		time.Sleep(2 * time.Second)
		return func() { _ = SendCommand("save-on") }
	}

	ch := hub.Subscribe()
	// Drain buffered lines so we only match a fresh save confirmation.
drain:
	for {
		select {
		case <-ch:
		default:
			break drain
		}
	}

	_ = SendCommand("save-all flush")
	deadline := time.After(20 * time.Second)
	closed := false
wait:
	for {
		select {
		case line, ok := <-ch:
			if !ok {
				closed = true
				break wait
			}
			if strings.Contains(line, "Saved the game") || strings.Contains(line, "Saved the world") || strings.Contains(line, "All chunks are saved") {
				break wait
			}
		case <-deadline:
			break wait
		}
	}
	if !closed {
		hub.Unsubscribe(ch)
	}
	return func() { _ = SendCommand("save-on") }
}

// --- scheduler ------------------------------------------------------------

// StartBackupScheduler runs periodic backups according to the saved config,
// re-reading it whenever SaveBackupConfig signals a change.
func StartBackupScheduler() {
	go func() {
		for {
			cfg := LoadBackupConfig()
			if !cfg.Enabled || cfg.IntervalMinutes <= 0 {
				<-backupReload // sleep until the config changes
				continue
			}
			timer := time.NewTimer(time.Duration(cfg.IntervalMinutes) * time.Minute)
			select {
			case <-timer.C:
				if _, err := CreateBackup(); err != nil {
					log.Printf("scheduled backup failed: %v", err)
				}
			case <-backupReload:
				timer.Stop()
			}
		}
	}()
}
