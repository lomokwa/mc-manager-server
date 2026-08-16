package handlers

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/lomokwa/mc-manager/automation"
	"github.com/lomokwa/mc-manager/middleware"
	"github.com/lomokwa/mc-manager/services"
	"github.com/lomokwa/mc-manager/types"
)

// firingHistoryLimit bounds what one request can pull back. The table has no
// retention policy yet (deliberately out of scope in the design), so an
// unbounded list would eventually be the slowest endpoint in the app.
const firingHistoryLimit = 100

// idParam parses :id, rejecting anything non-numeric before it reaches a query.
// `what` names the thing so a webhook route does not report "invalid automation
// id". Returns false having already written the response.
func idParam(c *gin.Context, what string) (int, bool) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, types.APIResponse{Error: "invalid " + what + " id"})
		return 0, false
	}
	return id, true
}

// ListAutomationsHandler returns every rule, enabled or not -- the management
// screen has to show the disabled ones too, unlike the engine.
func ListAutomationsHandler(c *gin.Context) {
	rules, err := automation.ListRules()
	if err != nil {
		c.JSON(http.StatusInternalServerError, types.APIResponse{Error: err.Error()})
		return
	}
	c.JSON(http.StatusOK, types.APIResponse{Success: true, Data: rules})
}

func GetAutomationHandler(c *gin.Context) {
	id, ok := idParam(c, "automation")
	if !ok {
		return
	}
	rule, err := automation.GetRule(id)
	if errors.Is(err, sql.ErrNoRows) {
		c.JSON(http.StatusNotFound, types.APIResponse{Error: "automation not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, types.APIResponse{Error: err.Error()})
		return
	}
	c.JSON(http.StatusOK, types.APIResponse{Success: true, Data: rule})
}

// ListAutomationWebhooksHandler returns destinations WITHOUT their URLs.
// automation.ListWebhooks does not select the column and Webhook.URL is
// json:"-", so the credential cannot be included here by accident -- the types
// do the remembering rather than the reviewer.
func ListAutomationWebhooksHandler(c *gin.Context) {
	hooks, err := automation.ListWebhooks()
	if err != nil {
		c.JSON(http.StatusInternalServerError, types.APIResponse{Error: err.Error()})
		return
	}
	c.JSON(http.StatusOK, types.APIResponse{Success: true, Data: hooks})
}

func ListAutomationFiringsHandler(c *gin.Context) {
	id, ok := idParam(c, "automation")
	if !ok {
		return
	}
	firings, err := automation.ListFirings(id, firingHistoryLimit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, types.APIResponse{Error: err.Error()})
		return
	}
	c.JSON(http.StatusOK, types.APIResponse{Success: true, Data: firings})
}

// automationRequest is the write shape. Deliberately NOT automation.Rule:
// binding straight onto the storage type would let a client set ID,
// LastFiredAt or CreatedAt -- and last_fired_at is what the cooldown reads, so
// a client that could post it could bypass every cooldown in the system.
type automationRequest struct {
	ServerID          string              `json:"server_id"`
	Name              string              `json:"name"`
	Enabled           *bool               `json:"enabled"`
	TriggerKind       string              `json:"trigger_kind"`
	TriggerConfig     map[string]any      `json:"trigger_config"`
	Actions           []automation.Action `json:"actions"`
	CooldownSeconds   int                 `json:"cooldown_seconds"`
	StopOnFailure     *bool               `json:"stop_on_failure"`
	DeafWindowSeconds int                 `json:"deaf_window_seconds"`
}

// engineReloader is set at boot so a write makes the engine re-read its rules
// immediately. Nil in tests that do not care.
var engineReloader func() error

// SetEngineReloader wires the running engine to the handlers. Called once from
// main; tests use it to observe that writes trigger a reload.
func SetEngineReloader(fn func() error) { engineReloader = fn }

func reloadEngine() {
	if engineReloader == nil {
		return
	}
	if err := engineReloader(); err != nil {
		slog.Error("automations: failed to reload rules after a write", "err", err)
	}
}

// toRule validates a request and converts it. Every rejection is a message the
// builder can show verbatim, because the person who typed it is the person who
// can fix it.
func (r automationRequest) toRule() (automation.Rule, error) {
	if strings.TrimSpace(r.Name) == "" {
		return automation.Rule{}, errors.New("an automation needs a name")
	}
	if _, err := services.RuntimeForID(r.ServerID); err != nil {
		return automation.Rule{}, fmt.Errorf("unknown server %q", r.ServerID)
	}
	if !automation.IsKnownTrigger(r.TriggerKind) {
		return automation.Rule{}, fmt.Errorf("unknown trigger %q", r.TriggerKind)
	}
	if err := automation.ValidateActions(r.Actions); err != nil {
		return automation.Rule{}, err
	}
	// ValidateActions can only check that a Discord action names SOME webhook:
	// it is a pure function with no database. A rule pointing at a destination
	// that does not exist looks configured and fails at fire time, taking every
	// action after it down with stop_on_failure set.
	for i, a := range r.Actions {
		if a.Type != "discord" {
			continue
		}
		exists, err := automation.WebhookExists(a.WebhookID)
		if err != nil {
			return automation.Rule{}, err
		}
		if !exists {
			return automation.Rule{}, fmt.Errorf("action %d: that Discord destination no longer exists", i+1)
		}
	}
	if r.CooldownSeconds < 0 || r.DeafWindowSeconds < 0 {
		return automation.Rule{}, errors.New("cooldown and deaf window cannot be negative")
	}

	enabled := true
	if r.Enabled != nil {
		enabled = *r.Enabled
	}
	// The safe default, matching the design: [warn on Discord] -> [restart]
	// with a dead webhook must not disconnect anyone unwarned.
	stopOnFailure := true
	if r.StopOnFailure != nil {
		stopOnFailure = *r.StopOnFailure
	}
	cfg := r.TriggerConfig
	if cfg == nil {
		cfg = map[string]any{}
	}

	return automation.Rule{
		ServerID: r.ServerID, Name: strings.TrimSpace(r.Name), Enabled: enabled,
		TriggerKind: r.TriggerKind, TriggerConfig: cfg, Actions: r.Actions,
		CooldownSeconds: r.CooldownSeconds, StopOnFailure: stopOnFailure,
		DeafWindowSeconds: r.DeafWindowSeconds,
	}, nil
}

func CreateAutomationHandler(c *gin.Context) {
	var req automationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, types.APIResponse{Error: "invalid request body"})
		return
	}
	rule, err := req.toRule()
	if err != nil {
		c.JSON(http.StatusBadRequest, types.APIResponse{Error: err.Error()})
		return
	}
	if uid, ok := middleware.UserIDFromContext(c); ok {
		rule.CreatedBy = &uid
	}

	id, err := automation.CreateRule(rule)
	if err != nil {
		c.JSON(http.StatusInternalServerError, types.APIResponse{Error: err.Error()})
		return
	}
	reloadEngine()

	rule.ID = id
	c.JSON(http.StatusCreated, types.APIResponse{Success: true, Data: rule})
}

func UpdateAutomationHandler(c *gin.Context) {
	id, ok := idParam(c, "automation")
	if !ok {
		return
	}
	if _, err := automation.GetRule(id); errors.Is(err, sql.ErrNoRows) {
		c.JSON(http.StatusNotFound, types.APIResponse{Error: "automation not found"})
		return
	}

	var req automationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, types.APIResponse{Error: "invalid request body"})
		return
	}
	rule, err := req.toRule()
	if err != nil {
		c.JSON(http.StatusBadRequest, types.APIResponse{Error: err.Error()})
		return
	}
	rule.ID = id

	if err := automation.UpdateRule(rule); err != nil {
		c.JSON(http.StatusInternalServerError, types.APIResponse{Error: err.Error()})
		return
	}
	reloadEngine()
	c.JSON(http.StatusOK, types.APIResponse{Success: true, Data: rule})
}

func DeleteAutomationHandler(c *gin.Context) {
	id, ok := idParam(c, "automation")
	if !ok {
		return
	}
	if _, err := automation.GetRule(id); errors.Is(err, sql.ErrNoRows) {
		c.JSON(http.StatusNotFound, types.APIResponse{Error: "automation not found"})
		return
	}
	if err := automation.DeleteRule(id); err != nil {
		c.JSON(http.StatusInternalServerError, types.APIResponse{Error: err.Error()})
		return
	}
	reloadEngine()
	c.JSON(http.StatusOK, types.APIResponse{Success: true})
}

// SetAutomationEnabledHandler is its own endpoint rather than a PUT of the
// whole rule. The list screen toggles a switch without holding the rest of the
// rule loaded, and round-tripping a full body just to flip a boolean is how a
// stale client silently reverts someone else's edit.
func SetAutomationEnabledHandler(c *gin.Context) {
	id, ok := idParam(c, "automation")
	if !ok {
		return
	}
	if _, err := automation.GetRule(id); errors.Is(err, sql.ErrNoRows) {
		c.JSON(http.StatusNotFound, types.APIResponse{Error: "automation not found"})
		return
	}

	var body struct {
		Enabled *bool `json:"enabled"`
	}
	// A pointer, so an absent field is rejected instead of silently reading as
	// false -- "disable" is not a safe default for a request that forgot to say.
	if err := c.ShouldBindJSON(&body); err != nil || body.Enabled == nil {
		c.JSON(http.StatusBadRequest, types.APIResponse{Error: "enabled must be true or false"})
		return
	}

	if err := automation.SetRuleEnabled(id, *body.Enabled); err != nil {
		c.JSON(http.StatusInternalServerError, types.APIResponse{Error: err.Error()})
		return
	}
	reloadEngine()
	c.JSON(http.StatusOK, types.APIResponse{Success: true, Data: gin.H{"enabled": *body.Enabled}})
}

// discordWebhookURL is the only shape accepted. Storing an arbitrary URL would
// make this endpoint a request forwarder: anyone with automations.manage could
// point it at an internal address -- a metadata service, another container, the
// panel itself -- and read the status code back off the webhook list. Matching
// Discord's own format is a whitelist, which is the only reliable way to say no
// to that; a blocklist of private ranges loses to DNS.
var discordWebhookURL = regexp.MustCompile(`^https://(?:canary\.|ptb\.)?discord\.com/api/webhooks/\d+/[\w-]+$`)

func CreateAutomationWebhookHandler(c *gin.Context) {
	var body struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, types.APIResponse{Error: "invalid request body"})
		return
	}
	name, url := strings.TrimSpace(body.Name), strings.TrimSpace(body.URL)
	if name == "" {
		c.JSON(http.StatusBadRequest, types.APIResponse{Error: "a webhook needs a name"})
		return
	}
	if !discordWebhookURL.MatchString(url) {
		c.JSON(http.StatusBadRequest, types.APIResponse{
			Error: "that is not a Discord webhook URL. Copy it from Server Settings > Integrations > Webhooks",
		})
		return
	}

	id, err := automation.CreateWebhook(automation.Webhook{Name: name, URL: url})
	if err != nil {
		c.JSON(http.StatusInternalServerError, types.APIResponse{Error: err.Error()})
		return
	}
	// Only the id and name go back. Webhook.URL is json:"-", but echoing the
	// request body would defeat that from the other direction.
	c.JSON(http.StatusCreated, types.APIResponse{
		Success: true,
		Data:    automation.Webhook{ID: id, Name: name},
	})
}

// DeleteAutomationWebhookHandler refuses while a rule still points at the
// destination. Deleting it anyway would not disable those rules -- it would
// make their Discord step fail at fire time, and with stop_on_failure (the
// default) every action after it is skipped. The design's own example is
// [warn on Discord] -> [restart]: the restart would silently stop happening,
// with nothing linking that to a deletion days earlier.
func DeleteAutomationWebhookHandler(c *gin.Context) {
	id, ok := idParam(c, "webhook")
	if !ok {
		return
	}
	exists, err := automation.WebhookExists(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, types.APIResponse{Error: err.Error()})
		return
	}
	if !exists {
		c.JSON(http.StatusNotFound, types.APIResponse{Error: "webhook not found"})
		return
	}

	using, err := automation.RulesUsingWebhook(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, types.APIResponse{Error: err.Error()})
		return
	}
	if len(using) > 0 {
		// Quoted, because a rule name can contain a comma. "still used by:
		// TPS no chao, Servidor caiu, avisa" reads as three rules when it is
		// two, and the operator goes looking for one that does not exist.
		names := make([]string, 0, len(using))
		for _, r := range using {
			names = append(names, strconv.Quote(r.Name))
		}
		// Naming them matters at all: a bare refusal leaves the operator
		// hunting through every rule to find which one is holding it.
		c.JSON(http.StatusConflict, types.APIResponse{
			Error: "still used by " + strings.Join(names, ", ") + ". Change or remove those actions first",
		})
		return
	}

	if err := automation.DeleteWebhook(id); err != nil {
		c.JSON(http.StatusInternalServerError, types.APIResponse{Error: err.Error()})
		return
	}
	c.JSON(http.StatusOK, types.APIResponse{Success: true})
}

// SendAutomationWebhookTestHandler sends one real message so an admin can
// confirm a destination before a rule depends on it. Named Send... rather than
// Test... because a function starting with Test in this package reads as a test
// helper.
func SendAutomationWebhookTestHandler(c *gin.Context) {
	id, ok := idParam(c, "webhook")
	if !ok {
		return
	}
	url, err := automation.WebhookURL(id)
	if err != nil {
		c.JSON(http.StatusNotFound, types.APIResponse{Error: "webhook not found"})
		return
	}

	status, postErr := automation.PostDiscord(url, "Teste do mc-manager: este destino esta funcionando.", "")
	// Recorded either way. An untested destination and a destination that
	// answered 401 must not look the same on the list.
	if err := automation.RecordWebhookResult(id, status, errText(postErr)); err != nil {
		slog.Error("automations: failed to record a webhook test result", "webhook", id, "err", err)
	}
	if postErr != nil {
		// 200 with an error, not a 5xx: our side worked, Discord refused. A 5xx
		// would send the admin looking for a fault in the panel.
		c.JSON(http.StatusOK, types.APIResponse{Error: postErr.Error()})
		return
	}
	c.JSON(http.StatusOK, types.APIResponse{Success: true})
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
