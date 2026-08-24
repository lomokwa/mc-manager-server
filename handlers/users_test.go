package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/lomokwa/mc-manager/db"
	"github.com/lomokwa/mc-manager/types"
)

func TestCreateInvitationHandler_Success(t *testing.T) {
	setupTestDB(t)

	r := newTestRouter()
	r.POST("/invitations", CreateInvitationHandler)

	req := httptest.NewRequest(http.MethodPost, "/invitations", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body=%s", w.Code, w.Body.String())
	}

	var resp struct {
		Data types.Invitation `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Data.Token == "" {
		t.Error("expected a non-empty invitation token")
	}
}

func TestValidateInvitationHandler_Invalid(t *testing.T) {
	setupTestDB(t)

	r := newTestRouter()
	r.GET("/invitations/:token", ValidateInvitationHandler)

	req := httptest.NewRequest(http.MethodGet, "/invitations/nonexistent-token", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d, body=%s", w.Code, w.Body.String())
	}
}

func TestValidateInvitationHandler_Valid(t *testing.T) {
	setupTestDB(t)

	createRouter := newTestRouter()
	createRouter.POST("/invitations", CreateInvitationHandler)
	w := httptest.NewRecorder()
	createRouter.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/invitations", nil))
	var createResp struct {
		Data types.Invitation `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &createResp)

	r := newTestRouter()
	r.GET("/invitations/:token", ValidateInvitationHandler)

	req := httptest.NewRequest(http.MethodGet, "/invitations/"+createResp.Data.Token, nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body=%s", w.Code, w.Body.String())
	}
}

func TestRegisterHandler_InvalidBody(t *testing.T) {
	setupTestDB(t)

	r := newTestRouter()
	r.POST("/register", RegisterHandler)

	req := httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(`{"username": "bob"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d, body=%s", w.Code, w.Body.String())
	}
}

func TestRegisterHandler_InvalidToken(t *testing.T) {
	setupTestDB(t)

	r := newTestRouter()
	r.POST("/register", RegisterHandler)

	body := `{"token": "bogus", "username": "bob", "password": "hunter2"}`
	req := httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d, body=%s", w.Code, w.Body.String())
	}
}

func TestRegisterHandler_Success(t *testing.T) {
	setupTestDB(t)

	createRouter := newTestRouter()
	createRouter.POST("/invitations", CreateInvitationHandler)
	w := httptest.NewRecorder()
	createRouter.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/invitations", nil))
	var createResp struct {
		Data types.Invitation `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &createResp)

	r := newTestRouter()
	r.POST("/register", RegisterHandler)

	body := `{"token": "` + createResp.Data.Token + `", "username": "bob", "password": "hunter2"}`
	req := httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d, body=%s", w.Code, w.Body.String())
	}

	var count int
	if err := db.DB.QueryRow("SELECT COUNT(*) FROM users WHERE username = ?", "bob").Scan(&count); err != nil {
		t.Fatalf("failed to query users: %v", err)
	}
	if count != 1 {
		t.Errorf("expected user to be created, got count=%d", count)
	}
}

func TestGetUsersHandler_Success(t *testing.T) {
	setupTestDB(t)
	if _, err := db.DB.Exec("INSERT INTO users (username, password_hash) VALUES (?, ?)", "alice", "hash"); err != nil {
		t.Fatalf("failed to seed user: %v", err)
	}

	r := newTestRouter()
	r.GET("/users", GetUsersHandler)

	req := httptest.NewRequest(http.MethodGet, "/users", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body=%s", w.Code, w.Body.String())
	}

	var resp struct {
		Data []types.User `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Data) != 1 || resp.Data[0].Username != "alice" {
		t.Errorf("expected one user 'alice', got %+v", resp.Data)
	}
}

func TestUpdateEmailHandler_Success(t *testing.T) {
	setupTestDB(t)
	_, r := withUser(t, "emailuser1", "")
	r.PATCH("/me/email", UpdateEmailHandler)

	req := httptest.NewRequest(http.MethodPatch, "/me/email", strings.NewReader(`{"email": "emailuser1@example.com"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body=%s", w.Code, w.Body.String())
	}

	var resp struct {
		Data types.User `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Data.Email != "emailuser1@example.com" {
		t.Errorf("expected email to be set, got %q", resp.Data.Email)
	}
}

func TestUpdateEmailHandler_RejectsMalformed(t *testing.T) {
	setupTestDB(t)
	_, r := withUser(t, "emailuser2", "")
	r.PATCH("/me/email", UpdateEmailHandler)

	req := httptest.NewRequest(http.MethodPatch, "/me/email", strings.NewReader(`{"email": "not-an-email"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d, body=%s", w.Code, w.Body.String())
	}
}

func TestUpdateEmailHandler_Unauthorized(t *testing.T) {
	setupTestDB(t)
	r := newTestRouter()
	r.PATCH("/me/email", UpdateEmailHandler)

	req := httptest.NewRequest(http.MethodPatch, "/me/email", strings.NewReader(`{"email": "nobody@example.com"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d, body=%s", w.Code, w.Body.String())
	}
}

func TestChangePasswordHandler_WrongCurrentPassword(t *testing.T) {
	setupTestDB(t)
	t.Setenv("JWT_SECRET", "test-secret")

	registerRouter := newTestRouter()
	registerRouter.POST("/invitations", CreateInvitationHandler)
	registerRouter.POST("/register", RegisterHandler)
	w := httptest.NewRecorder()
	registerRouter.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/invitations", nil))
	var createResp struct {
		Data types.Invitation `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &createResp)
	regBody := `{"token": "` + createResp.Data.Token + `", "username": "pwuser1", "password": "correctpw"}`
	req := httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(regBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	registerRouter.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("setup: expected register to succeed, got %d, body=%s", w.Code, w.Body.String())
	}

	var id int
	if err := db.DB.QueryRow("SELECT id FROM users WHERE username = ?", "pwuser1").Scan(&id); err != nil {
		t.Fatalf("failed to look up registered user: %v", err)
	}

	r := newTestRouter()
	r.Use(func(c *gin.Context) {
		c.Set("userID", float64(id))
		c.Next()
	})
	r.POST("/me/password", ChangePasswordHandler)

	body := `{"current_password": "wrongpw", "new_password": "newpassword"}`
	req = httptest.NewRequest(http.MethodPost, "/me/password", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d, body=%s", w.Code, w.Body.String())
	}
}

func TestChangePasswordHandler_Success(t *testing.T) {
	setupTestDB(t)
	t.Setenv("JWT_SECRET", "test-secret")

	registerRouter := newTestRouter()
	registerRouter.POST("/invitations", CreateInvitationHandler)
	registerRouter.POST("/register", RegisterHandler)
	w := httptest.NewRecorder()
	registerRouter.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/invitations", nil))
	var createResp struct {
		Data types.Invitation `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &createResp)
	regBody := `{"token": "` + createResp.Data.Token + `", "username": "pwuser2", "password": "correctpw"}`
	req := httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(regBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	registerRouter.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("setup: expected register to succeed, got %d, body=%s", w.Code, w.Body.String())
	}

	var id int
	if err := db.DB.QueryRow("SELECT id FROM users WHERE username = ?", "pwuser2").Scan(&id); err != nil {
		t.Fatalf("failed to look up registered user: %v", err)
	}

	r := newTestRouter()
	r.Use(func(c *gin.Context) {
		c.Set("userID", float64(id))
		c.Next()
	})
	r.POST("/me/password", ChangePasswordHandler)

	body := `{"current_password": "correctpw", "new_password": "newpassword"}`
	req = httptest.NewRequest(http.MethodPost, "/me/password", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body=%s", w.Code, w.Body.String())
	}

	loginRouter := newTestRouter()
	loginRouter.POST("/login", LoginHandler)
	loginBody := `{"username": "pwuser2", "password": "newpassword"}`
	req = httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(loginBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	loginRouter.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected login with new password to succeed, got %d, body=%s", w.Code, w.Body.String())
	}
}

func TestLoginHandler_InvalidBody(t *testing.T) {
	setupTestDB(t)

	r := newTestRouter()
	r.POST("/login", LoginHandler)

	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username": "bob"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d, body=%s", w.Code, w.Body.String())
	}
}

func TestLoginHandler_WrongCredentials(t *testing.T) {
	setupTestDB(t)

	r := newTestRouter()
	r.POST("/login", LoginHandler)

	body := `{"username": "nobody", "password": "wrong"}`
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d, body=%s", w.Code, w.Body.String())
	}
}

func TestLoginHandler_Success(t *testing.T) {
	setupTestDB(t)
	t.Setenv("JWT_SECRET", "test-secret")

	createRouter := newTestRouter()
	createRouter.POST("/invitations", CreateInvitationHandler)
	w := httptest.NewRecorder()
	createRouter.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/invitations", nil))
	var createResp struct {
		Data types.Invitation `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &createResp)

	registerRouter := newTestRouter()
	registerRouter.POST("/register", RegisterHandler)
	regBody := `{"token": "` + createResp.Data.Token + `", "username": "carol", "password": "s3cret"}`
	req := httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(regBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	registerRouter.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("setup: expected register to succeed, got %d, body=%s", w.Code, w.Body.String())
	}

	r := newTestRouter()
	r.POST("/login", LoginHandler)

	body := `{"username": "carol", "password": "s3cret"}`
	req = httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body=%s", w.Code, w.Body.String())
	}

	var resp struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Data.Token == "" {
		t.Error("expected a non-empty JWT token")
	}
}
