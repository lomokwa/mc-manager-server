package types

import "testing"

func TestValidateBackupConfig_Valid(t *testing.T) {
	cases := []BackupConfig{
		{Enabled: true, IntervalMinutes: 1, Keep: 0},
		{Enabled: false, IntervalMinutes: 1440, Keep: 7},
		{Enabled: true, IntervalMinutes: 60, Keep: 100},
	}
	for _, cfg := range cases {
		if err := ValidateBackupConfig(cfg); err != nil {
			t.Errorf("expected %+v to be valid, got error: %v", cfg, err)
		}
	}
}

func TestValidateBackupConfig_IntervalTooLow(t *testing.T) {
	cases := []int{0, -1, -100}
	for _, interval := range cases {
		cfg := BackupConfig{Enabled: true, IntervalMinutes: interval, Keep: 7}
		if err := ValidateBackupConfig(cfg); err == nil {
			t.Errorf("expected interval_minutes=%d to be invalid", interval)
		}
	}
}

func TestValidateBackupConfig_KeepNegative(t *testing.T) {
	cfg := BackupConfig{Enabled: true, IntervalMinutes: 60, Keep: -1}
	if err := ValidateBackupConfig(cfg); err == nil {
		t.Error("expected negative keep to be invalid")
	}
}
