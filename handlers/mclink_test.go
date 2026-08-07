package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lomokwa/mc-manager/db"
)

func TestStartMcLinkHandler_RejectsBadUsername(t *testing.T) {
	setupTestDB(t)
	_, r := withUser(t, "linker1", "")
	r.POST("/me/mclink/start", StartMcLinkHandler)

	for _, bad := range []string{"", "has space", "way-too-long-for-minecraft", "quote\"injection"} {
		body := strings.NewReader(`{"mc_username":"` + bad + `"}`)
		req := httptest.NewRequest(http.MethodPost, "/me/mclink/start", body)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("username %q: expected 400, got %d, body=%s", bad, w.Code, w.Body.String())
		}
	}
}

func TestStartMcLinkHandler_ServerNotRunning(t *testing.T) {
	setupTestDB(t)
	setupServerDir(t)
	clearStatusFile(t)
	_, r := withUser(t, "linker2", "")
	r.POST("/me/mclink/start", StartMcLinkHandler)

	body := strings.NewReader(`{"mc_username":"Herobrine"}`)
	req := httptest.NewRequest(http.MethodPost, "/me/mclink/start", body)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 when the server isn't running, got %d, body=%s", w.Code, w.Body.String())
	}
}

func TestVerifyMcLinkHandler_NoPendingCode(t *testing.T) {
	setupTestDB(t)
	_, r := withUser(t, "linker3", "")
	r.POST("/me/mclink/verify", VerifyMcLinkHandler)

	body := strings.NewReader(`{"code":"ABC123"}`)
	req := httptest.NewRequest(http.MethodPost, "/me/mclink/verify", body)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 with no pending code, got %d, body=%s", w.Code, w.Body.String())
	}
}

func TestVerifyMcLinkHandler_WrongCode(t *testing.T) {
	setupTestDB(t)
	userID, r := withUser(t, "linker4", "")
	seedPendingCode(t, userID, "Steve", "CORRECT")
	r.POST("/me/mclink/verify", VerifyMcLinkHandler)

	body := strings.NewReader(`{"code":"WRONG1"}`)
	req := httptest.NewRequest(http.MethodPost, "/me/mclink/verify", body)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for a mismatched code, got %d, body=%s", w.Code, w.Body.String())
	}
}

func TestVerifyMcLinkHandler_Expired(t *testing.T) {
	setupTestDB(t)
	userID, r := withUser(t, "linker5", "")
	if _, err := db.DB.Exec(
		`INSERT INTO mc_link_codes (user_id, mc_username, code, expires_at) VALUES (?, ?, ?, datetime('now', '-1 hour'))`,
		userID, "Steve", "CORRECT",
	); err != nil {
		t.Fatalf("failed to seed expired code: %v", err)
	}
	r.POST("/me/mclink/verify", VerifyMcLinkHandler)

	body := strings.NewReader(`{"code":"CORRECT"}`)
	req := httptest.NewRequest(http.MethodPost, "/me/mclink/verify", body)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for an expired code, got %d, body=%s", w.Code, w.Body.String())
	}
}

func TestVerifyMcLinkHandler_Success(t *testing.T) {
	setupTestDB(t)
	setupServerDir(t)
	userID, r := withUser(t, "linker6", "")
	seedPendingCode(t, userID, "Steve", "CORRECT")
	writeServerFile(t, "usercache.json", `[{"name":"Steve","uuid":"22222222-2222-2222-2222-222222222222","expiresOn":"2099-01-01T00:00:00Z"}]`)
	r.POST("/me/mclink/verify", VerifyMcLinkHandler)

	// lower-case on purpose: verification should be case-insensitive on the code itself.
	body := strings.NewReader(`{"code":"correct"}`)
	req := httptest.NewRequest(http.MethodPost, "/me/mclink/verify", body)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body=%s", w.Code, w.Body.String())
	}

	var mcUsername, uuid string
	if err := db.DB.QueryRow(`SELECT mc_username, mc_uuid FROM minecraft_links WHERE user_id = ?`, userID).
		Scan(&mcUsername, &uuid); err != nil {
		t.Fatalf("expected a saved link, got error: %v", err)
	}
	if mcUsername != "Steve" || uuid != "22222222-2222-2222-2222-222222222222" {
		t.Errorf("unexpected link: %s / %s", mcUsername, uuid)
	}

	var remaining int
	db.DB.QueryRow(`SELECT COUNT(*) FROM mc_link_codes WHERE user_id = ?`, userID).Scan(&remaining)
	if remaining != 0 {
		t.Error("expected the used code to be deleted after a successful verify")
	}
}

func TestGetMcLinkHandler_NoneLinked(t *testing.T) {
	setupTestDB(t)
	_, r := withUser(t, "linker7", "")
	r.GET("/me/mclink", GetMcLinkHandler)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/me/mclink", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 even with nothing linked, got %d, body=%s", w.Code, w.Body.String())
	}
}

func TestUnlinkMcHandler_RemovesLink(t *testing.T) {
	setupTestDB(t)
	userID, r := withUser(t, "linker8", "")
	if _, err := db.DB.Exec(`INSERT INTO minecraft_links (user_id, mc_username, mc_uuid) VALUES (?, ?, ?)`,
		userID, "Steve", "uuid-here"); err != nil {
		t.Fatalf("failed to seed a link: %v", err)
	}
	r.DELETE("/me/mclink", UnlinkMcHandler)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/me/mclink", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body=%s", w.Code, w.Body.String())
	}
	var count int
	db.DB.QueryRow(`SELECT COUNT(*) FROM minecraft_links WHERE user_id = ?`, userID).Scan(&count)
	if count != 0 {
		t.Error("expected the link to be removed")
	}
}

func seedPendingCode(t *testing.T, userID int, mcUsername, code string) {
	t.Helper()
	if _, err := db.DB.Exec(
		`INSERT INTO mc_link_codes (user_id, mc_username, code, expires_at) VALUES (?, ?, ?, datetime('now', '+10 minutes'))`,
		userID, mcUsername, code,
	); err != nil {
		t.Fatalf("failed to seed pending code: %v", err)
	}
}
