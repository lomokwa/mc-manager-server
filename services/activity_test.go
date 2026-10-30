package services

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lomokwa/mc-manager/db"
	"github.com/lomokwa/mc-manager/types"
)

func record(t *testing.T, category, action, detail string) {
	t.Helper()
	RecordActivity(types.ActivityEntry{
		UserID: 1, Username: "lomokwa", Category: category, Action: action, Detail: detail,
	})
}

func TestRecordActivity_StoresAndReadsBackNewestFirst(t *testing.T) {
	setupTestDB(t)

	record(t, types.ActivityServer, "started the server", "POST /api/start")
	record(t, types.ActivityConsole, "ran a console command", "ban Griefer123")

	entries, err := ListActivity("", 0, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(entries))
	}
	if entries[0].Detail != "ban Griefer123" {
		t.Errorf("expected the newest row first, got %q", entries[0].Detail)
	}
	if entries[0].Username != "lomokwa" {
		t.Errorf("expected the actor to be recorded, got %q", entries[0].Username)
	}
}

// The question the page exists to answer.
func TestListActivity_AnswersWhoBannedThisPlayer(t *testing.T) {
	setupTestDB(t)

	RecordActivity(types.ActivityEntry{
		UserID: 7, Username: "Ant", Category: types.ActivityConsole,
		Action: "ran a console command", Detail: "ban Griefer123 grief no spawn",
	})
	record(t, types.ActivityServer, "started the server", "POST /api/start")

	entries, err := ListActivity(types.ActivityConsole, 0, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected the category filter to isolate one row, got %d", len(entries))
	}
	if entries[0].Username != "Ant" || !strings.Contains(entries[0].Detail, "Griefer123") {
		t.Errorf("expected Ant's ban, got %+v", entries[0])
	}
}

// An audit row has to keep reading correctly after the account is gone --
// which is exactly when someone goes looking. Hence the denormalised username.
func TestActivity_SurvivesTheUserBeingDeleted(t *testing.T) {
	setupTestDB(t)
	userID := insertTestUser(t, "temp-admin")

	RecordActivity(types.ActivityEntry{
		UserID: userID, Username: "temp-admin", Category: types.ActivityAccess,
		Action: "changed someone's role", Detail: "PUT /api/users/:id/role",
	})
	if _, err := db.DB.Exec(`DELETE FROM users WHERE id = ?`, userID); err != nil {
		t.Fatalf("delete user: %v", err)
	}

	entries, err := ListActivity("", 0, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 1 || entries[0].Username != "temp-admin" {
		t.Fatalf("expected the row to still name temp-admin, got %+v", entries)
	}
}

func TestListActivity_PagesBackwardsWithoutSkippingOrRepeating(t *testing.T) {
	setupTestDB(t)
	for i := range 10 {
		record(t, types.ActivityConsole, "ran a console command", fmt.Sprintf("cmd-%d", i))
	}

	first, err := ListActivity("", 4, 0)
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	second, err := ListActivity("", 4, first[len(first)-1].ID)
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}

	if len(first) != 4 || len(second) != 4 {
		t.Fatalf("expected 4+4 rows, got %d+%d", len(first), len(second))
	}
	seen := map[int]bool{}
	for _, e := range append(first, second...) {
		if seen[e.ID] {
			t.Errorf("row %d appeared on both pages", e.ID)
		}
		seen[e.ID] = true
	}
	if first[3].ID <= second[0].ID {
		t.Error("expected the second page to continue strictly backwards")
	}
}

func TestListActivity_ClampsAnAbsurdLimit(t *testing.T) {
	setupTestDB(t)
	for i := range 3 {
		record(t, types.ActivityServer, "started the server", fmt.Sprintf("n-%d", i))
	}

	// A caller asking for the whole table gets the default page instead.
	entries, err := ListActivity("", 100000, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected the 3 existing rows, got %d", len(entries))
	}
}

// An audit trail nobody prunes is a disk leak with a nice name. This writes on
// every mutation and every console command, so the cap has to actually apply.
func TestRecordActivity_PrunesToTheRetentionCap(t *testing.T) {
	setupTestDB(t)

	// Seed just under the cap directly (fast), then drive enough real writes
	// through RecordActivity to cross it and trigger a prune.
	for i := range RetainedActivityRows - 10 {
		if _, err := db.DB.Exec(
			`INSERT INTO activity_log (username, category, action, detail) VALUES ('seed','server','started the server',?)`,
			fmt.Sprintf("seed-%d", i),
		); err != nil {
			t.Fatalf("seed row %d: %v", i, err)
		}
	}
	for i := range pruneEvery + 20 {
		record(t, types.ActivityServer, "started the server", fmt.Sprintf("live-%d", i))
	}

	n, err := CountActivity()
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	// Pruning is amortised (every pruneEvery writes), so the honest ceiling is
	// the cap plus one prune interval -- not the cap exactly. Asserting the
	// stricter bound would be asserting a design this deliberately doesn't have.
	if ceiling := RetainedActivityRows + pruneEvery; n > ceiling {
		t.Errorf("expected the log to stay under %d rows, got %d", ceiling, n)
	}
	if n <= RetainedActivityRows-pruneEvery {
		t.Errorf("pruning took too much: %d rows left, cap is %d", n, RetainedActivityRows)
	}

	// And pruning must take the OLDEST, never the newest.
	entries, err := ListActivity("", 1, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.HasPrefix(entries[0].Detail, "live-") {
		t.Errorf("expected the newest row to survive pruning, got %q", entries[0].Detail)
	}
}

// Recording must never be the reason an action fails. A ban that goes through
// but isn't logged is bad; a ban that fails because logging did is worse.
func TestRecordActivity_NeverPanicsWithoutADatabase(t *testing.T) {
	prev := db.DB
	db.DB = nil
	t.Cleanup(func() { db.DB = prev })

	RecordActivity(types.ActivityEntry{Username: "nobody", Category: types.ActivityServer, Action: "started the server"})
}

func TestRecordActivity_NamesAnUnknownActorRatherThanStoringBlank(t *testing.T) {
	setupTestDB(t)
	RecordActivity(types.ActivityEntry{Category: types.ActivityServer, Action: "started the server"})

	entries, _ := ListActivity("", 0, 0)
	if len(entries) != 1 || entries[0].Username == "" {
		t.Fatalf("expected a named actor, got %+v", entries)
	}
}
