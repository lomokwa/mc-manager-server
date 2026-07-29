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

func TestListBackupsHandler_Empty(t *testing.T) {
	os.RemoveAll(services.BackupDir)
	t.Cleanup(func() { os.RemoveAll(services.BackupDir) })

	r := newTestRouter()
	r.GET("/backups", ListBackupsHandler)

	req := httptest.NewRequest(http.MethodGet, "/backups", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body=%s", w.Code, w.Body.String())
	}

	var resp struct {
		Data []types.BackupInfo `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Data) != 0 {
		t.Errorf("expected empty backup list, got %+v", resp.Data)
	}
}

func TestListBackupsHandler_WithBackups(t *testing.T) {
	dir := setupBackupDir(t)
	os.WriteFile(dir+"/world-2024-01-01T00-00-00Z.zip", []byte("a"), 0644)
	os.WriteFile(dir+"/world-2024-01-02T00-00-00Z.zip", []byte("b"), 0644)

	newer := time.Now()
	older := newer.Add(-time.Hour)
	os.Chtimes(dir+"/world-2024-01-01T00-00-00Z.zip", older, older)
	os.Chtimes(dir+"/world-2024-01-02T00-00-00Z.zip", newer, newer)

	r := newTestRouter()
	r.GET("/backups", ListBackupsHandler)

	req := httptest.NewRequest(http.MethodGet, "/backups", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body=%s", w.Code, w.Body.String())
	}

	var resp struct {
		Data []types.BackupInfo `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Data) != 2 {
		t.Fatalf("expected 2 backups, got %d", len(resp.Data))
	}
	if resp.Data[0].Name != "world-2024-01-02T00-00-00Z.zip" {
		t.Errorf("expected newest backup first, got %+v", resp.Data)
	}
}

func TestDeleteBackupHandler_MissingName(t *testing.T) {
	setupBackupDir(t)

	r := newTestRouter()
	r.DELETE("/backups", DeleteBackupHandler)

	req := httptest.NewRequest(http.MethodDelete, "/backups", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d, body=%s", w.Code, w.Body.String())
	}
}

func TestDeleteBackupHandler_InvalidName(t *testing.T) {
	setupBackupDir(t)

	r := newTestRouter()
	r.DELETE("/backups", DeleteBackupHandler)

	req := httptest.NewRequest(http.MethodDelete, "/backups?name=../../etc/passwd", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d, body=%s", w.Code, w.Body.String())
	}
}

func TestDeleteBackupHandler_NotFound(t *testing.T) {
	setupBackupDir(t)

	r := newTestRouter()
	r.DELETE("/backups", DeleteBackupHandler)

	req := httptest.NewRequest(http.MethodDelete, "/backups?name=world-2024-01-01T00-00-00Z.zip", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d, body=%s", w.Code, w.Body.String())
	}
}

func TestDeleteBackupHandler_Success(t *testing.T) {
	dir := setupBackupDir(t)
	name := "world-2024-01-01T00-00-00Z.zip"
	os.WriteFile(dir+"/"+name, []byte("a"), 0644)

	r := newTestRouter()
	r.DELETE("/backups", DeleteBackupHandler)

	req := httptest.NewRequest(http.MethodDelete, "/backups?name="+name, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body=%s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(dir + "/" + name); !os.IsNotExist(err) {
		t.Error("expected backup file to be removed")
	}
}

func TestDownloadBackupHandler_MissingName(t *testing.T) {
	setupBackupDir(t)

	r := newTestRouter()
	r.GET("/backups/download", DownloadBackupHandler)

	req := httptest.NewRequest(http.MethodGet, "/backups/download", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d, body=%s", w.Code, w.Body.String())
	}
}

func TestDownloadBackupHandler_NotFound(t *testing.T) {
	setupBackupDir(t)

	r := newTestRouter()
	r.GET("/backups/download", DownloadBackupHandler)

	req := httptest.NewRequest(http.MethodGet, "/backups/download?name=world-2024-01-01T00-00-00Z.zip", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d, body=%s", w.Code, w.Body.String())
	}
}

func TestDownloadBackupHandler_Success(t *testing.T) {
	dir := setupBackupDir(t)
	name := "world-2024-01-01T00-00-00Z.zip"
	os.WriteFile(dir+"/"+name, []byte("zip-content"), 0644)

	r := newTestRouter()
	r.GET("/backups/download", DownloadBackupHandler)

	req := httptest.NewRequest(http.MethodGet, "/backups/download?name="+name, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body=%s", w.Code, w.Body.String())
	}
	if w.Body.String() != "zip-content" {
		t.Errorf("expected downloaded content, got %q", w.Body.String())
	}
}

func TestRestoreBackupHandler_InvalidBody(t *testing.T) {
	setupBackupDir(t)

	r := newTestRouter()
	r.POST("/backups/restore", RestoreBackupHandler)

	req := httptest.NewRequest(http.MethodPost, "/backups/restore", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d, body=%s", w.Code, w.Body.String())
	}
}

func TestRestoreBackupHandler_NotFound(t *testing.T) {
	setupServerDir(t)
	setupBackupDir(t)

	r := newTestRouter()
	r.POST("/backups/restore", RestoreBackupHandler)

	req := httptest.NewRequest(http.MethodPost, "/backups/restore", strings.NewReader(`{"name": "world-2024-01-01T00-00-00Z.zip"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d, body=%s", w.Code, w.Body.String())
	}
}

// TestCreateAndRestoreBackup_RoundTrip exercises CreateBackupHandler and
// RestoreBackupHandler together against real zip files (no mocking), the
// only supported way to invoke these handlers since ServerDir/BackupDir are
// fixed relative paths in the services package.
func TestCreateAndRestoreBackup_RoundTrip(t *testing.T) {
	setupServerDir(t)
	setupBackupDir(t)
	writeServerFile(t, "world/level.dat", "original world data")

	r := newTestRouter()
	r.POST("/backups", CreateBackupHandler)
	r.POST("/backups/restore", RestoreBackupHandler)

	// Create the backup.
	req := httptest.NewRequest(http.MethodPost, "/backups", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d, body=%s", w.Code, w.Body.String())
	}

	var createResp struct {
		Data types.BackupInfo `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &createResp)
	if createResp.Data.Name == "" {
		t.Fatal("expected a backup name in the response")
	}

	// Mutate the world to prove restore actually overwrites it.
	writeServerFile(t, "world/level.dat", "corrupted world data")

	// Restore the backup.
	restoreBody := `{"name": "` + createResp.Data.Name + `"}`
	req = httptest.NewRequest(http.MethodPost, "/backups/restore", strings.NewReader(restoreBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body=%s", w.Code, w.Body.String())
	}

	restored, err := os.ReadFile(services.ServerDir + "/world/level.dat")
	if err != nil {
		t.Fatalf("failed to read restored world file: %v", err)
	}
	if string(restored) != "original world data" {
		t.Errorf("expected restored content, got %q", restored)
	}
}

func TestGetBackupConfigHandler_Defaults(t *testing.T) {
	setupTestDB(t)

	r := newTestRouter()
	r.GET("/backups/config", GetBackupConfigHandler)

	req := httptest.NewRequest(http.MethodGet, "/backups/config", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body=%s", w.Code, w.Body.String())
	}

	var resp struct {
		Data types.BackupConfig `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Data.Enabled || resp.Data.IntervalMinutes != 1440 || resp.Data.Keep != 7 {
		t.Errorf("unexpected defaults: %+v", resp.Data)
	}
}

func TestUpdateBackupConfigHandler_InvalidBody(t *testing.T) {
	setupTestDB(t)

	r := newTestRouter()
	r.PUT("/backups/config", UpdateBackupConfigHandler)

	req := httptest.NewRequest(http.MethodPut, "/backups/config", strings.NewReader(`{not json`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d, body=%s", w.Code, w.Body.String())
	}
}

func TestUpdateBackupConfigHandler_InvalidConfig(t *testing.T) {
	setupTestDB(t)

	r := newTestRouter()
	r.PUT("/backups/config", UpdateBackupConfigHandler)

	req := httptest.NewRequest(http.MethodPut, "/backups/config", strings.NewReader(`{"enabled": true, "interval_minutes": 0, "keep": 7}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d, body=%s", w.Code, w.Body.String())
	}
}

func TestUpdateBackupConfigHandler_Success(t *testing.T) {
	setupTestDB(t)

	r := newTestRouter()
	r.PUT("/backups/config", UpdateBackupConfigHandler)
	r.GET("/backups/config", GetBackupConfigHandler)

	body := `{"enabled": true, "interval_minutes": 60, "keep": 3}`
	req := httptest.NewRequest(http.MethodPut, "/backups/config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body=%s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/backups/config", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var resp struct {
		Data types.BackupConfig `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if !resp.Data.Enabled || resp.Data.IntervalMinutes != 60 || resp.Data.Keep != 3 {
		t.Errorf("unexpected saved config: %+v", resp.Data)
	}
}
