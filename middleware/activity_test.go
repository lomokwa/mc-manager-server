package middleware

import (
	"testing"

	"github.com/lomokwa/mc-manager/types"
)

func TestClassifyRequest_ReadsAreNeverRecorded(t *testing.T) {
	// The panel polls /api/status every 5s per open tab. Recording reads would
	// bury the handful of rows that matter under thousands of them.
	for _, method := range []string{"GET", "HEAD", "OPTIONS"} {
		if !isReadOnly(method) {
			t.Errorf("%s should be treated as read-only", method)
		}
	}
	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		if isReadOnly(method) {
			t.Errorf("%s is a mutation and must be recorded", method)
		}
	}
}

func TestClassifyRequest_MapsTheActionsWorthAuditing(t *testing.T) {
	cases := []struct {
		method, route      string
		category, contains string
	}{
		{"POST", "/api/start", types.ActivityServer, "started"},
		{"POST", "/api/stop", types.ActivityServer, "stopped"},
		{"DELETE", "/api/server", types.ActivityServer, "deleted"},
		{"PATCH", "/api/properties", types.ActivitySettings, "settings"},
		{"PUT", "/api/files", types.ActivityFiles, "edited"},
		{"DELETE", "/api/files", types.ActivityFiles, "deleted"},
		{"POST", "/api/files/upload", types.ActivityFiles, "uploaded"},
		{"POST", "/api/backups", types.ActivityBackups, "created"},
		{"POST", "/api/backups/restore", types.ActivityBackups, "restored"},
		{"PUT", "/api/users/:id/role", types.ActivityAccess, "role"},
		{"PUT", "/api/users/:id/overrides", types.ActivityAccess, "permissions"},
		{"POST", "/api/admin/invitations", types.ActivityAccess, "invitation"},
	}
	for _, tc := range cases {
		category, action := classifyRequest(tc.method, tc.route)
		if category != tc.category {
			t.Errorf("%s %s: expected category %q, got %q", tc.method, tc.route, tc.category, category)
		}
		if action == "" {
			t.Errorf("%s %s: expected an action", tc.method, tc.route)
		}
	}
}

// A namespaced route is the same action as its flat twin -- which server it
// applied to is carried separately in server_id, not smuggled into the label.
func TestClassifyRequest_NamespacedMatchesFlat(t *testing.T) {
	for _, pair := range [][2]string{
		{"/api/start", "/api/servers/:sid/start"},
		{"/api/files", "/api/servers/:sid/files"},
		{"/api/backups/restore", "/api/servers/:sid/backups/restore"},
	} {
		flatCat, flatAction := classifyRequest("POST", pair[0])
		nsCat, nsAction := classifyRequest("POST", pair[1])
		if flatCat != nsCat || flatAction != nsAction {
			t.Errorf("%s vs %s: expected identical classification, got (%q,%q) vs (%q,%q)",
				pair[0], pair[1], flatCat, flatAction, nsCat, nsAction)
		}
	}
}

// Unmapped routes are absent rather than mislabelled: adding one is a
// deliberate edit here, not something that happens by accident.
func TestClassifyRequest_UnmappedRoutesAreNotRecorded(t *testing.T) {
	for _, route := range []string{"/api/login", "/api/register", "/healthz", "/api/whatever-ships-next"} {
		if _, action := classifyRequest("POST", route); action != "" {
			t.Errorf("%s should not be recorded until it is deliberately mapped, got %q", route, action)
		}
	}
}

// The one that would turn the audit table into a breach. The API key travels as
// ?key= and the console JWT as ?token=, so the detail is built from the route
// PATTERN and can never carry a query string -- no redaction to get wrong.
func TestClassifyRequest_NeverDerivesDetailFromARealURL(t *testing.T) {
	// A route pattern has placeholders, never values, and no query.
	_, action := classifyRequest("PUT", "/api/users/:id/role")
	if action == "" {
		t.Fatal("expected this route to be recorded")
	}
	// If someone ever passes a real URL here, it must not classify -- which
	// keeps a credential-bearing string from reaching the detail column.
	for _, url := range []string{
		"/api/files?path=world&key=supersecret",
		"/api/console?token=eyJhbGci.SECRET.sig",
	} {
		if _, a := classifyRequest("PUT", url); a != "" {
			t.Errorf("a URL with a query must not classify (it would be stored verbatim): %q -> %q", url, a)
		}
	}
}
