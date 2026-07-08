package types

// BackupInfo describes a stored world backup.
type BackupInfo struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	Created string `json:"created"` // RFC3339
}

// BackupConfig controls periodic (scheduled) backups.
type BackupConfig struct {
	Enabled         bool `json:"enabled"`
	IntervalMinutes int  `json:"interval_minutes"`
	Keep            int  `json:"keep"`
}
