package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/lomokwa/mc-manager/services"
	"github.com/lomokwa/mc-manager/types"
)

const testServerProperties = "gamemode=survival\ndifficulty=easy\nmotd=A Minecraft Server\n"

func TestGetServerPropertiesHandler_MissingFile(t *testing.T) {
	setupServerDir(t)

	r := newTestRouter()
	r.GET("/properties", GetServerPropertiesHandler)

	req := httptest.NewRequest(http.MethodGet, "/properties", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d, body=%s", w.Code, w.Body.String())
	}
}

func TestGetServerPropertiesHandler_Success(t *testing.T) {
	setupServerDir(t)
	writeServerFile(t, "server.properties", testServerProperties)

	r := newTestRouter()
	r.GET("/properties", GetServerPropertiesHandler)

	req := httptest.NewRequest(http.MethodGet, "/properties", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body=%s", w.Code, w.Body.String())
	}

	var resp struct {
		Success bool              `json:"success"`
		Data    map[string]string `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if !resp.Success {
		t.Fatal("expected success=true")
	}
	if resp.Data["gamemode"] != "survival" || resp.Data["difficulty"] != "easy" {
		t.Errorf("unexpected properties: %+v", resp.Data)
	}
}

func TestUpdateServerPropertiesHandler_InvalidBody(t *testing.T) {
	setupServerDir(t)

	r := newTestRouter()
	r.POST("/properties", UpdateServerPropertiesHandler)

	req := httptest.NewRequest(http.MethodPost, "/properties", strings.NewReader("{not json"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d, body=%s", w.Code, w.Body.String())
	}
}

// TestUpdateServerPropertiesHandler_InvalidPropertyValue exercises the fix for
// a bug where the handler was missing a `return` after the validation error,
// causing it to also invoke services.UpdateServerProperties and write a
// second (conflicting) JSON response.
func TestUpdateServerPropertiesHandler_InvalidPropertyValue(t *testing.T) {
	setupServerDir(t)
	writeServerFile(t, "server.properties", testServerProperties)

	r := newTestRouter()
	r.POST("/properties", UpdateServerPropertiesHandler)

	body := `{"properties": {"gamemode": "not-a-real-mode"}}`
	req := httptest.NewRequest(http.MethodPost, "/properties", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d, body=%s", w.Code, w.Body.String())
	}

	var resp types.APIResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response was not valid single JSON object (double-write bug?): %v, body=%s", err, w.Body.String())
	}
	if resp.Success {
		t.Error("expected success=false")
	}

	// The properties file must be untouched since validation failed.
	got, err := os.ReadFile(services.ServerDir + "/server.properties")
	if err != nil {
		t.Fatalf("failed to read properties file: %v", err)
	}
	if string(got) != testServerProperties {
		t.Errorf("expected server.properties to be unchanged, got %q", got)
	}
}

func TestUpdateServerPropertiesHandler_Success(t *testing.T) {
	setupServerDir(t)
	writeServerFile(t, "server.properties", testServerProperties)

	r := newTestRouter()
	r.POST("/properties", UpdateServerPropertiesHandler)

	body := `{"properties": {"gamemode": "creative", "pvp": "false"}}`
	req := httptest.NewRequest(http.MethodPost, "/properties", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body=%s", w.Code, w.Body.String())
	}

	got, err := os.ReadFile(services.ServerDir + "/server.properties")
	if err != nil {
		t.Fatalf("failed to read properties file: %v", err)
	}
	content := string(got)
	if !strings.Contains(content, "gamemode=creative") {
		t.Errorf("expected updated gamemode in file, got %q", content)
	}
	if !strings.Contains(content, "pvp=false") {
		t.Errorf("expected new pvp property in file, got %q", content)
	}
	if !strings.Contains(content, "difficulty=easy") {
		t.Errorf("expected untouched property preserved, got %q", content)
	}
}

func TestUpdateServerPropertiesHandler_MissingFile(t *testing.T) {
	setupServerDir(t)

	r := newTestRouter()
	r.POST("/properties", UpdateServerPropertiesHandler)

	body := `{"properties": {"gamemode": "creative"}}`
	req := httptest.NewRequest(http.MethodPost, "/properties", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d, body=%s", w.Code, w.Body.String())
	}
}
