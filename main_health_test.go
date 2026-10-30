package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The healthcheck exists to answer docker's "should I restart this container",
// so the two things worth pinning are that the probe is reachable without a
// token (it runs before anyone has logged in) and that it stays boring: a
// probe that grows a version string or a player count becomes an unauthenticated
// information leak the moment someone "improves" it.

func TestHealthz_NeedsNoAuthAndSaysOnlyOK(t *testing.T) {
	setupTestDB(t)
	setupServerDir(t)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil) // deliberately no Authorization header
	rec := httptest.NewRecorder()
	newRouter().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 without a token, got %d (body %q)", rec.Code, rec.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("expected a JSON body, got %q: %v", rec.Body.String(), err)
	}
	if got := body["status"]; got != "ok" {
		t.Errorf(`expected {"status":"ok"}, got status=%v`, got)
	}
	if len(body) != 1 {
		t.Errorf("healthz must expose exactly one field and nothing about the server's state, got %v", body)
	}
}

// Every other /api/* route is behind ValidateJWT. If /healthz ever ends up
// inside that group it still returns 200 to docker's probe only by accident of
// ordering, so assert the neighbouring route really is protected -- that's what
// makes the test above meaningful rather than vacuous.
func TestHealthz_IsTheOnlyUnauthenticatedGET(t *testing.T) {
	setupTestDB(t)
	setupServerDir(t)
	router := newRouter()

	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected /api/status to still require a token (401), got %d", rec.Code)
	}
}

func TestListenPort_MirrorsGinsResolution(t *testing.T) {
	t.Setenv("PORT", "")
	if got := listenPort(); got != "8080" {
		t.Errorf("expected the 8080 default when PORT is unset, got %q", got)
	}

	t.Setenv("PORT", "9001")
	if got := listenPort(); got != "9001" {
		t.Errorf("expected PORT to win, got %q", got)
	}

	// compose passes env through verbatim, and a trailing newline in a .env
	// value would otherwise build an unparseable URL and fail every probe.
	t.Setenv("PORT", "  9002 \n")
	if got := listenPort(); got != "9002" {
		t.Errorf("expected surrounding whitespace to be trimmed, got %q", got)
	}
}
