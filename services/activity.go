package services

import (
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"

	"github.com/lomokwa/mc-manager/db"
	"github.com/lomokwa/mc-manager/types"
)

// RetainedActivityRows caps the table. An audit trail nobody prunes is a disk
// leak with a nice name: this is written to on every state-changing request and
// every console command, so on a busy server it grows without bound. Keeping
// the newest N is the trade that fits what the page is for -- answering "who
// did this recently" -- rather than being a compliance archive.
const RetainedActivityRows = 5000

// pruneEvery bounds how often the DELETE runs. Pruning on every insert would
// make the audit trail cost two writes per action for no benefit, so it is
// amortised -- which means the table's real ceiling is
// RetainedActivityRows + pruneEvery, not RetainedActivityRows exactly.
const pruneEvery = 200

// Atomic because this is incremented from every request goroutine and from
// each console connection's read goroutine at once. A plain int here is a data
// race, and `go test -race` in CI is right to call it one.
var activityWrites atomic.Int64

// RecordActivity appends one audit row. Best-effort by design: a failure here
// is logged and swallowed, never returned. The alternative is that a broken
// audit table can fail a server start or a player ban, which trades a
// missing log line for an outage.
//
// detail must already be safe to store. Callers never pass a URL: the API key
// travels as ?key= and the console JWT as ?token=, so a stored URL would turn
// this table into a credential leak the moment anyone read it.
func RecordActivity(entry types.ActivityEntry) {
	if db.DB == nil {
		return
	}
	if strings.TrimSpace(entry.Username) == "" {
		entry.Username = "unknown"
	}

	var userID any
	if entry.UserID > 0 {
		userID = entry.UserID
	}

	_, err := db.DB.Exec(
		`INSERT INTO activity_log (user_id, username, category, action, detail, status, server_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		userID, entry.Username, entry.Category, entry.Action, entry.Detail, entry.Status, entry.ServerID,
	)
	if err != nil {
		slog.Error("failed to record activity", "action", entry.Action, "err", err)
		return
	}

	if activityWrites.Add(1)%pruneEvery == 0 {
		pruneActivity()
	}
}

func pruneActivity() {
	_, err := db.DB.Exec(
		`DELETE FROM activity_log WHERE id <= (
			SELECT id FROM activity_log ORDER BY id DESC LIMIT 1 OFFSET ?
		 )`, RetainedActivityRows,
	)
	if err != nil {
		slog.Error("failed to prune activity log", "err", err)
	}
}

// ListActivity returns the newest rows first, optionally narrowed to one
// category. limit is clamped so a caller can't ask for the whole table.
func ListActivity(category string, limit, before int) ([]types.ActivityEntry, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	query := `SELECT id, created_at, COALESCE(user_id, 0), username, category, action,
	                 COALESCE(detail, ''), status, COALESCE(server_id, '')
	          FROM activity_log WHERE 1=1`
	args := []any{}

	if c := strings.TrimSpace(category); c != "" {
		query += ` AND category = ?`
		args = append(args, c)
	}
	// Keyset pagination on a monotonic id rather than OFFSET: rows are only
	// ever appended and pruned from the tail, so an offset would silently skip
	// or repeat entries as the table changes underneath a reader.
	if before > 0 {
		query += ` AND id < ?`
		args = append(args, before)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := db.DB.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query activity: %w", err)
	}
	defer rows.Close()

	entries := make([]types.ActivityEntry, 0, limit)
	for rows.Next() {
		var e types.ActivityEntry
		if err := rows.Scan(&e.ID, &e.CreatedAt, &e.UserID, &e.Username,
			&e.Category, &e.Action, &e.Detail, &e.Status, &e.ServerID); err != nil {
			return nil, fmt.Errorf("scan activity row: %w", err)
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// CountActivity is used by tests and by the prune check; it is not on a hot path.
func CountActivity() (int, error) {
	var n int
	err := db.DB.QueryRow(`SELECT COUNT(*) FROM activity_log`).Scan(&n)
	if err != nil && err != sql.ErrNoRows {
		return 0, err
	}
	return n, nil
}
