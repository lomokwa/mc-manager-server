package services

import (
	"os"
	"strings"
	"testing"

	"github.com/lomokwa/mc-manager/db"
)

// withSeedFile writes content to ServerDir/permissions-seed.json, the
// highest-priority candidate -- matching where an operator actually drops
// it (next to server.properties). Callers must have already called
// setupServerDir(t).
func withSeedFile(t *testing.T, content string) {
	t.Helper()
	writeServerFile(t, "permissions-seed.json", content)
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
	// No seed file anywhere.

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

// TestApplyPermissionsSeed_FixingATypoThenRestartingWorks is the scenario an
// operator actually hits: they typo a username or role name, the entry is
// silently skipped (logged, not crashed), and later they fix the file and
// restart. There must be nothing left over from the bad first attempt that
// stops the corrected one from taking effect.
func TestApplyPermissionsSeed_FixingATypoThenRestartingWorks(t *testing.T) {
	setupTestDB(t)
	setupServerDir(t)
	if err := EnsureBuiltinRoles(); err != nil {
		t.Fatalf("failed to seed roles: %v", err)
	}
	userID := insertTestUser(t, "lomokwa")
	withSeedFile(t, `{"users":[{"username":"lomokwa","role":"Ownre"}]}`) // typo'd role

	ApplyPermissionsSeed() // first boot: silently no-ops on the bad entry

	if up, _ := EffectivePermissions(userID); up.RoleName != "" {
		t.Fatalf("expected no role from the typo'd first attempt, got %q", up.RoleName)
	}

	withSeedFile(t, `{"users":[{"username":"lomokwa","role":"Owner"}]}`) // corrected

	ApplyPermissionsSeed() // second boot: must pick up the fix

	up, err := EffectivePermissions(userID)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if up.RoleName != "Owner" {
		t.Errorf("expected the corrected seed file to be applied on the next boot, got %q", up.RoleName)
	}
}

func TestReadFirstSeedFile_PrefersServerDirOverRepoRoot(t *testing.T) {
	setupServerDir(t)
	writeServerFile(t, "permissions-seed.json", `{"users":[{"username":"from-serverdir","role":"Viewer"}]}`)
	const repoRootPath = "./permissions-seed.json"
	if err := os.WriteFile(repoRootPath, []byte(`{"users":[{"username":"from-reporoot","role":"Viewer"}]}`), 0644); err != nil {
		t.Fatalf("failed to write repo-root fallback: %v", err)
	}
	t.Cleanup(func() { os.Remove(repoRootPath) })

	_, data := readFirstSeedFile()

	if data == nil || !strings.Contains(string(data), "from-serverdir") {
		t.Errorf("expected the ServerDir candidate to win, got: %s", data)
	}
}

func TestReadFirstSeedFile_FallsBackToRepoRoot(t *testing.T) {
	setupServerDir(t) // ServerDir exists but has no seed file
	const repoRootPath = "./permissions-seed.json"
	if err := os.WriteFile(repoRootPath, []byte(`{"users":[{"username":"from-reporoot","role":"Viewer"}]}`), 0644); err != nil {
		t.Fatalf("failed to write repo-root fallback: %v", err)
	}
	t.Cleanup(func() { os.Remove(repoRootPath) })

	_, data := readFirstSeedFile()

	if data == nil || !strings.Contains(string(data), "from-reporoot") {
		t.Errorf("expected the repo-root fallback to be used, got: %s", data)
	}
}

func TestReadFirstSeedFile_NoCandidatesReturnsNil(t *testing.T) {
	setupServerDir(t)

	_, data := readFirstSeedFile()

	if data != nil {
		t.Errorf("expected nil with no seed file anywhere, got: %s", data)
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
