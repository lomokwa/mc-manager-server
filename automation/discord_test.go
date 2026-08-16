package automation

import (
	"encoding/json"
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
