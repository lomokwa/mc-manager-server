// Package automation stores and evaluates "when X -> do Y" rules: a trigger
// drawn from the event bus, and an ordered list of actions to run when it
// matches. See the design doc kept outside this repo (AUTOMATIONS-design.md in
// the workspace root).
//
// The package is deliberately split by responsibility rather than by layer:
// matcher.go decides, actions.go executes, engine.go wires the two to the bus,
// and this file is the only one that touches SQL.
package automation

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/lomokwa/mc-manager/db"
)

// Action is one step of a rule, in order. Type is the discriminator; the
// remaining fields are the union of every type's config, which keeps the
// stored JSON flat and the Go side free of type assertions on a map.
type Action struct {
	Type      string `json:"type"` // discord | command | spark | backup | restart
	WebhookID int    `json:"webhook_id,omitempty"`
	Message   string `json:"message,omitempty"`
	Mention   string `json:"mention,omitempty"`
	Command   string `json:"command,omitempty"`
	Warnings  []int  `json:"warnings,omitempty"` // seconds before a restart
}

// Rule is one automation. ServerID binds it to a single server: TPS and
// console lines are per-server by nature, since each server has its own hub.
type Rule struct {
	ID                int
	ServerID          string
	Name              string
	Enabled           bool
	TriggerKind       string
	TriggerConfig     map[string]any
	Actions           []Action
	CooldownSeconds   int
	StopOnFailure     bool
	DeafWindowSeconds int
	LastFiredAt       *time.Time
	CreatedBy         *int
	CreatedAt         time.Time
}

// Webhook is a Discord destination. URL carries a value only when loaded
// through WebhookURL: ListWebhooks does not select the column and the field is
// json:"-", so a handler cannot leak the credential by forgetting about it.
// The type does the remembering instead of the reviewer.
type Webhook struct {
	ID         int        `json:"id"`
	Name       string     `json:"name"`
	URL        string     `json:"-"`
	LastStatus *int       `json:"last_status"`
	LastError  *string    `json:"last_error"`
	LastUsedAt *time.Time `json:"last_used_at"`
	CreatedAt  time.Time  `json:"created_at"`
}

// Firing is one execution of a rule. Outcome is a JSON array with one entry
// per action, so the UI can say which step failed instead of "it errored".
type Firing struct {
	ID      int       `json:"id"`
	RuleID  int       `json:"rule_id"`
	FiredAt time.Time `json:"fired_at"`
	Trigger string    `json:"trigger"`
	Outcome string    `json:"outcome"`
}

const ruleColumns = `id, server_id, name, enabled, trigger_kind, trigger_config,
	actions, cooldown_seconds, stop_on_failure, deaf_window_seconds,
	last_fired_at, created_by, created_at`

type scanner interface{ Scan(...any) error }

func scanRule(s scanner) (Rule, error) {
	var r Rule
	var cfg, acts string
	if err := s.Scan(&r.ID, &r.ServerID, &r.Name, &r.Enabled, &r.TriggerKind, &cfg,
		&acts, &r.CooldownSeconds, &r.StopOnFailure, &r.DeafWindowSeconds,
		&r.LastFiredAt, &r.CreatedBy, &r.CreatedAt); err != nil {
		return Rule{}, err
	}
	if err := json.Unmarshal([]byte(cfg), &r.TriggerConfig); err != nil {
		return Rule{}, fmt.Errorf("decode trigger config for rule %d: %w", r.ID, err)
	}
	if err := json.Unmarshal([]byte(acts), &r.Actions); err != nil {
		return Rule{}, fmt.Errorf("decode actions for rule %d: %w", r.ID, err)
	}
	return r, nil
}

func CreateRule(r Rule) (int, error) {
	cfg, err := json.Marshal(r.TriggerConfig)
	if err != nil {
		return 0, fmt.Errorf("encode trigger config: %w", err)
	}
	acts, err := json.Marshal(r.Actions)
	if err != nil {
		return 0, fmt.Errorf("encode actions: %w", err)
	}
	res, err := db.DB.Exec(`
		INSERT INTO automation_rules
		  (server_id, name, enabled, trigger_kind, trigger_config, actions,
		   cooldown_seconds, stop_on_failure, deaf_window_seconds, created_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ServerID, r.Name, r.Enabled, r.TriggerKind, string(cfg), string(acts),
		r.CooldownSeconds, r.StopOnFailure, r.DeafWindowSeconds, r.CreatedBy,
	)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	return int(id), err
}

func GetRule(id int) (Rule, error) {
	return scanRule(db.DB.QueryRow(`SELECT `+ruleColumns+` FROM automation_rules WHERE id = ?`, id))
}

func queryRules(where string) ([]Rule, error) {
	rows, err := db.DB.Query(`SELECT ` + ruleColumns + ` FROM automation_rules ` + where + ` ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Rule
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListEnabledRules is what the engine loads. A disabled rule must cost
// nothing, which is why it is filtered in SQL rather than skipped later.
func ListEnabledRules() ([]Rule, error) { return queryRules(`WHERE enabled = 1`) }

// ListRules is for the management UI, which has to show disabled rules too.
func ListRules() ([]Rule, error) { return queryRules(``) }

func UpdateRule(r Rule) error {
	cfg, err := json.Marshal(r.TriggerConfig)
	if err != nil {
		return fmt.Errorf("encode trigger config: %w", err)
	}
	acts, err := json.Marshal(r.Actions)
	if err != nil {
		return fmt.Errorf("encode actions: %w", err)
	}
	_, err = db.DB.Exec(`
		UPDATE automation_rules SET server_id = ?, name = ?, enabled = ?,
		  trigger_kind = ?, trigger_config = ?, actions = ?, cooldown_seconds = ?,
		  stop_on_failure = ?, deaf_window_seconds = ?
		WHERE id = ?`,
		r.ServerID, r.Name, r.Enabled, r.TriggerKind, string(cfg), string(acts),
		r.CooldownSeconds, r.StopOnFailure, r.DeafWindowSeconds, r.ID,
	)
	return err
}

// DeleteRule removes the rule AND its firings, explicitly, in one transaction.
//
// The schema declares ON DELETE CASCADE, but that clause does nothing here:
// SQLite only enforces foreign keys when `PRAGMA foreign_keys = ON` is set per
// connection, and this application never sets it (verified -- the pragma reads
// 0). Every CASCADE in db/migrations.sql is therefore decorative today,
// including the pre-existing ones on user_roles, minecraft_links and
// mc_link_codes, which means deleting a user already leaves orphans.
//
// Turning the pragma on globally is the real fix, but it is not this feature's
// to make: if production data already contains an orphan, enabling enforcement
// starts rejecting writes that used to succeed. That needs its own audit.
// Deleting explicitly is correct either way, and stays correct if the pragma
// is enabled later.
func DeleteRule(id int) error {
	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM automation_firings WHERE rule_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM automation_rules WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func SetRuleEnabled(id int, enabled bool) error {
	_, err := db.DB.Exec(`UPDATE automation_rules SET enabled = ? WHERE id = ?`, enabled, id)
	return err
}

// MarkFired stamps the row, not a memory map: a 6h cooldown has to survive a
// deploy, and resetting it on restart would fire the rule again.
func MarkFired(ruleID int, at time.Time) error {
	_, err := db.DB.Exec(`UPDATE automation_rules SET last_fired_at = ? WHERE id = ?`, at.UTC(), ruleID)
	return err
}

func CreateWebhook(w Webhook) (int, error) {
	res, err := db.DB.Exec(`INSERT INTO automation_webhooks (name, url) VALUES (?, ?)`, w.Name, w.URL)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	return int(id), err
}

// ListWebhooks never selects url. The column is reachable only through
// WebhookURL, so a new handler has to ask for the secret on purpose.
func ListWebhooks() ([]Webhook, error) {
	rows, err := db.DB.Query(
		`SELECT id, name, last_status, last_error, last_used_at, created_at
		 FROM automation_webhooks ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Webhook
	for rows.Next() {
		var w Webhook
		if err := rows.Scan(&w.ID, &w.Name, &w.LastStatus, &w.LastError, &w.LastUsedAt, &w.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// WebhookURL is the only path to the credential. Everything that calls it is
// worth reading twice.
func WebhookURL(id int) (string, error) {
	var url string
	err := db.DB.QueryRow(`SELECT url FROM automation_webhooks WHERE id = ?`, id).Scan(&url)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("webhook %d not found", id)
	}
	return url, err
}

func DeleteWebhook(id int) error {
	_, err := db.DB.Exec(`DELETE FROM automation_webhooks WHERE id = ?`, id)
	return err
}

// RecordWebhookResult stores the real outcome of the last delivery, which is
// what lets the UI show a 401 as an error instead of a false green.
func RecordWebhookResult(id int, status int, errMsg string) error {
	var e *string
	if errMsg != "" {
		e = &errMsg
	}
	_, err := db.DB.Exec(
		`UPDATE automation_webhooks SET last_status = ?, last_error = ?, last_used_at = ? WHERE id = ?`,
		status, e, time.Now().UTC(), id)
	return err
}

func RecordFiring(f Firing) error {
	_, err := db.DB.Exec(
		`INSERT INTO automation_firings (rule_id, trigger, outcome) VALUES (?, ?, ?)`,
		f.RuleID, f.Trigger, f.Outcome)
	return err
}

func ListFirings(ruleID, limit int) ([]Firing, error) {
	rows, err := db.DB.Query(
		`SELECT id, rule_id, fired_at, trigger, outcome FROM automation_firings
		 WHERE rule_id = ? ORDER BY id DESC LIMIT ?`, ruleID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Firing
	for rows.Next() {
		var f Firing
		if err := rows.Scan(&f.ID, &f.RuleID, &f.FiredAt, &f.Trigger, &f.Outcome); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
