package services

import (
	"testing"

	"github.com/lomokwa/mc-manager/db"
	"github.com/lomokwa/mc-manager/types"
)

func insertTestUser(t *testing.T, username string) int {
	t.Helper()
	res, err := db.DB.Exec(`INSERT INTO users (username, password_hash) VALUES (?, ?)`, username, "hash")
	if err != nil {
		t.Fatalf("failed to insert test user %q: %v", username, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("failed to read inserted id: %v", err)
	}
	return int(id)
}

func TestEnsureBuiltinRoles_CreatesAllFive(t *testing.T) {
	setupTestDB(t)

	if err := EnsureBuiltinRoles(); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	roles, err := ListRoles()
	if err != nil {
		t.Fatalf("failed to list roles: %v", err)
	}
	if len(roles) != len(types.BuiltinRoles) {
		t.Fatalf("expected %d roles, got %d", len(types.BuiltinRoles), len(roles))
	}
	for _, want := range types.BuiltinRoles {
		found := false
		for _, got := range roles {
			if got.Name == want.Name {
				found = true
				if !got.IsSystem {
					t.Errorf("role %q: expected is_system=true", want.Name)
				}
				if len(got.Permissions) != len(want.Permissions) {
					t.Errorf("role %q: expected %d permissions, got %d", want.Name, len(want.Permissions), len(got.Permissions))
				}
			}
		}
		if !found {
			t.Errorf("expected built-in role %q to exist", want.Name)
		}
	}
}

func TestEnsureBuiltinRoles_IdempotentAndPreservesEdits(t *testing.T) {
	setupTestDB(t)

	if err := EnsureBuiltinRoles(); err != nil {
		t.Fatalf("first call: expected no error, got %v", err)
	}
	// Simulate an admin having hand-tuned a built-in role's permissions.
	if _, err := db.DB.Exec(`UPDATE roles SET permissions = '["console.read"]' WHERE name = 'Viewer'`); err != nil {
		t.Fatalf("failed to hand-edit role: %v", err)
	}

	if err := EnsureBuiltinRoles(); err != nil {
		t.Fatalf("second call: expected no error, got %v", err)
	}

	_, perms, _, err := GetRoleByName("Viewer")
	if err != nil {
		t.Fatalf("failed to load Viewer role: %v", err)
	}
	if len(perms) != 1 || perms[0] != types.PermConsoleRead {
		t.Errorf("expected the hand-edit to survive a second EnsureBuiltinRoles call, got %v", perms)
	}
}

func TestEffectivePermissions_NoRoleAssigned(t *testing.T) {
	setupTestDB(t)
	if err := EnsureBuiltinRoles(); err != nil {
		t.Fatalf("failed to seed roles: %v", err)
	}
	userID := insertTestUser(t, "nobody")

	up, err := EffectivePermissions(userID)
	if err != nil {
		t.Fatalf("expected no error for an unassigned user, got %v", err)
	}
	if up.RoleName != "" {
		t.Errorf("expected empty role name, got %q", up.RoleName)
	}
	if len(up.Permissions) != 0 {
		t.Errorf("expected no permissions, got %v", up.Permissions)
	}
	if HasPermission(userID, types.PermConsoleRead) {
		t.Error("expected HasPermission to deny by default for a user with no role")
	}
}

func TestEffectivePermissions_RoleOnly(t *testing.T) {
	setupTestDB(t)
	if err := EnsureBuiltinRoles(); err != nil {
		t.Fatalf("failed to seed roles: %v", err)
	}
	userID := insertTestUser(t, "op-alice")
	if err := SetUserRole(userID, "Operator"); err != nil {
		t.Fatalf("failed to assign role: %v", err)
	}

	up, err := EffectivePermissions(userID)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if up.RoleName != "Operator" {
		t.Errorf("expected role Operator, got %q", up.RoleName)
	}
	if !up.Permissions[types.PermServerStart] {
		t.Error("expected Operator to have server.start")
	}
	if up.Permissions[types.PermFilesDelete] {
		t.Error("expected Operator NOT to have files.delete")
	}
}

func TestEffectivePermissions_OverrideWinsOverRoleDefault(t *testing.T) {
	setupTestDB(t)
	if err := EnsureBuiltinRoles(); err != nil {
		t.Fatalf("failed to seed roles: %v", err)
	}
	userID := insertTestUser(t, "custom-mod")
	if err := SetUserRole(userID, "Moderator"); err != nil {
		t.Fatalf("failed to assign role: %v", err)
	}
	// A moderator trusted with console access but explicitly NOT allowed to
	// ban -- override should revoke a permission the role would otherwise grant.
	if err := SetUserOverrides(userID, map[types.Permission]bool{types.PermPlayersModerate: false}); err != nil {
		t.Fatalf("failed to set overrides: %v", err)
	}
	// And grant something the role doesn't normally include.
	if err := SetUserOverrides(userID, map[types.Permission]bool{
		types.PermPlayersModerate: false,
		types.PermFilesRead:       true,
	}); err != nil {
		t.Fatalf("failed to set overrides: %v", err)
	}

	up, err := EffectivePermissions(userID)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if up.Permissions[types.PermPlayersModerate] {
		t.Error("expected the override to revoke players.moderate")
	}
	if !up.Permissions[types.PermFilesRead] {
		t.Error("expected the override to grant files.read")
	}
	if !up.Permissions[types.PermConsoleRead] {
		t.Error("expected console.read to remain from the role default")
	}
}

func TestSetUserRole_ResetsOverridesOnRoleChange(t *testing.T) {
	setupTestDB(t)
	if err := EnsureBuiltinRoles(); err != nil {
		t.Fatalf("failed to seed roles: %v", err)
	}
	userID := insertTestUser(t, "switcher")
	if err := SetUserRole(userID, "Viewer"); err != nil {
		t.Fatalf("failed to assign initial role: %v", err)
	}
	if err := SetUserOverrides(userID, map[types.Permission]bool{types.PermFilesDelete: true}); err != nil {
		t.Fatalf("failed to set override: %v", err)
	}

	if err := SetUserRole(userID, "Operator"); err != nil {
		t.Fatalf("failed to change role: %v", err)
	}

	up, err := EffectivePermissions(userID)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if up.RoleName != "Operator" {
		t.Errorf("expected role Operator, got %q", up.RoleName)
	}
	if up.Permissions[types.PermFilesDelete] {
		t.Error("expected the stale override from the old role to be cleared on role change")
	}
}

func TestSetUserRole_UnknownRole(t *testing.T) {
	setupTestDB(t)
	if err := EnsureBuiltinRoles(); err != nil {
		t.Fatalf("failed to seed roles: %v", err)
	}
	userID := insertTestUser(t, "nobody2")

	if err := SetUserRole(userID, "SuperDuperAdmin"); err == nil {
		t.Error("expected an error for an unknown role name")
	}
}

func TestSetUserOverrides_NoRoleYet(t *testing.T) {
	setupTestDB(t)
	if err := EnsureBuiltinRoles(); err != nil {
		t.Fatalf("failed to seed roles: %v", err)
	}
	userID := insertTestUser(t, "roleless")

	if err := SetUserOverrides(userID, map[types.Permission]bool{types.PermConsoleRead: true}); err == nil {
		t.Error("expected an error setting overrides for a user with no role assigned")
	}
}

func TestLookupUUID(t *testing.T) {
	setupServerDir(t)
	writeServerFile(t, "usercache.json", `[
		{"name":"Herobrine","uuid":"11111111-1111-1111-1111-111111111111","expiresOn":"2099-01-01T00:00:00Z"}
	]`)

	uuid, ok := LookupUUID("herobrine") // case-insensitive on purpose
	if !ok {
		t.Fatal("expected to find a case-insensitive match")
	}
	if uuid != "11111111-1111-1111-1111-111111111111" {
		t.Errorf("unexpected uuid: %q", uuid)
	}

	if _, ok := LookupUUID("Nobody"); ok {
		t.Error("expected no match for a name not in usercache.json")
	}
}
