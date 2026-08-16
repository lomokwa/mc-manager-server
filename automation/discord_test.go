package automation

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// captureWebhook stands in for Discord and hands back whatever payload it was
// sent, so the guard can be checked on the wire rather than by reading code.
func captureWebhook(t *testing.T, status int, body string) (*httptest.Server, func() map[string]any) {
	t.Helper()
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		w.WriteHeader(status)
		if body != "" {
			_, _ = w.Write([]byte(body))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() map[string]any { return got }
}

// A player typing "@everyone" in chat must not become a server-wide ping when
// a rule relays it. The Discord bot in this project already shipped this bug
// once (its PR #8).
func TestDiscord_AlwaysConstrainsMentions(t *testing.T) {
	srv, payload := captureWebhook(t, http.StatusNoContent, "")

	if _, err := PostDiscord(srv.URL, "chat: @everyone venham ver", ""); err != nil {
		t.Fatalf("PostDiscord: %v", err)
	}

	am, ok := payload()["allowed_mentions"].(map[string]any)
	if !ok {
		t.Fatal("every payload must carry allowed_mentions")
	}
	parse, _ := am["parse"].([]any)
	if len(parse) != 0 {
		t.Errorf("with no mention configured, parse must be empty, got %v", parse)
	}
	if _, hasRoles := am["roles"]; hasRoles {
		t.Error("no mention was configured, so no role should be allowed")
	}
}

func TestDiscord_AllowsExactlyTheConfiguredRole(t *testing.T) {
	srv, payload := captureWebhook(t, http.StatusNoContent, "")

	if _, err := PostDiscord(srv.URL, "servidor caiu", "<@&123456>"); err != nil {
		t.Fatalf("PostDiscord: %v", err)
	}

	am := payload()["allowed_mentions"].(map[string]any)
	roles, _ := am["roles"].([]any)
	if len(roles) != 1 || roles[0] != "123456" {
		t.Errorf("expected exactly the configured role to be allowed, got %v", roles)
	}
	if content, _ := payload()["content"].(string); !strings.HasPrefix(content, "<@&123456>") {
		t.Errorf("expected the mention to lead the message, got %q", content)
	}
}

func TestDiscord_AllowsExactlyTheConfiguredUser(t *testing.T) {
	srv, payload := captureWebhook(t, http.StatusNoContent, "")

	if _, err := PostDiscord(srv.URL, "olha isso", "<@987654>"); err != nil {
		t.Fatalf("PostDiscord: %v", err)
	}

	am := payload()["allowed_mentions"].(map[string]any)
	users, _ := am["users"].([]any)
	if len(users) != 1 || users[0] != "987654" {
		t.Errorf("expected exactly the configured user to be allowed, got %v", users)
	}
	if _, hasRoles := am["roles"]; hasRoles {
		t.Error("a user mention must not allow roles")
	}
}

// The dangerous combination: an admin configures a role mention, AND a player
// types @everyone in chat. The configured role must go through; the player's
// text must not.
func TestDiscord_AConfiguredMentionDoesNotUnlockPlayerText(t *testing.T) {
	srv, payload := captureWebhook(t, http.StatusNoContent, "")

	if _, err := PostDiscord(srv.URL, "chat: @everyone @here venham", "<@&123456>"); err != nil {
		t.Fatalf("PostDiscord: %v", err)
	}

	am := payload()["allowed_mentions"].(map[string]any)
	parse, _ := am["parse"].([]any)
	if len(parse) != 0 {
		t.Errorf("parse must stay empty so @everyone in player text is inert, got %v", parse)
	}
	roles, _ := am["roles"].([]any)
	if len(roles) != 1 {
		t.Errorf("the configured role should still be allowed, got %v", roles)
	}
}

func TestDiscord_ReportsTheRealStatus(t *testing.T) {
	srv, _ := captureWebhook(t, http.StatusUnauthorized, `{"message":"Invalid Webhook Token"}`)

	status, err := PostDiscord(srv.URL, "oi", "")
	if status != http.StatusUnauthorized {
		t.Errorf("expected the real status to come back, got %d", status)
	}
	if err == nil {
		t.Fatal("a 401 must be an error, never a silent success")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("the error should name the status, got %q", err)
	}
}

// The URL is a credential and errors get logged. It must not travel inside
// one.
func TestDiscord_ErrorNeverContainsTheURL(t *testing.T) {
	srv, _ := captureWebhook(t, http.StatusForbidden, "")
	secret := srv.URL + "/SUPERSECRETTOKEN"

	_, err := PostDiscord(secret, "oi", "")
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "SUPERSECRETTOKEN") {
		t.Errorf("the webhook URL leaked into an error: %q", err)
	}
}

// An unreachable host is the other path that carries a URL in the stdlib's
// own error text.
func TestDiscord_UnreachableHostDoesNotLeakTheURL(t *testing.T) {
	_, err := PostDiscord("http://127.0.0.1:1/SUPERSECRETTOKEN", "oi", "")
	if err == nil {
		t.Fatal("expected an error reaching a dead port")
	}
	if strings.Contains(err.Error(), "SUPERSECRETTOKEN") {
		t.Errorf("the webhook URL leaked into a transport error: %q", err)
	}
}

func TestDiscord_MalformedURLDoesNotLeakItEither(t *testing.T) {
	_, err := PostDiscord("://SUPERSECRETTOKEN", "oi", "")
	if err == nil {
		t.Fatal("expected an error for a malformed URL")
	}
	if strings.Contains(err.Error(), "SUPERSECRETTOKEN") {
		t.Errorf("the webhook URL leaked into a parse error: %q", err)
	}
}

// The refusal string is what an admin reads off a list row. Discord wraps its
// one useful sentence in JSON, and showing the wrapper is most of the way to
// not showing the sentence.
func TestExplain_PullsDiscordsSentenceOutOfItsJSON(t *testing.T) {
	got := explain([]byte(`{"message": "Invalid Webhook Token", "code": 50027}`))
	if got != "Invalid Webhook Token" {
		t.Errorf("got %q", got)
	}
}

// Anything that is not that shape is passed through. Guessing further would
// risk hiding the one line that explains the failure.
func TestExplain_PassesThroughAnythingElse(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"<html>502 Bad Gateway</html>", "<html>502 Bad Gateway</html>"},
		{"  rate limited  ", "rate limited"},
		{`{"code": 50027}`, `{"code": 50027}`},
		{`{"message": "   "}`, `{"message": "   "}`},
		{"", ""},
	} {
		if got := explain([]byte(tc.in)); got != tc.want {
			t.Errorf("explain(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The gap the other three leave open. They prove the URL does not come from Go
// -- transport errors, parse errors, our own wording -- and all of them answer
// with an EMPTY body, so none of them exercises the path where the response
// body itself becomes the error string.
//
// That path is real. A proxy, a captive portal or a WAF sitting in front of an
// outbound request routinely echoes the URL it refused, and that string is
// stored in last_error, rendered on the destinations list, and logged. The one
// value in this feature that must never be written down would then be written
// down three times, by a component nobody here controls.
func TestDiscord_ARefusalBodyThatEchoesTheURLIsRedacted(t *testing.T) {
	var url string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		// What a proxy actually does: repeat the request line back.
		fmt.Fprintf(w, "Cannot POST %s", url)
	}))
	t.Cleanup(srv.Close)
	url = srv.URL + "/1234/SUPERSECRETTOKEN"

	_, err := PostDiscord(url, "oi", "")
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "SUPERSECRETTOKEN") {
		t.Errorf("a refusal body carried the webhook token into the error: %q", err)
	}
	// The status still has to survive, or redacting has cost the admin the one
	// fact that says what went wrong.
	if !strings.Contains(err.Error(), "502") {
		t.Errorf("the status was lost along with the URL: %q", err)
	}
}

// The token alone, without the scheme and host, is still the secret half.
func TestDiscord_ARefusalBodyEchoingOnlyThePathIsRedacted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, "no route for %s", r.URL.Path)
	}))
	t.Cleanup(srv.Close)

	_, err := PostDiscord(srv.URL+"/1234/SUPERSECRETTOKEN", "oi", "")
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "SUPERSECRETTOKEN") {
		t.Errorf("a refusal body carried the token into the error: %q", err)
	}
}

// redactURL replaces the last path segment as loose text, and a short segment
// is a substring of ordinary words. The URL validator accepts a one-character
// token ([\w-]+ has no minimum), so this is reachable, and the damage lands on
// the sentence the admin is trying to read.
func TestRedactURL_DoesNotMangleTheMessageForAShortToken(t *testing.T) {
	got := redactURL("Cannot POST, and the account is inactive", "https://discord.com/api/webhooks/1/a")
	if strings.Contains(got, "[destino]") {
		t.Errorf("a one-character token ate letters out of the message: %q", got)
	}
	if got != "Cannot POST, and the account is inactive" {
		t.Errorf("the message was altered: %q", got)
	}
}

// The real thing still has to go. Discord's tokens are long, which is exactly
// what makes them safe to match as loose text.
func TestRedactURL_StillRemovesARealToken(t *testing.T) {
	const url = "https://discord.com/api/webhooks/123456789/abcdefGHIJK-lmnop_123"
	for _, body := range []string{
		"Cannot POST " + url,
		"no route for /api/webhooks/123456789/abcdefGHIJK-lmnop_123",
		"unknown token abcdefGHIJK-lmnop_123",
	} {
		got := redactURL(body, url)
		if strings.Contains(got, "abcdefGHIJK-lmnop_123") {
			t.Errorf("the token survived redaction in %q -> %q", body, got)
		}
	}
}
