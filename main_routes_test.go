package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
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

// TestServersListRoute_NoExtraPermissionRequired proves GET /api/servers
// needs nothing beyond a valid JWT -- same gate as GET /api/status -- by
// reaching it with a user who has NO role assigned at all (deny-by-default
// per services/permissions.go's EffectivePermissions). If this route were
// accidentally gated behind a specific permission, this user would get 403
// instead of 200.
func TestServersListRoute_NoExtraPermissionRequired(t *testing.T) {
	setupTestDB(t)
	setupServerDir(t)
	bootTestRegistry(t)
	t.Setenv("JWT_SECRET", "test-secret")
	token := newTestUserToken(t, "no-role-user", "")

	r := newRouter()

	w := doRequest(r, http.MethodGet, "/api/servers", token)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for a no-role authenticated user, got %d, body=%s", w.Code, w.Body.String())
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

// The panel calls GET /api/world on every Players load and it has been 404ing
// in production, which apiFetch degrades to "unsupported" -- silent, so it
// went unnoticed for weeks. This asserts the route exists and, like every
// other pair, that the namespaced form behaves the same as the flat one.
func TestWorldRoute_ExistsFlatAndNamespaced(t *testing.T) {
	setupTestDB(t)
	setupServerDir(t)
	bootTestRegistry(t)
	t.Setenv("JWT_SECRET", "test-secret")
	if err := services.EnsureBuiltinRoles(); err != nil {
		t.Fatalf("failed to seed roles: %v", err)
	}
	token := newTestUserToken(t, "viewer", "Viewer")

	r := newRouter()

	for _, path := range []string{"/api/world", "/api/servers/" + services.DefaultServerID + "/world"} {
		w := doRequest(r, http.MethodGet, path, token)
		if w.Code != http.StatusOK {
			t.Errorf("%s: expected 200, got %d, body=%s", path, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "level_name") {
			t.Errorf("%s: expected a level_name in the body, got %s", path, w.Body.String())
		}
	}
}
