package types

import (
	"fmt"
	"time"
)

// BackupInfo describes a single backup archive.
type BackupInfo struct {
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	Created time.Time `json:"created"`
}

// BackupConfig controls the automatic backup schedule.
type BackupConfig struct {
	Enabled         bool `json:"enabled"`
	IntervalMinutes int  `json:"interval_minutes"`
	Keep            int  `json:"keep"`
}

// ValidateBackupConfig rejects nonsensical schedules before they're persisted.
func ValidateBackupConfig(cfg BackupConfig) error {
	if cfg.IntervalMinutes < 1 {
		return fmt.Errorf("interval_minutes must be at least 1")
	}
	if cfg.Keep < 0 {
		return fmt.Errorf("keep must be zero or greater")
	}
	return nil
}
