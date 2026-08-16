package automation

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/lomokwa/mc-manager/db"
)

func setupTestDB(t *testing.T) {
	t.Helper()
	prev := db.DB
	if err := db.Init(filepath.Join(t.TempDir(), "test.db")); err != nil {
		t.Fatalf("failed to init test db: %v", err)
	}
	if _, err := db.DB.Exec(
		`INSERT INTO servers (id, name, dir, port) VALUES ('default', 'Default', './minecraft-server', 25565)`,
	); err != nil {
		t.Fatalf("failed to seed the server row: %v", err)
	}
	t.Cleanup(func() {
		db.DB.Close()
		db.DB = prev
	})
}

func sampleRule() Rule {
	return Rule{
		ServerID:      "default",
		Name:          "avisa quando cair",
		Enabled:       true,
		TriggerKind:   "stop",
		TriggerConfig: map[string]any{},
		Actions: []Action{
			{Type: "discord", WebhookID: 1, Message: "{server} caiu às {time}"},
		},
		CooldownSeconds:   60,
		StopOnFailure:     true,
		DeafWindowSeconds: 5,
	}
}

func TestStore_RuleRoundTrip(t *testing.T) {
	setupTestDB(t)

	id, err := CreateRule(sampleRule())
	if err != nil {
		t.Fatalf("CreateRule: %v", err)
	}

	got, err := GetRule(id)
	if err != nil {
		t.Fatalf("GetRule: %v", err)
	}
	if got.Name != "avisa quando cair" || got.TriggerKind != "stop" {
		t.Errorf("unexpected rule: %+v", got)
	}
	if len(got.Actions) != 1 || got.Actions[0].Type != "discord" {
		t.Errorf("actions did not round-trip: %+v", got.Actions)
	}
	if !got.StopOnFailure {
		t.Error("stop_on_failure must default to true and round-trip")
	}
	if got.DeafWindowSeconds != 5 {
		t.Errorf("deaf window did not round-trip, got %d", got.DeafWindowSeconds)
	}
}

// The engine loads only what it must evaluate. A disabled rule costing
// anything at all is how "no rules means no work" stops being true.
func TestStore_ListEnabledSkipsDisabled(t *testing.T) {
	setupTestDB(t)

	on, err := CreateRule(sampleRule())
	if err != nil {
		t.Fatalf("CreateRule: %v", err)
	}
	off, err := CreateRule(sampleRule())
	if err != nil {
		t.Fatalf("CreateRule: %v", err)
	}
	if err := SetRuleEnabled(off, false); err != nil {
		t.Fatalf("SetRuleEnabled: %v", err)
	}

	rules, err := ListEnabledRules()
	if err != nil {
		t.Fatalf("ListEnabledRules: %v", err)
	}
	if len(rules) != 1 || rules[0].ID != on {
		t.Errorf("expected only the enabled rule, got %+v", rules)
	}
}

// Cooldown has to survive an API restart, so it lives on the row rather than
// in memory -- a 6h cooldown that reset on deploy would fire again.
func TestStore_LastFiredAtPersists(t *testing.T) {
	setupTestDB(t)
	id, err := CreateRule(sampleRule())
	if err != nil {
		t.Fatalf("CreateRule: %v", err)
	}
	at := time.Now().UTC().Truncate(time.Second)

	if err := MarkFired(id, at); err != nil {
		t.Fatalf("MarkFired: %v", err)
	}

	got, err := GetRule(id)
	if err != nil {
		t.Fatalf("GetRule: %v", err)
	}
	if got.LastFiredAt == nil {
		t.Fatal("expected last_fired_at to be set")
	}
	if !got.LastFiredAt.Equal(at) {
		t.Errorf("expected last_fired_at %v, got %v", at, *got.LastFiredAt)
	}
}

// The URL is a credential. Making it unreachable through the normal listing
// means a future handler has to ask for it on purpose, rather than leak it by
// forgetting -- the type does the remembering instead of the reviewer.
func TestStore_WebhookURLIsReadableOnlyThroughItsOwnAccessor(t *testing.T) {
	setupTestDB(t)
	id, err := CreateWebhook(Webhook{Name: "alertas", URL: "https://discord.com/api/webhooks/1/SECRET"})
	if err != nil {
		t.Fatalf("CreateWebhook: %v", err)
	}

	list, err := ListWebhooks()
	if err != nil {
		t.Fatalf("ListWebhooks: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 webhook, got %d", len(list))
	}
	if list[0].URL != "" {
		t.Errorf("ListWebhooks must not carry the URL, got %q", list[0].URL)
	}

	url, err := WebhookURL(id)
	if err != nil {
		t.Fatalf("WebhookURL: %v", err)
	}
	if url != "https://discord.com/api/webhooks/1/SECRET" {
		t.Errorf("the accessor must return the real URL, got %q", url)
	}
}

// A 401 has to be visible as a 401. The UI promises "never a false green".
func TestStore_RecordsTheRealDeliveryOutcome(t *testing.T) {
	setupTestDB(t)
	id, err := CreateWebhook(Webhook{Name: "alertas", URL: "https://example.invalid/hook"})
	if err != nil {
		t.Fatalf("CreateWebhook: %v", err)
	}

	if err := RecordWebhookResult(id, 401, "webhook rejected with 401"); err != nil {
		t.Fatalf("RecordWebhookResult: %v", err)
	}

	list, _ := ListWebhooks()
	if list[0].LastStatus == nil || *list[0].LastStatus != 401 {
		t.Errorf("expected the real status to be stored, got %v", list[0].LastStatus)
	}
	if list[0].LastError == nil || *list[0].LastError == "" {
		t.Error("expected the failure reason to be stored")
	}
	if list[0].LastUsedAt == nil {
		t.Error("expected the attempt to be timestamped")
	}
}

func TestStore_DeletingARuleDeletesItsFirings(t *testing.T) {
	setupTestDB(t)
	id, err := CreateRule(sampleRule())
	if err != nil {
		t.Fatalf("CreateRule: %v", err)
	}
	if err := RecordFiring(Firing{RuleID: id, Trigger: "stop", Outcome: `[{"action":"discord","ok":true}]`}); err != nil {
		t.Fatalf("RecordFiring: %v", err)
	}

	if err := DeleteRule(id); err != nil {
		t.Fatalf("DeleteRule: %v", err)
	}

	var n int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM automation_firings WHERE rule_id = ?`, id).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("deleting a rule must remove its firings, %d survived", n)
	}
}

// The schema says ON DELETE CASCADE, but SQLite ignores foreign keys unless
// PRAGMA foreign_keys is ON per connection -- and this app never sets it. This
// pins the fact, so that if someone enables the pragma later and expects the
// declaration to start doing the work, the assumption is written down rather
// than rediscovered. DeleteRule deletes explicitly and is correct either way.
func TestStore_ForeignKeysAreNotEnforcedSoDeletesMustBeExplicit(t *testing.T) {
	setupTestDB(t)

	var on int
	if err := db.DB.QueryRow(`PRAGMA foreign_keys`).Scan(&on); err != nil {
		t.Fatalf("read pragma: %v", err)
	}
	if on != 0 {
		t.Skip("foreign key enforcement is now on -- the CASCADE clauses do the work, and this note is stale")
	}

	id, err := CreateRule(sampleRule())
	if err != nil {
		t.Fatalf("CreateRule: %v", err)
	}
	if err := RecordFiring(Firing{RuleID: id, Trigger: "stop", Outcome: "[]"}); err != nil {
		t.Fatalf("RecordFiring: %v", err)
	}

	// A bare delete of the parent, bypassing DeleteRule: proves the database
	// itself does NOT clean up, which is why DeleteRule must.
	if _, err := db.DB.Exec(`DELETE FROM automation_rules WHERE id = ?`, id); err != nil {
		t.Fatalf("raw delete: %v", err)
	}
	var n int
	db.DB.QueryRow(`SELECT COUNT(*) FROM automation_firings WHERE rule_id = ?`, id).Scan(&n)
	if n != 1 {
		t.Errorf("expected the orphan to survive a raw delete (proving CASCADE is inert), got %d", n)
	}
}

// The deploy that carries this runs against a live database. Prove the three
// tables appear without disturbing what is already there, and that a second
// boot -- which every API restart performs -- changes nothing.
func TestMigration_AddsTablesWithoutTouchingExistingData(t *testing.T) {
	setupTestDB(t)

	if _, err := db.DB.Exec(
		`INSERT INTO users (username, password_hash) VALUES ('lomokwa', 'hash')`,
	); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	var before string
	if err := db.DB.QueryRow(`SELECT password_hash FROM users WHERE username = 'lomokwa'`).Scan(&before); err != nil {
		t.Fatalf("read user: %v", err)
	}

	if err := db.Migrate(); err != nil {
		t.Fatalf("second migrate: %v", err)
	}

	var after string
	if err := db.DB.QueryRow(`SELECT password_hash FROM users WHERE username = 'lomokwa'`).Scan(&after); err != nil {
		t.Fatalf("re-read user: %v", err)
	}
	if before != after {
		t.Error("an existing row changed across a re-migration")
	}
	for _, table := range []string{"automation_rules", "automation_webhooks", "automation_firings"} {
		var n int
		if err := db.DB.QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n); err != nil {
			t.Fatalf("check %s: %v", table, err)
		}
		if n != 1 {
			t.Errorf("expected table %s to exist", table)
		}
	}
}
