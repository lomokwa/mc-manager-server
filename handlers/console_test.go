package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestConsoleHandler_ServerNotRunning covers the only branch of ConsoleHandler
// reachable without a real Minecraft/mc-supervisor process and a live
// WebSocket connection: it never gets to upgrade.Upgrade in this case.
func TestConsoleHandler_ServerNotRunning(t *testing.T) {
	setupServerDir(t)

	r := newTestRouter()
	r.GET("/console", ConsoleHandler)

	req := httptest.NewRequest(http.MethodGet, "/console", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d, body=%s", w.Code, w.Body.String())
	}
}
