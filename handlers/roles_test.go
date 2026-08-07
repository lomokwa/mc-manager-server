package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/lomokwa/mc-manager/db"
	"github.com/lomokwa/mc-manager/services"
	"github.com/lomokwa/mc-manager/types"
)

// withUser inserts a user (optionally pre-assigned a role) and returns a gin
// engine that sets "userID" in context before delegating, matching what
// ValidateJWT does in production.
func withUser(t *testing.T, username, role string) (id int, r *gin.Engine) {
	t.Helper()
	if err := services.EnsureBuiltinRoles(); err != nil {
		t.Fatalf("failed to seed roles: %v", err)
	}
	res, err := db.DB.Exec(`INSERT INTO users (username, password_hash) VALUES (?, ?)`, username, "hash")
	if err != nil {
		t.Fatalf("failed to insert user: %v", err)
	}
	id64, _ := res.LastInsertId()
	id = int(id64)
	if role != "" {
		if err := services.SetUserRole(id, role); err != nil {
			t.Fatalf("failed to assign role: %v", err)
		}
	}
	r = newTestRouter()
	r.Use(func(c *gin.Context) {
		c.Set("userID", float64(id))
		c.Next()
	})
	return id, r
}

func TestMyPermissionsHandler(t *testing.T) {
	setupTestDB(t)
	_, r := withUser(t, "viewer1", "Viewer")
	r.GET("/me/permissions", MyPermissionsHandler)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/me/permissions", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Role        string          `json:"role"`
			Permissions map[string]bool `json:"permissions"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Data.Role != "Viewer" {
		t.Errorf("expected role Viewer, got %q", resp.Data.Role)
	}
	if !resp.Data.Permissions[string(types.PermConsoleRead)] {
		t.Error("expected console.read to be true for a Viewer")
	}
}

func TestListRolesHandler(t *testing.T) {
	setupTestDB(t)
	_, r := withUser(t, "admin1", "Admin")
	r.GET("/roles", ListRolesHandler)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/roles", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Data []types.RoleInfo `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Data) != len(types.BuiltinRoles) {
		t.Errorf("expected %d roles, got %d", len(types.BuiltinRoles), len(resp.Data))
	}
}

func TestGetUserPermissionsHandler_NotFound(t *testing.T) {
	setupTestDB(t)
	_, r := withUser(t, "admin2", "Admin")
	r.GET("/users/:id/permissions", GetUserPermissionsHandler)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/users/999999/permissions", nil))

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestSetUserRoleHandler_Success(t *testing.T) {
	setupTestDB(t)
	adminID, r := withUser(t, "admin3", "Admin")
	targetID := insertPlainUser(t, "target1")
	r.PUT("/users/:id/role", SetUserRoleHandler)

	body := strings.NewReader(`{"role":"Moderator"}`)
	req := httptest.NewRequest(http.MethodPut, "/users/"+strconv.Itoa(targetID)+"/role", body)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body=%s", w.Code, w.Body.String())
	}
	up, err := services.EffectivePermissions(targetID)
	if err != nil || up.RoleName != "Moderator" {
		t.Errorf("expected target to become Moderator, got %q (err=%v)", up.RoleName, err)
	}
	_ = adminID
}

func TestSetUserRoleHandler_RefusesOwner(t *testing.T) {
	setupTestDB(t)
	_, r := withUser(t, "admin4", "Admin")
	targetID := insertPlainUser(t, "target2")
	r.PUT("/users/:id/role", SetUserRoleHandler)

	body := strings.NewReader(`{"role":"Owner"}`)
	req := httptest.NewRequest(http.MethodPut, "/users/"+strconv.Itoa(targetID)+"/role", body)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 assigning Owner through the API, got %d, body=%s", w.Code, w.Body.String())
	}
}

func TestSetUserRoleHandler_UnknownUser(t *testing.T) {
	setupTestDB(t)
	_, r := withUser(t, "admin5", "Admin")
	r.PUT("/users/:id/role", SetUserRoleHandler)

	body := strings.NewReader(`{"role":"Viewer"}`)
	req := httptest.NewRequest(http.MethodPut, "/users/999999/role", body)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestSetUserRoleHandler_UnknownRoleName(t *testing.T) {
	setupTestDB(t)
	_, r := withUser(t, "admin6", "Admin")
	targetID := insertPlainUser(t, "target3")
	r.PUT("/users/:id/role", SetUserRoleHandler)

	body := strings.NewReader(`{"role":"NotARealRole"}`)
	req := httptest.NewRequest(http.MethodPut, "/users/"+strconv.Itoa(targetID)+"/role", body)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestSetUserRoleHandler_SelfLockoutBlocked(t *testing.T) {
	setupTestDB(t)
	adminID, r := withUser(t, "self-admin", "Admin")
	r.PUT("/users/:id/role", SetUserRoleHandler)

	// Admin tries to demote themselves to Viewer, which has no admin.manage_roles.
	body := strings.NewReader(`{"role":"Viewer"}`)
	req := httptest.NewRequest(http.MethodPut, "/users/"+strconv.Itoa(adminID)+"/role", body)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 self-demoting away admin.manage_roles, got %d, body=%s", w.Code, w.Body.String())
	}
	up, _ := services.EffectivePermissions(adminID)
	if up.RoleName != "Admin" {
		t.Errorf("expected the role to remain Admin after a blocked self-demotion, got %q", up.RoleName)
	}
}

func TestSetUserOverridesHandler_SelfLockoutBlocked(t *testing.T) {
	setupTestDB(t)
	adminID, r := withUser(t, "self-admin2", "Admin")
	r.PUT("/users/:id/overrides", SetUserOverridesHandler)

	body := strings.NewReader(`{"overrides":{"admin.manage_roles":false}}`)
	req := httptest.NewRequest(http.MethodPut, "/users/"+strconv.Itoa(adminID)+"/overrides", body)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d, body=%s", w.Code, w.Body.String())
	}
}

func TestSetUserOverridesHandler_OwnerProtected(t *testing.T) {
	setupTestDB(t)
	_, r := withUser(t, "admin7", "Admin")
	ownerID := insertPlainUser(t, "the-owner")
	if err := services.SetUserRole(ownerID, "Owner"); err != nil {
		t.Fatalf("failed to assign Owner directly (bypassing the handler, as the seed file would): %v", err)
	}
	r.PUT("/users/:id/overrides", SetUserOverridesHandler)

	body := strings.NewReader(`{"overrides":{"server.stop":false}}`)
	req := httptest.NewRequest(http.MethodPut, "/users/"+strconv.Itoa(ownerID)+"/overrides", body)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 editing Owner's permissions, got %d, body=%s", w.Code, w.Body.String())
	}
}

func insertPlainUser(t *testing.T, username string) int {
	t.Helper()
	res, err := db.DB.Exec(`INSERT INTO users (username, password_hash) VALUES (?, ?)`, username, "hash")
	if err != nil {
		t.Fatalf("failed to insert user %q: %v", username, err)
	}
	id, _ := res.LastInsertId()
	return int(id)
}
