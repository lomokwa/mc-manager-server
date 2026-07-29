package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/lomokwa/mc-manager/services"
	"github.com/lomokwa/mc-manager/types"
)

func TestCreateServerHandler_InvalidBody(t *testing.T) {
	setupServerDir(t)

	r := newTestRouter()
	r.POST("/server", CreateServerHandler)

	req := httptest.NewRequest(http.MethodPost, "/server", strings.NewReader(`{not json`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d, body=%s", w.Code, w.Body.String())
	}
}

func TestCreateServerHandler_InvalidProperties(t *testing.T) {
	setupServerDir(t)

	r := newTestRouter()
	r.POST("/server", CreateServerHandler)

	body := `{"serverType": "vanilla", "properties": {"gamemode": "not-a-mode"}}`
	req := httptest.NewRequest(http.MethodPost, "/server", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d, body=%s", w.Code, w.Body.String())
	}
}

func TestCreateServerHandler_AlreadyExists(t *testing.T) {
	setupServerDir(t)
	writeServerFile(t, "server.jar", "fake jar")

	r := newTestRouter()
	r.POST("/server", CreateServerHandler)

	body := `{"serverType": "vanilla"}`
	req := httptest.NewRequest(http.MethodPost, "/server", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d, body=%s", w.Code, w.Body.String())
	}
	var resp types.APIResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if !strings.Contains(resp.Error, "already exists") {
		t.Errorf("expected 'already exists' error, got %q", resp.Error)
	}
}

func TestCreateServerHandler_UnsupportedType(t *testing.T) {
	setupServerDir(t)

	r := newTestRouter()
	r.POST("/server", CreateServerHandler)

	body := `{"serverType": "bogus"}`
	req := httptest.NewRequest(http.MethodPost, "/server", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d, body=%s", w.Code, w.Body.String())
	}
	var resp types.APIResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if !strings.Contains(resp.Error, "unsupported server type") {
		t.Errorf("expected 'unsupported server type' error, got %q", resp.Error)
	}
}

func TestStartServerHandler_NotCreated(t *testing.T) {
	setupServerDir(t)

	r := newTestRouter()
	r.POST("/start", StartServerHandler)

	req := httptest.NewRequest(http.MethodPost, "/start", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d, body=%s", w.Code, w.Body.String())
	}
}

func TestStopServerHandler_NotRunning(t *testing.T) {
	setupServerDir(t)

	r := newTestRouter()
	r.POST("/stop", StopServerHandler)

	req := httptest.NewRequest(http.MethodPost, "/stop", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d, body=%s", w.Code, w.Body.String())
	}
	var resp types.APIResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if !strings.Contains(resp.Error, "not running") {
		t.Errorf("expected 'not running' error, got %q", resp.Error)
	}
}

func TestDeleteServerHandler_NoServer(t *testing.T) {
	setupServerDir(t)

	r := newTestRouter()
	r.DELETE("/server", DeleteServerHandler)

	req := httptest.NewRequest(http.MethodDelete, "/server", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d, body=%s", w.Code, w.Body.String())
	}
	var resp types.APIResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if !strings.Contains(resp.Error, "no server to delete") {
		t.Errorf("expected 'no server to delete' error, got %q", resp.Error)
	}
}

func TestDeleteServerHandler_WhileRunning(t *testing.T) {
	setupServerDir(t)
	writeServerFile(t, "server.jar", "fake jar")
	status := types.ServerRuntimeStatus{Running: true, Heartbeat: time.Now()}
	statusJSON, _ := json.Marshal(status)
	writeServerFile(t, ".mcmanager/status.json", string(statusJSON))

	r := newTestRouter()
	r.DELETE("/server", DeleteServerHandler)

	req := httptest.NewRequest(http.MethodDelete, "/server", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d, body=%s", w.Code, w.Body.String())
	}
	var resp types.APIResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if !strings.Contains(resp.Error, "cannot delete server while it is running") {
		t.Errorf("expected 'running' error, got %q", resp.Error)
	}
}

func TestDeleteServerHandler_Success(t *testing.T) {
	setupServerDir(t)
	writeServerFile(t, "server.jar", "fake jar")
	writeServerFile(t, "world/level.dat", "fake world data")

	r := newTestRouter()
	r.DELETE("/server", DeleteServerHandler)

	req := httptest.NewRequest(http.MethodDelete, "/server", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body=%s", w.Code, w.Body.String())
	}

	if _, err := os.Stat(services.ServerJarPath); !os.IsNotExist(err) {
		t.Error("expected server.jar to be removed")
	}
}

func TestServerExistsHandler_NotExists(t *testing.T) {
	setupServerDir(t)

	r := newTestRouter()
	r.GET("/server", ServerExistsHandler)

	req := httptest.NewRequest(http.MethodGet, "/server", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body=%s", w.Code, w.Body.String())
	}

	var resp struct {
		Data map[string]any `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Data["exists"] != false {
		t.Errorf("expected exists=false, got %+v", resp.Data)
	}
}

func TestServerExistsHandler_ExistsWithMeta(t *testing.T) {
	setupServerDir(t)
	writeServerFile(t, "server.jar", "fake jar")
	writeServerFile(t, "server-meta.json", `{"serverType": "vanilla", "gameVersion": "1.21", "loaderVersion": ""}`)

	r := newTestRouter()
	r.GET("/server", ServerExistsHandler)

	req := httptest.NewRequest(http.MethodGet, "/server", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body=%s", w.Code, w.Body.String())
	}

	var resp struct {
		Data map[string]any `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Data["exists"] != true {
		t.Errorf("expected exists=true, got %+v", resp.Data)
	}
	if resp.Data["serverType"] != "vanilla" || resp.Data["gameVersion"] != "1.21" {
		t.Errorf("expected meta fields to be present, got %+v", resp.Data)
	}
}

func TestStatusHandler(t *testing.T) {
	setupServerDir(t)

	r := newTestRouter()
	r.GET("/status", StatusHandler)

	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body=%s", w.Code, w.Body.String())
	}

	var resp struct {
		Data map[string]any `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Data["running"] != false {
		t.Errorf("expected running=false, got %+v", resp.Data)
	}
}
