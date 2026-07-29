package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lomokwa/mc-manager/types"
)

func TestListPlayersHandler_MissingUserCache(t *testing.T) {
	setupServerDir(t)

	r := newTestRouter()
	r.GET("/players", ListPlayersHandler)

	req := httptest.NewRequest(http.MethodGet, "/players", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d, body=%s", w.Code, w.Body.String())
	}

	var resp types.APIResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Success {
		t.Error("expected success=false")
	}
}

func TestListPlayersHandler_Success(t *testing.T) {
	setupServerDir(t)
	writeServerFile(t, "usercache.json", `[
		{"uuid": "11111111-1111-1111-1111-111111111111", "name": "Alice", "expiresOn": "2099-01-01"},
		{"uuid": "22222222-2222-2222-2222-222222222222", "name": "Bob", "expiresOn": "2099-01-01"}
	]`)
	writeServerFile(t, "ops.json", `[{"uuid": "11111111-1111-1111-1111-111111111111", "name": "Alice", "level": 4}]`)
	writeServerFile(t, "whitelist.json", `[{"uuid": "22222222-2222-2222-2222-222222222222", "name": "Bob"}]`)
	writeServerFile(t, "banned-players.json", `[]`)

	r := newTestRouter()
	r.GET("/players", ListPlayersHandler)

	req := httptest.NewRequest(http.MethodGet, "/players", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body=%s", w.Code, w.Body.String())
	}

	var resp struct {
		Success bool           `json:"success"`
		Data    []types.Player `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if !resp.Success {
		t.Fatal("expected success=true")
	}
	if len(resp.Data) != 2 {
		t.Fatalf("expected 2 players, got %d", len(resp.Data))
	}

	byName := map[string]types.Player{}
	for _, p := range resp.Data {
		byName[p.Name] = p
	}

	if !byName["Alice"].IsOp {
		t.Error("expected Alice to be op")
	}
	if byName["Alice"].IsWhitelisted {
		t.Error("expected Alice to not be whitelisted")
	}
	if !byName["Bob"].IsWhitelisted {
		t.Error("expected Bob to be whitelisted")
	}
	if byName["Bob"].IsOp {
		t.Error("expected Bob to not be op")
	}
	if byName["Alice"].Online || byName["Bob"].Online {
		t.Error("expected no players online (server not running)")
	}
}
