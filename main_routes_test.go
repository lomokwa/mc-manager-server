package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/lomokwa/mc-manager/automation"
	"github.com/lomokwa/mc-manager/db"
	"github.com/lomokwa/mc-manager/services"
	"github.com/lomokwa/mc-manager/types"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// setupTestDB points db.DB at a fresh temp-file sqlite database. Test
// helpers in _test.go files aren't shared across packages, so this mirrors
// the identical helper services/handlers/middleware each keep their own
// copy of.
func setupTestDB(t *testing.T) {
	t.Helper()
	prev := db.DB
	dbPath := filepath.Join(t.TempDir(), "test.db")
	if err := db.Init(dbPath); err != nil {
		t.Fatalf("failed to init test db: %v", err)
	}
	t.Cleanup(func() {
		db.DB.Close()
		db.DB = prev
	})
}

// setupServerDir creates services.ServerDir (a fixed relative path) fresh
// for a test and removes it during cleanup -- see handlers/testutil_test.go
// for the identical pattern used elsewhere.
func setupServerDir(t *testing.T) {
	t.Helper()
	if err := os.RemoveAll(services.ServerDir); err != nil {
		t.Fatalf("failed to clear server dir: %v", err)
	}
	if err := os.MkdirAll(services.ServerDir, 0755); err != nil {
		t.Fatalf("failed to create server dir: %v", err)
	}
	t.Cleanup(func() {
		os.RemoveAll(services.ServerDir)
	})
}

// bootTestRegistry runs the same registry boot sequence main() runs before
// building the router (EnsureDefaultServer + LoadRuntimes), so router-level
// tests see the same "default" server main.go's routes expect to resolve.
func bootTestRegistry(t *testing.T) {
	t.Helper()
	if err := services.EnsureDefaultServer(); err != nil {
		t.Fatalf("failed to seed default server: %v", err)
	}
	if err := services.LoadRuntimes(); err != nil {
		t.Fatalf("failed to load server runtimes: %v", err)
	}
}

// newTestUserToken inserts a user (assigning roleName if non-empty, leaving
// them role-less -- deny by default -- if empty) and returns a signed JWT
// for them. Requires JWT_SECRET="test-secret" to already be set (e.g. via
// t.Setenv) so the token this mints validates against ValidateJWT.
func newTestUserToken(t *testing.T, username, roleName string) string {
	t.Helper()
	if err := services.EnsureBuiltinRoles(); err != nil {
		t.Fatalf("failed to seed built-in roles: %v", err)
	}
	res, err := db.DB.Exec(`INSERT INTO users (username, password_hash) VALUES (?, ?)`, username, "hash")
	if err != nil {
		t.Fatalf("failed to insert test user %q: %v", username, err)
	}
	id64, _ := res.LastInsertId()
	if roleName != "" {
		if err := services.SetUserRole(int(id64), roleName); err != nil {
			t.Fatalf("failed to assign role %q: %v", roleName, err)
		}
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"user_id": float64(id64), "username": username,
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	signed, err := token.SignedString([]byte("test-secret"))
	if err != nil {
		t.Fatalf("failed to sign test token: %v", err)
	}
	return signed
}

func doRequest(r *gin.Engine, method, path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// TestFlatRoutes_StillRouteAndMatchDefaultServer is the regression test
// PLAN-multi-server.md D3 requires: the flat routes are PERMANENT aliases
// for the "default" server, not a legacy path scheduled for removal. This
// exercises the real *gin.Engine newRouter() builds (not a handler called
// directly, the way most other handler tests in this repo work), proving
// both that the flat routes still route at all and that they behave
// identically to their /api/servers/default/... equivalent. This is the
// single most important property of this PR: the Discord bot
// (selton-mello-bot, a separately deployed service) only ever calls the
// flat routes and has no way to learn about /api/servers/:sid/... on its
// own schedule.
func TestFlatRoutes_StillRouteAndMatchDefaultServer(t *testing.T) {
	setupTestDB(t)
	setupServerDir(t)
	bootTestRegistry(t)
	t.Setenv("JWT_SECRET", "test-secret")
	token := newTestUserToken(t, "owner-user", "Owner")

	r := newRouter()

	flatW := doRequest(r, http.MethodGet, "/api/status", token)
	nsW := doRequest(r, http.MethodGet, "/api/servers/"+services.DefaultServerID+"/status", token)

	if flatW.Code != http.StatusOK {
		t.Fatalf("flat /api/status: expected 200, got %d, body=%s", flatW.Code, flatW.Body.String())
	}
	if nsW.Code != http.StatusOK {
		t.Fatalf("namespaced /api/servers/default/status: expected 200, got %d, body=%s", nsW.Code, nsW.Body.String())
	}
	if flatW.Body.String() != nsW.Body.String() {
		t.Errorf("expected identical bodies -- flat=%s namespaced=%s", flatW.Body.String(), nsW.Body.String())
	}

	var resp types.APIResponse
	if err := json.Unmarshal(flatW.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode flat response: %v", err)
	}
	if !resp.Success {
		t.Error("expected success=true")
	}
}

// TestFlatRoutes_PlayersStillRoutesAndRequiresAuth covers the one route
// PLAN-multi-server.md explicitly flags as the hard external dependency:
// selton-mello-bot calls this exact flat endpoint in production. An
// unauthenticated request must still 401 (ValidateJWT still gates it
// through the api group, unchanged), and an authenticated one must still
// reach ListPlayersHandler -- proven by getting the SAME 500 the handler
// already returns with no usercache.json on disk (see
// handlers.TestListPlayersHandler_MissingUserCache), not a 404 that would
// mean the route stopped resolving.
func TestFlatRoutes_PlayersStillRoutesAndRequiresAuth(t *testing.T) {
	setupTestDB(t)
	setupServerDir(t)
	bootTestRegistry(t)

	r := newRouter()

	unauth := doRequest(r, http.MethodGet, "/api/players", "")
	if unauth.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for an unauthenticated request, got %d", unauth.Code)
	}

	t.Setenv("JWT_SECRET", "test-secret")
	token := newTestUserToken(t, "players-viewer", "Operator") // Operator has players.view
	authed := doRequest(r, http.MethodGet, "/api/players", token)
	if authed.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 (matches ListPlayersHandler's own missing-usercache behavior), got %d, body=%s", authed.Code, authed.Body.String())
	}
}

// TestNamespacedRoute_UnknownServerID_404s proves an action route under
// /api/servers/:sid/... 404s for an id that isn't in the registry, through
// the real router (ResolveServer mounted on the group in main.go), not just
// the middleware in isolation (see middleware/server_test.go for that).
func TestNamespacedRoute_UnknownServerID_404s(t *testing.T) {
	setupTestDB(t)
	setupServerDir(t)
	bootTestRegistry(t)
	t.Setenv("JWT_SECRET", "test-secret")
	token := newTestUserToken(t, "owner-user", "Owner")

	r := newRouter()

	w := doRequest(r, http.MethodGet, "/api/servers/does-not-exist/status", token)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d, body=%s", w.Code, w.Body.String())
	}
}

// GET /api/servers is gated on PermServersView, which every built-in role
// holds. That combination is the whole point, so both halves are asserted:
// nobody who has a role loses the page, and an account with no role at all
// doesn't get to read it just because this one route was never gated.
//
// The "every role keeps it" half is the one that matters most -- gating a
// route that used to be JWT-only is exactly how a security tidy-up turns into
// a regression for Moderators, Operators and Viewers.
func TestServersListRoute_EveryBuiltinRoleKeepsIt(t *testing.T) {
	setupTestDB(t)
	setupServerDir(t)
	bootTestRegistry(t)
	t.Setenv("JWT_SECRET", "test-secret")

	if err := services.EnsureBuiltinRoles(); err != nil {
		t.Fatalf("failed to seed roles: %v", err)
	}
	r := newRouter()

	for _, role := range types.BuiltinRoles {
		token := newTestUserToken(t, "user-"+role.Name, role.Name)
		w := doRequest(r, http.MethodGet, "/api/servers", token)
		if w.Code != http.StatusOK {
			t.Errorf("role %q must still be able to list servers, got %d, body=%s", role.Name, w.Code, w.Body.String())
		}
	}
}

func TestServersListRoute_DeniedWithoutARole(t *testing.T) {
	setupTestDB(t)
	setupServerDir(t)
	bootTestRegistry(t)
	t.Setenv("JWT_SECRET", "test-secret")
	token := newTestUserToken(t, "no-role-user", "")

	r := newRouter()

	w := doRequest(r, http.MethodGet, "/api/servers", token)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for an account with no role, got %d, body=%s", w.Code, w.Body.String())
	}
}

// TestNamespacedRoute_PermissionStillEnforced proves namespacing didn't
// loosen anything: a no-role user hitting a permission-gated action route
// (start, here) under /api/servers/:sid/... still gets 403, exactly like
// the flat route would.
func TestNamespacedRoute_PermissionStillEnforced(t *testing.T) {
	setupTestDB(t)
	setupServerDir(t)
	bootTestRegistry(t)
	t.Setenv("JWT_SECRET", "test-secret")
	token := newTestUserToken(t, "no-role-user", "")

	r := newRouter()

	flatW := doRequest(r, http.MethodPost, "/api/start", token)
	nsW := doRequest(r, http.MethodPost, "/api/servers/"+services.DefaultServerID+"/start", token)

	if flatW.Code != http.StatusForbidden {
		t.Errorf("flat /api/start: expected 403, got %d, body=%s", flatW.Code, flatW.Body.String())
	}
	if nsW.Code != http.StatusForbidden {
		t.Errorf("namespaced /api/servers/default/start: expected 403, got %d, body=%s", nsW.Code, nsW.Body.String())
	}
}

// doWrite is doRequest with a body -- the write routes need one, and a nil
// body would fail binding before the permission gate is ever the reason.
func doWrite(r *gin.Engine, method, path, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// Reading automations and managing them are different powers. A rule can run
// console commands, so automations.manage is console access by another name --
// it must never be reachable with only automations.view, which the built-in
// Moderator role holds.
func TestAutomationRoutes_ViewCannotWrite(t *testing.T) {
	setupTestDB(t)
	setupServerDir(t)
	bootTestRegistry(t)
	t.Setenv("JWT_SECRET", "test-secret")
	if err := services.EnsureBuiltinRoles(); err != nil {
		t.Fatalf("failed to seed roles: %v", err)
	}
	token := newTestUserToken(t, "mod", "Moderator")
	r := newRouter()

	for _, path := range []string{
		"/api/automations",
		"/api/automations/1",
		"/api/automations/1/firings",
		"/api/automation-webhooks",
	} {
		if w := doRequest(r, http.MethodGet, path, token); w.Code == http.StatusForbidden {
			t.Errorf("a Moderator should be able to read %s, got 403", path)
		}
	}

	// Every write route, not just POST /automations. A gate that covers four of
	// five routes is the same as no gate.
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/automations", `{"server_id":"default","name":"x","trigger_kind":"stop","trigger_config":{},"actions":[{"type":"backup"}]}`},
		{http.MethodPut, "/api/automations/1", `{"server_id":"default","name":"x","trigger_kind":"stop","trigger_config":{},"actions":[{"type":"backup"}]}`},
		{http.MethodDelete, "/api/automations/1", ""},
		{http.MethodPost, "/api/automations/1/enabled", `{"enabled":false}`},
		{http.MethodPost, "/api/automation-webhooks", `{"name":"x","url":"https://discord.com/api/webhooks/1/tok"}`},
		{http.MethodDelete, "/api/automation-webhooks/1", ""},
		{http.MethodPost, "/api/automation-webhooks/1/test", ""},
	} {
		w := doWrite(r, tc.method, tc.path, token, tc.body)
		if w.Code != http.StatusForbidden {
			t.Errorf("a Moderator must NOT reach %s %s, got %d: %s",
				tc.method, tc.path, w.Code, w.Body.String())
		}
	}
}

// The single most important assertion in the REST layer, run through the real
// router rather than a handler in isolation -- the same shape as the existing
// credential-redaction test.
func TestAutomationRoutes_TheWebhookURLNeverAppearsInAnyResponse(t *testing.T) {
	setupTestDB(t)
	setupServerDir(t)
	bootTestRegistry(t)
	t.Setenv("JWT_SECRET", "test-secret")
	if err := services.EnsureBuiltinRoles(); err != nil {
		t.Fatalf("failed to seed roles: %v", err)
	}
	token := newTestUserToken(t, "owner", "Owner")

	const secret = "https://discord.com/api/webhooks/123456789/SUPERSECRETTOKEN"
	hookID, err := automation.CreateWebhook(automation.Webhook{Name: "alertas", URL: secret})
	if err != nil {
		t.Fatalf("CreateWebhook: %v", err)
	}
	ruleID, err := automation.CreateRule(automation.Rule{
		ServerID: services.DefaultServerID, Name: "avisa", Enabled: true,
		TriggerKind: "stop", TriggerConfig: map[string]any{},
		Actions: []automation.Action{{Type: "discord", WebhookID: hookID, Message: "caiu"}},
	})
	if err != nil {
		t.Fatalf("CreateRule: %v", err)
	}
	if err := automation.RecordFiring(automation.Firing{
		RuleID: ruleID, Trigger: "stop", Outcome: `[{"action":"discord","ok":true}]`,
	}); err != nil {
		t.Fatalf("RecordFiring: %v", err)
	}

	r := newRouter()
	rid := strconv.Itoa(ruleID)
	for _, path := range []string{
		"/api/automations",
		"/api/automations/" + rid,
		"/api/automations/" + rid + "/firings",
		"/api/automation-webhooks",
	} {
		w := doRequest(r, http.MethodGet, path, token)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: expected 200, got %d: %s", path, w.Code, w.Body.String())
		}
		for _, leak := range []string{"SUPERSECRETTOKEN", "discord.com/api/webhooks"} {
			if strings.Contains(w.Body.String(), leak) {
				t.Errorf("%s leaked the webhook credential: %s", path, w.Body.String())
			}
		}
	}
}

// Gin panics at REGISTRATION when a static segment and a wildcard share a
// position, which takes the whole API down at boot instead of failing one
// endpoint. /automation-webhooks exists as its own prefix precisely because
// /automations/webhooks would sit beside /automations/:id. Building the router
// at all is the assertion.
func TestAutomationRoutes_RouterBuildsWithoutAConflict(t *testing.T) {
	setupTestDB(t)
	setupServerDir(t)
	bootTestRegistry(t)

	if r := newRouter(); r == nil {
		t.Fatal("expected a router")
	}
}

// The other half of the gate. Without this, a router that denied EVERYONE
// would pass TestAutomationRoutes_ViewCannotWrite -- a broken gate and a
// correct one look identical from the denied side.
func TestAutomationRoutes_ManageCanWriteEndToEnd(t *testing.T) {
	setupTestDB(t)
	setupServerDir(t)
	bootTestRegistry(t)
	t.Setenv("JWT_SECRET", "test-secret")
	if err := services.EnsureBuiltinRoles(); err != nil {
		t.Fatalf("failed to seed roles: %v", err)
	}
	token := newTestUserToken(t, "owner", "Owner")
	r := newRouter()

	body := `{"server_id":"default","name":"backup ao parar","trigger_kind":"stop",
	          "trigger_config":{},"actions":[{"type":"backup"}]}`
	w := doWrite(r, http.MethodPost, "/api/automations", token, body)
	if w.Code != http.StatusCreated {
		t.Fatalf("an Owner must be able to create a rule, got %d: %s", w.Code, w.Body.String())
	}

	rules, err := automation.ListRules()
	if err != nil || len(rules) != 1 {
		t.Fatalf("expected exactly one stored rule, got %+v (err %v)", rules, err)
	}
	id := strconv.Itoa(rules[0].ID)

	// Who created a rule is the only audit trail this table has, and it comes
	// from the JWT claim -- a shape that fails by being silently nil.
	if rules[0].CreatedBy == nil {
		t.Error("the rule was stored with no author")
	}

	if w := doWrite(r, http.MethodPost, "/api/automations/"+id+"/enabled", token, `{"enabled":false}`); w.Code != http.StatusOK {
		t.Errorf("disable: got %d: %s", w.Code, w.Body.String())
	}
	if w := doWrite(r, http.MethodDelete, "/api/automations/"+id, token, ""); w.Code != http.StatusOK {
		t.Errorf("delete: got %d: %s", w.Code, w.Body.String())
	}
	if rules, _ := automation.ListRules(); len(rules) != 0 {
		t.Errorf("the rule survived the delete: %+v", rules)
	}
}

// The one link nothing else covers: that a rule saved through the API reaches
// the running engine. Every other test proves half of it -- the handler calls
// its reloader, the engine reloads correctly -- and a missing
// SetEngineReloader would pass both while the feature did nothing until the
// next deploy.
func TestAutomations_ARuleSavedThroughTheAPIFiresWithoutARestart(t *testing.T) {
	setupTestDB(t)
	setupServerDir(t)
	bootTestRegistry(t)
	t.Setenv("JWT_SECRET", "test-secret")
	if err := services.EnsureBuiltinRoles(); err != nil {
		t.Fatalf("failed to seed roles: %v", err)
	}
	token := newTestUserToken(t, "owner", "Owner")

	engine, stop := startAutomations()
	t.Cleanup(stop)
	r := newRouter()

	// Nothing configured: the engine is asleep and asks for no samples.
	if engine.NeedsSampling(types.SampleTPS) {
		t.Error("an engine with no rules must not ask for TPS samples")
	}

	body := `{"server_id":"default","name":"reinicia com tps baixo","trigger_kind":"tps",
	          "trigger_config":{"below":5,"held_for_seconds":300},
	          "actions":[{"type":"backup"}]}`
	if w := doWrite(r, http.MethodPost, "/api/automations", token, body); w.Code != http.StatusCreated {
		t.Fatalf("create: got %d: %s", w.Code, w.Body.String())
	}

	// The engine learned about it from the write alone -- no restart, no boot.
	if !engine.NeedsSampling(types.SampleTPS) {
		t.Fatal("the engine did not pick up a rule saved through the API")
	}

	rules, _ := automation.ListRules()
	if len(rules) != 1 {
		t.Fatalf("expected one rule, got %d", len(rules))
	}
	id := strconv.Itoa(rules[0].ID)

	if w := doWrite(r, http.MethodPost, "/api/automations/"+id+"/enabled", token, `{"enabled":false}`); w.Code != http.StatusOK {
		t.Fatalf("disable: got %d: %s", w.Code, w.Body.String())
	}
	// And it stops measuring the moment the last rule that needed it is off.
	if engine.NeedsSampling(types.SampleTPS) {
		t.Error("the engine kept sampling for a rule that was disabled through the API")
	}
}
