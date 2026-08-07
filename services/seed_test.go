package services

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lomokwa/mc-manager/db"
)

// withSeedFile writes content directly to the final, already-settled
// PermissionsSeedPath (inside ControlDir) -- for tests of ApplyPermissionsSeed's
// entry-processing behavior, which is independent of where the file
// originally came from. The settleSeedFile move itself has its own tests
// below. Callers must have already called setupServerDir(t).
func withSeedFile(t *testing.T, content string) {
	t.Helper()
	if err := os.MkdirAll(ControlDir, 0755); err != nil {
		t.Fatalf("failed to create control dir: %v", err)
	}
	if err := os.WriteFile(PermissionsSeedPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write seed file: %v", err)
	}
}

func TestApplyPermissionsSeed_AssignsMatchingUsers(t *testing.T) {
	setupTestDB(t)
	setupServerDir(t)
	if err := EnsureBuiltinRoles(); err != nil {
		t.Fatalf("failed to seed roles: %v", err)
	}
	ownerID := insertTestUser(t, "lomokwa")
	adminID := insertTestUser(t, "Ant")
	withSeedFile(t, `{"users":[{"username":"lomokwa","role":"Owner"},{"username":"Ant","role":"Admin"}]}`)

	ApplyPermissionsSeed()

	owner, err := EffectivePermissions(ownerID)
	if err != nil || owner.RoleName != "Owner" {
		t.Errorf("expected lomokwa to be Owner, got %q (err=%v)", owner.RoleName, err)
	}
	admin, err := EffectivePermissions(adminID)
	if err != nil || admin.RoleName != "Admin" {
		t.Errorf("expected Ant to be Admin, got %q (err=%v)", admin.RoleName, err)
	}
}

func TestApplyPermissionsSeed_NeverOverwritesAnExistingAssignment(t *testing.T) {
	setupTestDB(t)
	setupServerDir(t)
	if err := EnsureBuiltinRoles(); err != nil {
		t.Fatalf("failed to seed roles: %v", err)
	}
	userID := insertTestUser(t, "already-set")
	if err := SetUserRole(userID, "Viewer"); err != nil {
		t.Fatalf("failed to pre-assign a role: %v", err)
	}
	// A seed file that disagrees with what's already assigned -- it must lose.
	withSeedFile(t, `{"users":[{"username":"already-set","role":"Owner"}]}`)

	ApplyPermissionsSeed()

	up, err := EffectivePermissions(userID)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if up.RoleName != "Viewer" {
		t.Errorf("expected the pre-existing Viewer assignment to survive, got %q", up.RoleName)
	}
}

func TestApplyPermissionsSeed_SkipsUnregisteredUsername(t *testing.T) {
	setupTestDB(t)
	setupServerDir(t)
	if err := EnsureBuiltinRoles(); err != nil {
		t.Fatalf("failed to seed roles: %v", err)
	}
	withSeedFile(t, `{"users":[{"username":"not-registered-yet","role":"Owner"}]}`)

	// Must not panic or error out just because the account doesn't exist yet.
	ApplyPermissionsSeed()
}

func TestApplyPermissionsSeed_MissingFileIsANoOp(t *testing.T) {
	setupTestDB(t)
	setupServerDir(t)
	if err := EnsureBuiltinRoles(); err != nil {
		t.Fatalf("failed to seed roles: %v", err)
	}
	// No seed file anywhere -- fallbacks and the settled path are all absent.

	ApplyPermissionsSeed() // must not panic or error
}

func TestApplyPermissionsSeed_UnknownRoleNameIsSkipped(t *testing.T) {
	setupTestDB(t)
	setupServerDir(t)
	if err := EnsureBuiltinRoles(); err != nil {
		t.Fatalf("failed to seed roles: %v", err)
	}
	userID := insertTestUser(t, "typo-role")
	withSeedFile(t, `{"users":[{"username":"typo-role","role":"Ownerr"}]}`)

	ApplyPermissionsSeed()

	up, err := EffectivePermissions(userID)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if up.RoleName != "" {
		t.Errorf("expected no role assigned from a typo'd role name, got %q", up.RoleName)
	}
}

func TestSettleSeedFile_MovesFromServerDirFallback(t *testing.T) {
	setupServerDir(t)
	writeServerFile(t, "permissions-seed.json", `{"users":[]}`)

	settleSeedFile()

	if _, err := os.Stat(filepath.Join(ServerDir, "permissions-seed.json")); !os.IsNotExist(err) {
		t.Error("expected the dropped file to be gone from ServerDir after settling")
	}
	if _, err := os.Stat(PermissionsSeedPath); err != nil {
		t.Errorf("expected the file to land at PermissionsSeedPath, got: %v", err)
	}
}

func TestSettleSeedFile_MovesFromRepoRootFallback(t *testing.T) {
	setupServerDir(t)
	const repoRootPath = "./permissions-seed.json"
	if err := os.WriteFile(repoRootPath, []byte(`{"users":[]}`), 0644); err != nil {
		t.Fatalf("failed to write repo-root fallback: %v", err)
	}
	t.Cleanup(func() { os.Remove(repoRootPath) })

	settleSeedFile()

	if _, err := os.Stat(repoRootPath); !os.IsNotExist(err) {
		t.Error("expected the repo-root fallback to be gone after settling")
	}
	if _, err := os.Stat(PermissionsSeedPath); err != nil {
		t.Errorf("expected the file to land at PermissionsSeedPath, got: %v", err)
	}
}

func TestSettleSeedFile_PrefersServerDirOverRepoRoot(t *testing.T) {
	setupServerDir(t)
	writeServerFile(t, "permissions-seed.json", `{"users":[{"username":"from-serverdir","role":"Viewer"}]}`)
	const repoRootPath = "./permissions-seed.json"
	if err := os.WriteFile(repoRootPath, []byte(`{"users":[{"username":"from-reporoot","role":"Viewer"}]}`), 0644); err != nil {
		t.Fatalf("failed to write repo-root fallback: %v", err)
	}
	t.Cleanup(func() { os.Remove(repoRootPath) })

	settleSeedFile()

	settled, err := os.ReadFile(PermissionsSeedPath)
	if err != nil {
		t.Fatalf("expected a settled file, got error: %v", err)
	}
	if !strings.Contains(string(settled), "from-serverdir") {
		t.Errorf("expected the ServerDir fallback to win, got: %s", settled)
	}
	// The lower-priority fallback is left untouched, not consumed.
	if _, err := os.Stat(repoRootPath); err != nil {
		t.Error("expected the repo-root fallback to be left alone once ServerDir's copy already won")
	}
}

func TestSettleSeedFile_NeverOverwritesAlreadySettledFile(t *testing.T) {
	setupServerDir(t)
	if err := os.MkdirAll(ControlDir, 0755); err != nil {
		t.Fatalf("failed to create control dir: %v", err)
	}
	if err := os.WriteFile(PermissionsSeedPath, []byte(`{"users":[{"username":"already-settled","role":"Viewer"}]}`), 0644); err != nil {
		t.Fatalf("failed to seed the settled file: %v", err)
	}
	writeServerFile(t, "permissions-seed.json", `{"users":[{"username":"newly-dropped","role":"Owner"}]}`)

	settleSeedFile()

	settled, err := os.ReadFile(PermissionsSeedPath)
	if err != nil {
		t.Fatalf("expected the settled file to still exist, got error: %v", err)
	}
	if !strings.Contains(string(settled), "already-settled") {
		t.Errorf("expected the already-settled file to survive untouched, got: %s", settled)
	}
	// The fresh drop in ServerDir is left in place, not silently discarded.
	if _, err := os.Stat(filepath.Join(ServerDir, "permissions-seed.json")); err != nil {
		t.Error("expected the newly-dropped file to be left alone rather than deleted")
	}
}

func TestSettleSeedFile_NoFallbacksIsANoOp(t *testing.T) {
	setupServerDir(t)

	settleSeedFile() // must not panic or error with nothing to find

	if _, err := os.Stat(PermissionsSeedPath); !os.IsNotExist(err) {
		t.Error("expected no settled file to appear from nothing")
	}
}

func TestEnsureBootstrapOwner_PromotesFirstRegisteredUser(t *testing.T) {
	setupTestDB(t)
	setupServerDir(t)
	if err := EnsureBuiltinRoles(); err != nil {
		t.Fatalf("failed to seed roles: %v", err)
	}
	firstID := insertTestUser(t, "first-ever")
	insertTestUser(t, "second-ever")
	// No seed file anywhere in this scenario.

	EnsureBootstrapOwner()

	up, err := EffectivePermissions(firstID)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if up.RoleName != "Owner" {
		t.Errorf("expected the first-registered user to become Owner, got %q", up.RoleName)
	}
}

func TestEnsureBootstrapOwner_NoOpIfAnyoneAlreadyHasARole(t *testing.T) {
	setupTestDB(t)
	if err := EnsureBuiltinRoles(); err != nil {
		t.Fatalf("failed to seed roles: %v", err)
	}
	firstID := insertTestUser(t, "first")
	secondID := insertTestUser(t, "second")
	// The seed already assigned the SECOND user (e.g. Admin) -- the
	// bootstrap must not then also promote the first user to Owner.
	if err := SetUserRole(secondID, "Admin"); err != nil {
		t.Fatalf("failed to pre-assign role: %v", err)
	}

	EnsureBootstrapOwner()

	up, err := EffectivePermissions(firstID)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if up.RoleName != "" {
		t.Errorf("expected the bootstrap to leave the first user alone once anyone has a role, got %q", up.RoleName)
	}
}

func TestEnsureBootstrapOwner_NoUsersAtAll(t *testing.T) {
	setupTestDB(t)
	if err := EnsureBuiltinRoles(); err != nil {
		t.Fatalf("failed to seed roles: %v", err)
	}

	EnsureBootstrapOwner() // must not panic with an empty users table

	var count int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM user_roles`).Scan(&count); err != nil {
		t.Fatalf("failed to count user_roles: %v", err)
	}
	if count != 0 {
		t.Errorf("expected no role assignments with zero registered users, got %d", count)
	}
}
