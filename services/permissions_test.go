package services

import (
	"encoding/json"
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

// A system role's permission list is owned by types.BuiltinRoles, and every
// boot re-asserts it. This test used to assert the opposite -- that a row which
// had drifted from the code was left alone -- but it produced the drift with a
// raw UPDATE, and no route in the product can do that: GET /api/roles is
// read-only and the two writable role endpoints assign a role to a user or set
// per-user overrides. So the preserved "edit" was unreachable in practice,
// while the cost of preserving it was severe and silent: see the regression
// test below.
//
// If hand-editable roles ever ship, they need their own storage or an
// explicit "customised" flag -- not a seeder that quietly stops seeding.
func TestEnsureBuiltinRoles_ReassertsTheCodesListOnEveryBoot(t *testing.T) {
	setupTestDB(t)

	if err := EnsureBuiltinRoles(); err != nil {
		t.Fatalf("first call: expected no error, got %v", err)
	}
	if _, err := db.DB.Exec(`UPDATE roles SET permissions = '["console.read"]' WHERE name = 'Viewer'`); err != nil {
		t.Fatalf("failed to drift the role: %v", err)
	}

	if err := EnsureBuiltinRoles(); err != nil {
		t.Fatalf("second call: expected no error, got %v", err)
	}

	_, perms, _, err := GetRoleByName("Viewer")
	if err != nil {
		t.Fatalf("failed to load Viewer role: %v", err)
	}
	var want []types.Permission
	for _, r := range types.BuiltinRoles {
		if r.Name == "Viewer" {
			want = r.Permissions
		}
	}
	if len(perms) != len(want) {
		t.Fatalf("expected the row to be restored to the code's %d permissions, got %d: %v", len(want), len(perms), perms)
	}
}

// The bug this guards against: with ON CONFLICT DO NOTHING, a permission added
// to the schema never reached an already-seeded role, so the feature it gated
// was invisible to everyone -- including the Owner -- on every existing
// install, with nothing logged. Adding a permission has to be enough.
func TestEnsureBuiltinRoles_ANewPermissionReachesAnAlreadySeededRole(t *testing.T) {
	setupTestDB(t)

	// Seed as an older build would have: Viewer exists, without the newer
	// overview.view that types.BuiltinRoles now grants it.
	if _, err := db.DB.Exec(
		`INSERT INTO roles (name, permissions, is_system) VALUES ('Viewer', '["console.read"]', 1)`,
	); err != nil {
		t.Fatalf("failed to seed the pre-upgrade row: %v", err)
	}

	if err := EnsureBuiltinRoles(); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	_, perms, _, err := GetRoleByName("Viewer")
	if err != nil {
		t.Fatalf("failed to load Viewer role: %v", err)
	}
	found := false
	for _, p := range perms {
		if p == types.PermOverviewView {
			found = true
		}
	}
	if !found {
		t.Errorf("a permission added to the schema must reach an existing install's role, got %v", perms)
	}
}

// Full rehearsal of what happens to the LIVE database on the deploy that
// carries this change: seed every role exactly as the pre-change build left
// them, boot, and check both directions. Roles must gain the new permissions
// (otherwise the new tabs are dead for everyone) and must lose none (otherwise
// this "security tidy-up" quietly takes access away from real people).
func TestEnsureBuiltinRoles_UpgradeFromThePreviousBuildGrantsAndTakesNothing(t *testing.T) {
	setupTestDB(t)

	// The role rows as the previous build seeded them, verbatim.
	previous := map[string][]types.Permission{
		"Owner": {
			types.PermServerStart, types.PermServerStop,
			types.PermConsoleRead, types.PermConsoleChat, types.PermConsoleCommands,
			types.PermFilesRead, types.PermFilesUpload, types.PermFilesEdit, types.PermFilesDelete,
			types.PermBackupsView, types.PermBackupsCreate, types.PermBackupsDownload,
			types.PermBackupsDelete, types.PermBackupsRestore,
			types.PermSettingsView, types.PermSettingsEdit,
			types.PermPerformanceView, types.PermPerformanceReport,
			types.PermPlayersView, types.PermPlayersModerate,
			types.PermAdminManageUsers, types.PermAdminManageRoles,
		},
		"Moderator": {
			types.PermConsoleRead, types.PermConsoleChat, types.PermConsoleCommands,
			types.PermPlayersView, types.PermPlayersModerate,
		},
		"Operator": {
			types.PermServerStart, types.PermServerStop,
			types.PermConsoleRead, types.PermConsoleChat,
			types.PermPlayersView,
		},
		"Viewer": {
			types.PermConsoleRead, types.PermPerformanceView, types.PermPlayersView,
		},
	}
	previous["Admin"] = previous["Owner"]

	for name, perms := range previous {
		blob, err := json.Marshal(perms)
		if err != nil {
			t.Fatalf("marshal %s: %v", name, err)
		}
		if _, err := db.DB.Exec(
			`INSERT INTO roles (name, permissions, is_system) VALUES (?, ?, 1)`, name, string(blob),
		); err != nil {
			t.Fatalf("seed pre-upgrade role %q: %v", name, err)
		}
	}

	if err := EnsureBuiltinRoles(); err != nil {
		t.Fatalf("upgrade boot failed: %v", err)
	}

	for name, before := range previous {
		_, after, _, err := GetRoleByName(name)
		if err != nil {
			t.Fatalf("load %q after upgrade: %v", name, err)
		}
		have := make(map[types.Permission]bool, len(after))
		for _, p := range after {
			have[p] = true
		}
		for _, p := range before {
			if !have[p] {
				t.Errorf("role %q LOST %q across the upgrade", name, p)
			}
		}
	}

	// And the four new surfaces actually arrive where they were meant to.
	for _, want := range []struct {
		role string
		perm types.Permission
	}{
		{"Owner", types.PermAutomationsManage},
		{"Owner", types.PermActivityView},
		{"Admin", types.PermServersManage},
		{"Moderator", types.PermActivityView},
		{"Viewer", types.PermServersView},
		{"Viewer", types.PermOverviewView},
	} {
		_, after, _, err := GetRoleByName(want.role)
		if err != nil {
			t.Fatalf("load %q: %v", want.role, err)
		}
		found := false
		for _, p := range after {
			if p == want.perm {
				found = true
			}
		}
		if !found {
			t.Errorf("role %q did not gain %q on upgrade", want.role, want.perm)
		}
	}

	// Viewer must NOT quietly pick up the powerful ones.
	_, viewer, _, err := GetRoleByName("Viewer")
	if err != nil {
		t.Fatalf("load Viewer: %v", err)
	}
	for _, p := range viewer {
		if p == types.PermAutomationsManage || p == types.PermServersManage || p == types.PermActivityView {
			t.Errorf("Viewer must not gain %q", p)
		}
	}
}

// The thing that genuinely must survive re-seeding: a per-user override. Those
// live on user_roles, not on the role, and are applied on top in
// EffectivePermissions -- so someone explicitly denied a permission keeps that
// denial even as their role gains it.
func TestEnsureBuiltinRoles_PerUserOverridesSurviveReseeding(t *testing.T) {
	setupTestDB(t)
	if err := EnsureBuiltinRoles(); err != nil {
		t.Fatalf("failed to seed roles: %v", err)
	}
	userID := insertTestUser(t, "denied")
	if err := SetUserRole(userID, "Viewer"); err != nil {
		t.Fatalf("failed to assign role: %v", err)
	}
	if err := SetUserOverrides(userID, map[types.Permission]bool{types.PermConsoleRead: false}); err != nil {
		t.Fatalf("failed to set override: %v", err)
	}

	if err := EnsureBuiltinRoles(); err != nil {
		t.Fatalf("re-seed: expected no error, got %v", err)
	}

	if HasPermission(userID, types.PermConsoleRead) {
		t.Error("an explicit per-user deny must survive the role being re-seeded")
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
