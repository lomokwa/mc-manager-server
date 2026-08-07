package services

import (
	"os"
	"testing"

	"github.com/lomokwa/mc-manager/db"
)

// withSeedFile writes content to PermissionsSeedPath for the duration of a
// test and removes it afterward. PermissionsSeedPath is a fixed relative
// path (like ServerDir/BackupDir), so tests using this must not run in
// parallel with each other.
func withSeedFile(t *testing.T, content string) {
	t.Helper()
	if err := os.WriteFile(PermissionsSeedPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write seed file: %v", err)
	}
	t.Cleanup(func() {
		os.Remove(PermissionsSeedPath)
	})
}

func TestApplyPermissionsSeed_AssignsMatchingUsers(t *testing.T) {
	setupTestDB(t)
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
	if err := EnsureBuiltinRoles(); err != nil {
		t.Fatalf("failed to seed roles: %v", err)
	}
	withSeedFile(t, `{"users":[{"username":"not-registered-yet","role":"Owner"}]}`)

	// Must not panic or error out just because the account doesn't exist yet.
	ApplyPermissionsSeed()
}

func TestApplyPermissionsSeed_MissingFileIsANoOp(t *testing.T) {
	setupTestDB(t)
	if err := EnsureBuiltinRoles(); err != nil {
		t.Fatalf("failed to seed roles: %v", err)
	}
	os.Remove(PermissionsSeedPath) // ensure it's really absent

	ApplyPermissionsSeed() // must not panic or error
}

func TestApplyPermissionsSeed_UnknownRoleNameIsSkipped(t *testing.T) {
	setupTestDB(t)
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

func TestEnsureBootstrapOwner_PromotesFirstRegisteredUser(t *testing.T) {
	setupTestDB(t)
	if err := EnsureBuiltinRoles(); err != nil {
		t.Fatalf("failed to seed roles: %v", err)
	}
	firstID := insertTestUser(t, "first-ever")
	insertTestUser(t, "second-ever")
	os.Remove(PermissionsSeedPath) // no seed file at all in this scenario

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
