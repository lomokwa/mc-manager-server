package db

import (
	"database/sql"
	_ "embed"

	_ "github.com/mattn/go-sqlite3"
)

var DB *sql.DB

//go:embed migrations.sql
var migrations string

func Init(path string) error {
	var err error
	DB, err = sql.Open("sqlite3", path)
	if err != nil {
		return err
	}
	return Migrate()
}

// Migrate applies the embedded schema. Exported so a test can re-run exactly
// what every API restart runs, which is what makes a migration rehearsal
// against realistic data possible at all.
func Migrate() error {
	_, err := DB.Exec(migrations)
	return err
}
