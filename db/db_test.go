package db

import (
	"path/filepath"
	"testing"
)

func TestInit_CreatesSchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")

	if err := Init(dbPath); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	defer DB.Close()

	if DB == nil {
		t.Fatal("expected DB to be set after Init")
	}

	tables := []string{"users", "invitations", "backup_config"}
	for _, table := range tables {
		var name string
		err := DB.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name = ?", table).Scan(&name)
		if err != nil {
			t.Errorf("expected table %q to exist after migration: %v", table, err)
		}
	}
}

func TestInit_MigrationIsIdempotent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")

	if err := Init(dbPath); err != nil {
		t.Fatalf("first Init failed: %v", err)
	}
	defer DB.Close()

	// Re-running the migration against the same schema (via migrate) must
	// not fail, since every CREATE TABLE uses IF NOT EXISTS.
	if err := migrate(); err != nil {
		t.Errorf("expected re-running migrate to be a no-op, got error: %v", err)
	}
}

func TestInit_InsertAndQueryRoundTrip(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")

	if err := Init(dbPath); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	defer DB.Close()

	if _, err := DB.Exec("INSERT INTO users (username, password_hash) VALUES (?, ?)", "alice", "hash"); err != nil {
		t.Fatalf("failed to insert user: %v", err)
	}

	var username string
	if err := DB.QueryRow("SELECT username FROM users WHERE username = ?", "alice").Scan(&username); err != nil {
		t.Fatalf("failed to query inserted user: %v", err)
	}
	if username != "alice" {
		t.Errorf("expected 'alice', got %q", username)
	}
}
