package services

import (
	"strings"
	"testing"
	"time"

	"github.com/lomokwa/mc-manager/db"
	"github.com/lomokwa/mc-manager/types"
)

func TestCreateInvitation_Success(t *testing.T) {
	setupTestDB(t)
	t.Setenv("CLIENT_URL", "https://example.com")

	inv, err := CreateInvitation()
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if inv.Token == "" {
		t.Error("expected a non-empty token")
	}
	if !strings.Contains(inv.Link, inv.Token) {
		t.Errorf("expected link to contain token, got %q", inv.Link)
	}
	if !inv.ExpiresAt.After(time.Now()) {
		t.Error("expected ExpiresAt to be in the future")
	}
}

func TestValidateInvitation_Unknown(t *testing.T) {
	setupTestDB(t)

	if err := ValidateInvitation("does-not-exist"); err == nil {
		t.Error("expected an error for an unknown token")
	}
}

func TestValidateInvitation_Valid(t *testing.T) {
	setupTestDB(t)

	inv, err := CreateInvitation()
	if err != nil {
		t.Fatalf("failed to create invitation: %v", err)
	}

	if err := ValidateInvitation(inv.Token); err != nil {
		t.Errorf("expected no error for a fresh invitation, got %v", err)
	}
}

func TestValidateInvitation_Expired(t *testing.T) {
	setupTestDB(t)

	_, err := db.DB.Exec(
		"INSERT INTO invitations (token, expires_at) VALUES (?, datetime('now', '-1 hour'))",
		"expired-token",
	)
	if err != nil {
		t.Fatalf("failed to seed expired invitation: %v", err)
	}

	if err := ValidateInvitation("expired-token"); err == nil {
		t.Error("expected an error for an expired invitation")
	}
}

func TestValidateInvitation_AlreadyUsed(t *testing.T) {
	setupTestDB(t)

	inv, err := CreateInvitation()
	if err != nil {
		t.Fatalf("failed to create invitation: %v", err)
	}
	if err := Register(types.RegisterRequest{Token: inv.Token, Username: "firstuser", Password: "pw123456"}); err != nil {
		t.Fatalf("failed to register: %v", err)
	}

	if err := ValidateInvitation(inv.Token); err == nil {
		t.Error("expected an error for an already-used invitation")
	}
}

func TestRegister_InvalidToken(t *testing.T) {
	setupTestDB(t)

	err := Register(types.RegisterRequest{Token: "bogus", Username: "bob", Password: "hunter2"})
	if err == nil {
		t.Error("expected an error for an invalid token")
	}
}

func TestRegister_Success(t *testing.T) {
	setupTestDB(t)

	inv, err := CreateInvitation()
	if err != nil {
		t.Fatalf("failed to create invitation: %v", err)
	}

	if err := Register(types.RegisterRequest{Token: inv.Token, Username: "bob", Password: "hunter2"}); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	var count int
	if err := db.DB.QueryRow("SELECT COUNT(*) FROM users WHERE username = ?", "bob").Scan(&count); err != nil {
		t.Fatalf("failed to query users: %v", err)
	}
	if count != 1 {
		t.Errorf("expected exactly one user 'bob', got %d", count)
	}

	var usedAt *string
	if err := db.DB.QueryRow("SELECT used_at FROM invitations WHERE token = ?", inv.Token).Scan(&usedAt); err != nil {
		t.Fatalf("failed to query invitation: %v", err)
	}
	if usedAt == nil {
		t.Error("expected invitation to be marked used")
	}
}

func TestRegister_DuplicateUsername(t *testing.T) {
	setupTestDB(t)

	inv1, err := CreateInvitation()
	if err != nil {
		t.Fatalf("failed to create invitation: %v", err)
	}
	if err := Register(types.RegisterRequest{Token: inv1.Token, Username: "dupe", Password: "hunter2"}); err != nil {
		t.Fatalf("failed first registration: %v", err)
	}

	inv2, err := CreateInvitation()
	if err != nil {
		t.Fatalf("failed to create second invitation: %v", err)
	}
	if err := Register(types.RegisterRequest{Token: inv2.Token, Username: "dupe", Password: "otherpw"}); err == nil {
		t.Error("expected an error registering a duplicate username")
	}
}

func TestLogin_UnknownUser(t *testing.T) {
	setupTestDB(t)

	if _, err := Login(types.LoginRequest{Username: "nobody", Password: "wrong"}); err == nil {
		t.Error("expected an error for an unknown user")
	}
}

func TestLogin_WrongPassword(t *testing.T) {
	setupTestDB(t)

	inv, err := CreateInvitation()
	if err != nil {
		t.Fatalf("failed to create invitation: %v", err)
	}
	if err := Register(types.RegisterRequest{Token: inv.Token, Username: "dave", Password: "correctpw"}); err != nil {
		t.Fatalf("failed to register: %v", err)
	}

	if _, err := Login(types.LoginRequest{Username: "dave", Password: "wrongpw"}); err == nil {
		t.Error("expected an error for the wrong password")
	}
}

func TestLogin_Success(t *testing.T) {
	setupTestDB(t)
	t.Setenv("JWT_SECRET", "test-secret")

	inv, err := CreateInvitation()
	if err != nil {
		t.Fatalf("failed to create invitation: %v", err)
	}
	if err := Register(types.RegisterRequest{Token: inv.Token, Username: "erin", Password: "correctpw"}); err != nil {
		t.Fatalf("failed to register: %v", err)
	}

	token, err := Login(types.LoginRequest{Username: "erin", Password: "correctpw"})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if token == "" {
		t.Error("expected a non-empty JWT")
	}
}

func TestGetUsers_Empty(t *testing.T) {
	setupTestDB(t)

	users, err := GetUsers()
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(users) != 0 {
		t.Errorf("expected no users, got %+v", users)
	}
}

func TestGetUsers_ReturnsCreatedUsers(t *testing.T) {
	setupTestDB(t)

	inv, err := CreateInvitation()
	if err != nil {
		t.Fatalf("failed to create invitation: %v", err)
	}
	if err := Register(types.RegisterRequest{Token: inv.Token, Username: "frank", Password: "pw"}); err != nil {
		t.Fatalf("failed to register: %v", err)
	}

	users, err := GetUsers()
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(users) != 1 || users[0].Username != "frank" {
		t.Errorf("expected one user 'frank', got %+v", users)
	}
}

func registerTestUser(t *testing.T, username, password string) int {
	t.Helper()
	inv, err := CreateInvitation()
	if err != nil {
		t.Fatalf("failed to create invitation: %v", err)
	}
	if err := Register(types.RegisterRequest{Token: inv.Token, Username: username, Password: password}); err != nil {
		t.Fatalf("failed to register: %v", err)
	}
	var id int
	if err := db.DB.QueryRow("SELECT id FROM users WHERE username = ?", username).Scan(&id); err != nil {
		t.Fatalf("failed to look up registered user: %v", err)
	}
	return id
}

func TestUpdateEmail_SetsEmail(t *testing.T) {
	setupTestDB(t)
	id := registerTestUser(t, "gina", "pw")

	if err := UpdateEmail(id, "gina@example.com"); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	user, err := GetUserByID(id)
	if err != nil {
		t.Fatalf("failed to fetch user: %v", err)
	}
	if user.Email != "gina@example.com" {
		t.Errorf("expected email to be set, got %q", user.Email)
	}
}

func TestUpdateEmail_ClearsEmail(t *testing.T) {
	setupTestDB(t)
	id := registerTestUser(t, "hank", "pw")

	if err := UpdateEmail(id, "hank@example.com"); err != nil {
		t.Fatalf("failed to set email: %v", err)
	}
	if err := UpdateEmail(id, ""); err != nil {
		t.Fatalf("expected no error clearing email, got %v", err)
	}

	user, err := GetUserByID(id)
	if err != nil {
		t.Fatalf("failed to fetch user: %v", err)
	}
	if user.Email != "" {
		t.Errorf("expected email to be cleared, got %q", user.Email)
	}
}

func TestUpdateEmail_MultipleUsersCanLeaveEmailUnset(t *testing.T) {
	setupTestDB(t)
	id1 := registerTestUser(t, "ivan", "pw")
	id2 := registerTestUser(t, "jill", "pw")

	if err := UpdateEmail(id1, ""); err != nil {
		t.Fatalf("expected no error for first user, got %v", err)
	}
	if err := UpdateEmail(id2, ""); err != nil {
		t.Fatalf("expected no error for second user with unset email too, got %v", err)
	}
}

func TestUpdateEmail_DuplicateRejected(t *testing.T) {
	setupTestDB(t)
	id1 := registerTestUser(t, "ken", "pw")
	id2 := registerTestUser(t, "liz", "pw")

	if err := UpdateEmail(id1, "shared@example.com"); err != nil {
		t.Fatalf("failed to set first user's email: %v", err)
	}
	if err := UpdateEmail(id2, "shared@example.com"); err == nil {
		t.Error("expected an error assigning a duplicate email")
	}
}

func TestChangePassword_WrongCurrentPassword(t *testing.T) {
	setupTestDB(t)
	id := registerTestUser(t, "mona", "correctpw")

	if err := ChangePassword(id, "wrongpw", "newpassword"); err == nil {
		t.Error("expected an error for the wrong current password")
	}
}

func TestChangePassword_Success(t *testing.T) {
	setupTestDB(t)
	t.Setenv("JWT_SECRET", "test-secret")
	id := registerTestUser(t, "nora", "correctpw")

	if err := ChangePassword(id, "correctpw", "newpassword"); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if _, err := Login(types.LoginRequest{Username: "nora", Password: "correctpw"}); err == nil {
		t.Error("expected the old password to no longer work")
	}
	if _, err := Login(types.LoginRequest{Username: "nora", Password: "newpassword"}); err != nil {
		t.Errorf("expected the new password to work, got %v", err)
	}
}
