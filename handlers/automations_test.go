package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/lomokwa/mc-manager/automation"
	"github.com/lomokwa/mc-manager/db"
)

// seedDefaultServer inserts the row automation_rules.server_id points at.
// Without it every CreateRule here fails on the foreign key.
func seedDefaultServer(t *testing.T) {
	t.Helper()
	if _, err := db.DB.Exec(
		`INSERT INTO servers (id, name, dir, port) VALUES ('default', 'Default', './minecraft-server', 25565)`,
	); err != nil {
		t.Fatalf("failed to seed the server row: %v", err)
	}
}

func newRule(t *testing.T, name string) int {
	t.Helper()
	id, err := automation.CreateRule(automation.Rule{
		ServerID: "default", Name: name, Enabled: true,
		TriggerKind: "stop", TriggerConfig: map[string]any{},
		Actions: []automation.Action{{Type: "backup"}},
	})
	if err != nil {
		t.Fatalf("CreateRule: %v", err)
	}
	return id
}

// call runs a handler with the given params and body, the way the router would.
func call(h gin.HandlerFunc, method, path, body string, params gin.Params) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = params
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	c.Request = httptest.NewRequest(method, path, reader)
	if body != "" {
		c.Request.Header.Set("Content-Type", "application/json")
	}
	h(c)
	return w
}

func TestListAutomations_ReturnsStoredRules(t *testing.T) {
	setupTestDB(t)
	seedDefaultServer(t)
	newRule(t, "avisa quando cair")

	w := call(ListAutomationsHandler, http.MethodGet, "/api/automations", "", nil)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "avisa quando cair") {
		t.Errorf("expected the rule in the body, got %s", w.Body.String())
	}
}

// The credential must not reach the client. This is the single most important
// assertion in the whole REST layer.
func TestListAutomationWebhooks_NeverCarriesTheURL(t *testing.T) {
	setupTestDB(t)
	const secret = "https://discord.com/api/webhooks/1/SUPERSECRETTOKEN"
	if _, err := automation.CreateWebhook(automation.Webhook{Name: "alertas", URL: secret}); err != nil {
		t.Fatalf("CreateWebhook: %v", err)
	}

	w := call(ListAutomationWebhooksHandler, http.MethodGet, "/api/automation-webhooks", "", nil)

	body := w.Body.String()
	if !strings.Contains(body, "alertas") {
		t.Errorf("expected the webhook name, got %s", body)
	}
	for _, leak := range []string{"SUPERSECRETTOKEN", "discord.com/api/webhooks"} {
		if strings.Contains(body, leak) {
			t.Errorf("the webhook credential leaked into the response: %s", body)
		}
	}
}

func TestGetAutomation_404sForAnUnknownID(t *testing.T) {
	setupTestDB(t)

	w := call(GetAutomationHandler, http.MethodGet, "/api/automations/9999", "",
		gin.Params{{Key: "id", Value: "9999"}})

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for an unknown rule, got %d", w.Code)
	}
}

// The id reaches a query, so anything non-numeric is rejected before it gets
// there rather than relied on to be harmless.
func TestGetAutomation_RejectsANonNumericID(t *testing.T) {
	setupTestDB(t)

	for _, bad := range []string{"../../etc/passwd", "1 OR 1=1", "", "-3", "abc"} {
		w := call(GetAutomationHandler, http.MethodGet, "/api/automations/x", "",
			gin.Params{{Key: "id", Value: bad}})
		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400 for id %q, got %d", bad, w.Code)
		}
	}
}

func TestListFirings_ReturnsNewestFirst(t *testing.T) {
	setupTestDB(t)
	seedDefaultServer(t)
	id := newRule(t, "r")
	for _, trig := range []string{"primeiro", "segundo"} {
		if err := automation.RecordFiring(automation.Firing{RuleID: id, Trigger: trig, Outcome: "[]"}); err != nil {
			t.Fatalf("RecordFiring: %v", err)
		}
	}

	w := call(ListAutomationFiringsHandler, http.MethodGet, "/api/automations/1/firings", "",
		gin.Params{{Key: "id", Value: strconv.Itoa(id)}})

	var resp struct {
		Data []automation.Firing `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Data) != 2 || resp.Data[0].Trigger != "segundo" {
		t.Errorf("expected newest first, got %+v", resp.Data)
	}
}

// ValidateActions has existed since plan 1 and nothing called it. This is the
// wiring that makes it real: until now a rule with {line} in a command action
// could be stored, and the engine WOULD run it -- handing a player the console.
func TestCreateAutomation_RefusesLineInACommand(t *testing.T) {
	setupTestDB(t)
	seedDefaultServer(t)

	body := `{"server_id":"default","name":"perigoso","trigger_kind":"console",
	          "trigger_config":{"pattern":"socorro"},
	          "actions":[{"type":"command","command":"say vi: {line}"}]}`

	w := call(CreateAutomationHandler, http.MethodPost, "/api/automations", body, nil)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "{line}") {
		t.Errorf("the error must name the offending variable, got %s", w.Body.String())
	}
	rules, _ := automation.ListRules()
	if len(rules) != 0 {
		t.Error("the rejected rule must not have been stored")
	}
}

func TestCreateAutomation_StoresAValidRuleAndReloadsTheEngine(t *testing.T) {
	setupTestDB(t)
	seedDefaultServer(t)

	reloaded := 0
	SetEngineReloader(func() error { reloaded++; return nil })
	t.Cleanup(func() { SetEngineReloader(nil) })

	body := `{"server_id":"default","name":"avisa","trigger_kind":"stop",
	          "trigger_config":{},"cooldown_seconds":60,
	          "actions":[{"type":"backup"}]}`

	w := call(CreateAutomationHandler, http.MethodPost, "/api/automations", body, nil)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	rules, _ := automation.ListRules()
	if len(rules) != 1 || rules[0].Name != "avisa" {
		t.Fatalf("expected the rule to be stored, got %+v", rules)
	}
	if !rules[0].StopOnFailure {
		t.Error("stop_on_failure must default to true when the client omits it")
	}
	// A rule that only takes effect after a deploy is a rule the user will
	// assume is broken.
	if reloaded != 1 {
		t.Errorf("expected the engine to be reloaded once, got %d", reloaded)
	}
}

func TestCreateAutomation_RejectsAnUnknownServerOrTrigger(t *testing.T) {
	setupTestDB(t)
	seedDefaultServer(t)

	for _, tc := range []struct{ name, body string }{
		{"unknown server", `{"server_id":"nao-existe","name":"x","trigger_kind":"stop","trigger_config":{},"actions":[{"type":"backup"}]}`},
		{"unknown trigger", `{"server_id":"default","name":"x","trigger_kind":"launch-missiles","trigger_config":{},"actions":[{"type":"backup"}]}`},
		{"no actions", `{"server_id":"default","name":"vazia","trigger_kind":"stop","trigger_config":{},"actions":[]}`},
		{"no name", `{"server_id":"default","name":"  ","trigger_kind":"stop","trigger_config":{},"actions":[{"type":"backup"}]}`},
		{"negative cooldown", `{"server_id":"default","name":"x","trigger_kind":"stop","trigger_config":{},"cooldown_seconds":-5,"actions":[{"type":"backup"}]}`},
	} {
		w := call(CreateAutomationHandler, http.MethodPost, "/api/automations", tc.body, nil)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: expected 400, got %d: %s", tc.name, w.Code, w.Body.String())
		}
	}
}

// A client must not be able to set last_fired_at: it is what the cooldown
// reads, so posting it would bypass every cooldown in the system.
func TestCreateAutomation_IgnoresClientSuppliedFiringState(t *testing.T) {
	setupTestDB(t)
	seedDefaultServer(t)

	body := `{"server_id":"default","name":"x","trigger_kind":"stop","trigger_config":{},
	          "cooldown_seconds":3600,"actions":[{"type":"backup"}],
	          "id":999,"last_fired_at":"2020-01-01T00:00:00Z","created_at":"2020-01-01T00:00:00Z"}`

	w := call(CreateAutomationHandler, http.MethodPost, "/api/automations", body, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	rules, _ := automation.ListRules()
	if len(rules) != 1 {
		t.Fatalf("expected one rule, got %d", len(rules))
	}
	if rules[0].ID == 999 {
		t.Error("a client set the primary key")
	}
	if rules[0].LastFiredAt != nil {
		t.Error("a client set last_fired_at, which would bypass the cooldown")
	}
}

func TestUpdateAutomation_ValidatesTheSameWayAsCreate(t *testing.T) {
	setupTestDB(t)
	seedDefaultServer(t)
	id := newRule(t, "ok")

	body := `{"server_id":"default","name":"agora perigosa","trigger_kind":"console",
	          "trigger_config":{"pattern":"x"},
	          "actions":[{"type":"command","command":"say {line}"}]}`

	w := call(UpdateAutomationHandler, http.MethodPut, "/api/automations/1", body,
		gin.Params{{Key: "id", Value: strconv.Itoa(id)}})

	if w.Code != http.StatusBadRequest {
		t.Fatalf("an update must validate exactly like a create, got %d", w.Code)
	}
	got, _ := automation.GetRule(id)
	if got.Name != "ok" {
		t.Error("the rejected update must not have been applied")
	}
}

func TestUpdateAutomation_404sForAnUnknownRule(t *testing.T) {
	setupTestDB(t)
	seedDefaultServer(t)

	body := `{"server_id":"default","name":"x","trigger_kind":"stop","trigger_config":{},"actions":[{"type":"backup"}]}`
	w := call(UpdateAutomationHandler, http.MethodPut, "/api/automations/9999", body,
		gin.Params{{Key: "id", Value: "9999"}})

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

// Editing a rule must not clear its firing history. If it did, "edit the name"
// would be a free cooldown reset -- the cheapest possible way to bypass the one
// control that stops a rule from running in a loop.
func TestUpdateAutomation_PreservesTheCooldownState(t *testing.T) {
	setupTestDB(t)
	seedDefaultServer(t)
	id := newRule(t, "antes")
	if err := automation.MarkFired(id, time.Now()); err != nil {
		t.Fatalf("MarkFired: %v", err)
	}

	body := `{"server_id":"default","name":"depois","trigger_kind":"stop",
	          "trigger_config":{},"cooldown_seconds":3600,"actions":[{"type":"backup"}]}`
	w := call(UpdateAutomationHandler, http.MethodPut, "/api/automations/1", body,
		gin.Params{{Key: "id", Value: strconv.Itoa(id)}})

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	got, err := automation.GetRule(id)
	if err != nil {
		t.Fatalf("GetRule: %v", err)
	}
	if got.Name != "depois" {
		t.Errorf("the edit was not applied, name is %q", got.Name)
	}
	if got.LastFiredAt == nil {
		t.Error("editing a rule cleared last_fired_at, which resets its cooldown")
	}
}

func TestDeleteAutomation_RemovesTheRuleAndItsFirings(t *testing.T) {
	setupTestDB(t)
	seedDefaultServer(t)
	id := newRule(t, "descartavel")
	if err := automation.RecordFiring(automation.Firing{RuleID: id, Trigger: "t", Outcome: "[]"}); err != nil {
		t.Fatalf("RecordFiring: %v", err)
	}

	reloaded := 0
	SetEngineReloader(func() error { reloaded++; return nil })
	t.Cleanup(func() { SetEngineReloader(nil) })

	w := call(DeleteAutomationHandler, http.MethodDelete, "/api/automations/1", "",
		gin.Params{{Key: "id", Value: strconv.Itoa(id)}})

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	rules, _ := automation.ListRules()
	if len(rules) != 0 {
		t.Errorf("the rule survived the delete: %+v", rules)
	}
	// The schema says ON DELETE CASCADE but SQLite has foreign keys OFF, so
	// without the explicit transaction in DeleteRule the firings would outlive
	// the rule and leak into the next rule that reuses the id.
	firings, _ := automation.ListFirings(id, 10)
	if len(firings) != 0 {
		t.Errorf("orphan firings survived the delete: %+v", firings)
	}
	if reloaded != 1 {
		t.Errorf("a deleted rule must stop firing immediately, reloads = %d", reloaded)
	}
}

func TestDeleteAutomation_404sForAnUnknownRule(t *testing.T) {
	setupTestDB(t)

	w := call(DeleteAutomationHandler, http.MethodDelete, "/api/automations/9999", "",
		gin.Params{{Key: "id", Value: "9999"}})

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

// Enable/disable is its own endpoint rather than a PUT of the whole rule: the
// list screen toggles a switch without holding the rest of the rule, and
// round-tripping the full body just to flip a boolean is how a stale client
// silently reverts someone else's edit.
func TestSetAutomationEnabled_TogglesWithoutTouchingTheRest(t *testing.T) {
	setupTestDB(t)
	seedDefaultServer(t)
	id := newRule(t, "liga desliga")

	reloaded := 0
	SetEngineReloader(func() error { reloaded++; return nil })
	t.Cleanup(func() { SetEngineReloader(nil) })

	w := call(SetAutomationEnabledHandler, http.MethodPost, "/api/automations/1/enabled",
		`{"enabled":false}`, gin.Params{{Key: "id", Value: strconv.Itoa(id)}})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	got, _ := automation.GetRule(id)
	if got.Enabled {
		t.Error("the rule is still enabled")
	}
	if got.Name != "liga desliga" || len(got.Actions) != 1 {
		t.Errorf("toggling enabled changed the rest of the rule: %+v", got)
	}
	if reloaded != 1 {
		t.Errorf("expected one reload, got %d", reloaded)
	}

	w = call(SetAutomationEnabledHandler, http.MethodPost, "/api/automations/1/enabled",
		`{"enabled":true}`, gin.Params{{Key: "id", Value: strconv.Itoa(id)}})
	if w.Code != http.StatusOK {
		t.Fatalf("re-enable: expected 200, got %d", w.Code)
	}
	if got, _ := automation.GetRule(id); !got.Enabled {
		t.Error("the rule did not come back on")
	}
}

func TestSetAutomationEnabled_404sForAnUnknownRule(t *testing.T) {
	setupTestDB(t)

	w := call(SetAutomationEnabledHandler, http.MethodPost, "/api/automations/9999/enabled",
		`{"enabled":false}`, gin.Params{{Key: "id", Value: "9999"}})

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func newWebhook(t *testing.T, name string) int {
	t.Helper()
	id, err := automation.CreateWebhook(automation.Webhook{
		Name: name, URL: "https://discord.com/api/webhooks/123456789/abcdefTOKEN",
	})
	if err != nil {
		t.Fatalf("CreateWebhook: %v", err)
	}
	return id
}

// Storing an arbitrary URL would turn this endpoint into a request forwarder:
// anyone with automations.manage could point a rule at an internal address and
// read the status code back off the webhook list.
func TestCreateWebhook_RejectsAnythingButADiscordWebhookURL(t *testing.T) {
	setupTestDB(t)

	for _, bad := range []string{
		"http://evil.example/collect",
		"https://discord.com/api/webhooks",
		"https://discord.com.evil.example/api/webhooks/1/tok",
		"http://169.254.169.254/latest/meta-data/",
		"http://localhost:8080/api/users",
		"not a url at all",
		"",
		"file:///etc/passwd",
	} {
		body := fmt.Sprintf(`{"name":"x","url":%q}`, bad)
		w := call(CreateAutomationWebhookHandler, http.MethodPost, "/api/automation-webhooks", body, nil)
		if w.Code != http.StatusBadRequest {
			t.Errorf("expected %q to be rejected, got %d", bad, w.Code)
		}
	}
}

// The create response must not echo the URL back either -- devtools and a
// proxy log are both places it should never appear.
func TestCreateWebhook_ResponseDoesNotEchoTheURL(t *testing.T) {
	setupTestDB(t)
	const secret = "https://discord.com/api/webhooks/123456789/SUPERSECRETTOKEN"

	w := call(CreateAutomationWebhookHandler, http.MethodPost, "/api/automation-webhooks",
		fmt.Sprintf(`{"name":"alertas","url":%q}`, secret), nil)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "SUPERSECRETTOKEN") {
		t.Errorf("the create response echoed the credential: %s", w.Body.String())
	}
}

// A rule saved against a destination that does not exist looks configured and
// fails at fire time -- and with stop_on_failure (the default) it takes every
// action after it down too.
func TestCreateAutomation_RejectsAnUnknownWebhook(t *testing.T) {
	setupTestDB(t)
	seedDefaultServer(t)

	body := `{"server_id":"default","name":"avisa","trigger_kind":"stop","trigger_config":{},
	          "actions":[{"type":"discord","webhook_id":4242,"message":"caiu"}]}`

	w := call(CreateAutomationHandler, http.MethodPost, "/api/automations", body, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a webhook that does not exist, got %d: %s", w.Code, w.Body.String())
	}

	// And the same rule against a real destination must save.
	hookID := newWebhook(t, "alertas")
	ok := fmt.Sprintf(`{"server_id":"default","name":"avisa","trigger_kind":"stop","trigger_config":{},
	          "actions":[{"type":"discord","webhook_id":%d,"message":"caiu"}]}`, hookID)
	if w := call(CreateAutomationHandler, http.MethodPost, "/api/automations", ok, nil); w.Code != http.StatusCreated {
		t.Fatalf("expected 201 for a real webhook, got %d: %s", w.Code, w.Body.String())
	}
}

// Deleting a destination a rule depends on must refuse, not silently break the
// rule. [warn on Discord] -> [restart] with a dead webhook means the restart
// never happens, and nothing connects that to the deletion days earlier.
func TestDeleteWebhook_RefusesWhileARuleStillUsesIt(t *testing.T) {
	setupTestDB(t)
	seedDefaultServer(t)
	hookID := newWebhook(t, "alertas")

	if _, err := automation.CreateRule(automation.Rule{
		ServerID: "default", Name: "avisa e reinicia", Enabled: true,
		TriggerKind: "tps", TriggerConfig: map[string]any{},
		Actions: []automation.Action{
			{Type: "discord", WebhookID: hookID, Message: "TPS baixo"},
			{Type: "restart", Warnings: []int{60, 10}},
		},
	}); err != nil {
		t.Fatalf("CreateRule: %v", err)
	}

	w := call(DeleteAutomationWebhookHandler, http.MethodDelete, "/api/automation-webhooks/1", "",
		gin.Params{{Key: "id", Value: strconv.Itoa(hookID)}})

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
	// The operator has to be told WHICH rule, or the refusal is just a wall.
	if !strings.Contains(w.Body.String(), "avisa e reinicia") {
		t.Errorf("the refusal must name the rules still using it, got %s", w.Body.String())
	}
	// Quoted, because a rule name can contain a comma -- an unquoted list of
	// "TPS baixo, Servidor caiu, avisa" reads as three rules when it is two.
	if !strings.Contains(w.Body.String(), `\"avisa e reinicia\"`) {
		t.Errorf("the names must be quoted so a comma in one cannot split it, got %s", w.Body.String())
	}
	if hooks, _ := automation.ListWebhooks(); len(hooks) != 1 {
		t.Error("the webhook was deleted despite the refusal")
	}
}

func TestDeleteWebhook_RemovesAnUnusedOne(t *testing.T) {
	setupTestDB(t)
	hookID := newWebhook(t, "sem uso")

	w := call(DeleteAutomationWebhookHandler, http.MethodDelete, "/api/automation-webhooks/1", "",
		gin.Params{{Key: "id", Value: strconv.Itoa(hookID)}})

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if hooks, _ := automation.ListWebhooks(); len(hooks) != 0 {
		t.Errorf("the webhook survived: %+v", hooks)
	}
}

func TestDeleteWebhook_404sForAnUnknownWebhook(t *testing.T) {
	setupTestDB(t)

	w := call(DeleteAutomationWebhookHandler, http.MethodDelete, "/api/automation-webhooks/9999", "",
		gin.Params{{Key: "id", Value: "9999"}})

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

// The delivery test has to send a real request, or it proves nothing an admin
// cares about. httptest stands in for Discord so the assertion is about our
// side: the recorded result matches what actually happened.
func TestSendWebhookTest_RecordsARefusalAsARefusal(t *testing.T) {
	setupTestDB(t)

	discord := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message": "Invalid Webhook Token"}`, http.StatusUnauthorized)
	}))
	t.Cleanup(discord.Close)

	// Inserted through the store, not the handler: the handler only accepts
	// real discord.com URLs, which is the point of the other test.
	id, err := automation.CreateWebhook(automation.Webhook{Name: "quebrado", URL: discord.URL})
	if err != nil {
		t.Fatalf("CreateWebhook: %v", err)
	}

	w := call(SendAutomationWebhookTestHandler, http.MethodPost, "/api/automation-webhooks/1/test", "",
		gin.Params{{Key: "id", Value: strconv.Itoa(id)}})

	// 200 with success:false. The API worked; Discord refused. A 5xx here would
	// say our server broke, which is a different thing to go debug.
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "401") {
		t.Errorf("the response must say what Discord answered, got %s", w.Body.String())
	}

	hooks, _ := automation.ListWebhooks()
	if len(hooks) != 1 {
		t.Fatalf("expected one webhook, got %d", len(hooks))
	}
	// This is what stops the list showing an untested destination as green.
	if hooks[0].LastStatus == nil || *hooks[0].LastStatus != http.StatusUnauthorized {
		t.Errorf("expected the 401 to be recorded, got %+v", hooks[0].LastStatus)
	}
	if hooks[0].LastError == nil || !strings.Contains(*hooks[0].LastError, "Invalid Webhook Token") {
		t.Errorf("expected Discord's own explanation to be recorded, got %+v", hooks[0].LastError)
	}
	if strings.Contains(w.Body.String(), discord.URL) {
		t.Error("the destination URL leaked into the response")
	}
}

func TestSendWebhookTest_RecordsASuccess(t *testing.T) {
	setupTestDB(t)

	var got struct{ contentType string }
	discord := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.contentType = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(discord.Close)

	id, err := automation.CreateWebhook(automation.Webhook{Name: "bom", URL: discord.URL})
	if err != nil {
		t.Fatalf("CreateWebhook: %v", err)
	}

	w := call(SendAutomationWebhookTestHandler, http.MethodPost, "/api/automation-webhooks/1/test", "",
		gin.Params{{Key: "id", Value: strconv.Itoa(id)}})

	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"success":true`) {
		t.Fatalf("expected a success, got %d: %s", w.Code, w.Body.String())
	}
	if got.contentType != "application/json" {
		t.Errorf("expected a JSON post, got %q", got.contentType)
	}
	hooks, _ := automation.ListWebhooks()
	if hooks[0].LastStatus == nil || *hooks[0].LastStatus != http.StatusNoContent {
		t.Errorf("expected 204 recorded, got %+v", hooks[0].LastStatus)
	}
	if hooks[0].LastError != nil && *hooks[0].LastError != "" {
		t.Errorf("a success recorded an error: %v", *hooks[0].LastError)
	}
}

func TestSendWebhookTest_404sForAnUnknownWebhook(t *testing.T) {
	setupTestDB(t)

	w := call(SendAutomationWebhookTestHandler, http.MethodPost, "/api/automation-webhooks/9999/test", "",
		gin.Params{{Key: "id", Value: "9999"}})

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

// The wire shape, key by key. The list test above only checks that the rule's
// NAME appears in the body -- which is true whether the field is called "name"
// or "Name", so it cannot tell a working API from one no client can read.
//
// This is what caught it: every other type in this feature emits snake_case,
// Rule emitted Go field names, and a client could create a rule and then never
// read one back.
func TestAutomationJSON_UsesTheSameSnakeCaseAsEverythingElse(t *testing.T) {
	setupTestDB(t)
	seedDefaultServer(t)
	newRule(t, "avisa")

	w := call(ListAutomationsHandler, http.MethodGet, "/api/automations", "", nil)

	var resp struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("expected one rule, got %d", len(resp.Data))
	}

	for _, key := range []string{
		"id", "server_id", "name", "enabled", "trigger_kind", "trigger_config",
		"actions", "cooldown_seconds", "stop_on_failure", "deaf_window_seconds",
		"last_fired_at", "created_at",
	} {
		if _, ok := resp.Data[0][key]; !ok {
			t.Errorf("missing %q on the wire; got keys %v", key, keysOf(resp.Data[0]))
		}
	}
	// And the Go names must NOT be there, or both spellings would ship and the
	// next reader would have to guess which one is the contract.
	for _, wrong := range []string{"ID", "ServerID", "TriggerKind", "CooldownSeconds"} {
		if _, ok := resp.Data[0][wrong]; ok {
			t.Errorf("%q leaked the Go field name onto the wire", wrong)
		}
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
