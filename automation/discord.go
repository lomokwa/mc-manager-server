package automation

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"regexp"
	"strings"
	"time"
)

// discordClient has a timeout because a rule can be the first step of a chain
// that restarts a server: a webhook hanging must not hold the rest hostage.
var discordClient = &http.Client{Timeout: 10 * time.Second}

// mentionRe pulls the numeric id out of <@&123> (role) or <@123> / <@!123>
// (user), which is how a configured mention becomes an allow-list entry.
var mentionRe = regexp.MustCompile(`<@([&!]?)(\d+)>`)

// discordPayload always sets allowed_mentions, and always with an empty parse
// list.
//
// Message text can contain {line}, which is written by a player when the
// trigger is a chat message. An empty parse list is what makes an "@everyone"
// typed in chat inert: only ids named here can ping, and the only id that gets
// named is the one the RULE configured. The Discord bot in this project
// shipped the unguarded version once (its PR #8).
func discordPayload(content, mention string) map[string]any {
	allowed := map[string]any{"parse": []string{}}

	if m := mentionRe.FindStringSubmatch(mention); m != nil {
		if m[1] == "&" {
			allowed["roles"] = []string{m[2]}
		} else {
			allowed["users"] = []string{m[2]}
		}
		content = mention + " " + content
	}
	return map[string]any{"content": content, "allowed_mentions": allowed}
}

// PostDiscord delivers one message and returns the real HTTP status. A 401
// comes back as an error so the UI can show it instead of a false green.
//
// No error returned from here contains the URL. It is a credential, errors get
// logged, and both net/http's transport errors and url.Parse's errors embed
// the URL by default -- so every path builds its own message rather than
// wrapping theirs.
func PostDiscord(url, content, mention string) (int, error) {
	body, err := json.Marshal(discordPayload(content, mention))
	if err != nil {
		return 0, fmt.Errorf("encode webhook payload: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("invalid webhook URL")
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := discordClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("could not reach Discord")
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp.StatusCode, nil
	}

	// Discord explains refusals in the body ("Invalid Webhook Token"), which is
	// the part that tells an admin what to fix. Bounded, because an error
	// string ends up in the database and on screen.
	detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	msg := redactURL(explain(detail), url)
	if msg == "" {
		return resp.StatusCode, fmt.Errorf("webhook rejected with %d", resp.StatusCode)
	}
	return resp.StatusCode, fmt.Errorf("webhook rejected with %d: %s", resp.StatusCode, msg)
}

// explain pulls the human sentence out of a refusal body.
//
// Discord answers with {"message": "Invalid Webhook Token", "code": 50027}, and
// this string is what an admin reads off a list row. Showing the raw JSON puts
// the answer on screen wrapped in punctuation and a number that means nothing
// to them -- which is most of the way to not showing it.
//
// Anything that is not that shape is returned as-is: a proxy's HTML, a plain
// sentence, an empty body. Guessing further would risk hiding the one line
// that explains the failure.
// redactURL removes the destination from text that came back from the network.
//
// Every other guard in this file stops OUR code from writing the URL down. This
// one stops someone else's: a proxy, a captive portal or a WAF in front of an
// outbound request routinely echoes the request line it refused, and that text
// becomes the error we store in last_error, render on the destinations list,
// and log. The one value in this feature that must never be written down would
// be written down three times, by a component nobody here controls.
//
// The path is redacted as well as the whole URL, because "no route for
// /1234/TOKEN" gives away the secret half without the scheme and host. Query
// and fragment are dropped first so a URL echoed back with either still
// matches. The status code survives -- redacting is not worth costing the admin
// the one fact that says what went wrong.
func redactURL(text, raw string) string {
	if text == "" {
		return text
	}
	text = strings.ReplaceAll(text, raw, "[destino]")
	u, err := neturl.Parse(raw)
	if err != nil || u.Path == "" || u.Path == "/" {
		return text
	}
	text = strings.ReplaceAll(text, u.Path, "[destino]")

	// The token on its own, which is the half that matters -- but only when it
	// is long enough to actually be one. This is a loose substring match, and a
	// short segment is a substring of ordinary words: the URL validator has no
	// minimum length ([\w-]+), so a token of "a" would turn "Cannot POST, and
	// the account is inactive" into "C[destino]nnot POST, [destino]nd the
	// [destino]ccount is in[destino]ctive" and destroy the sentence the admin
	// is trying to read. Anything that short is not a credential worth the
	// damage, and the full-path replacement above already covers it in the one
	// place it appears.
	if i := strings.LastIndex(u.Path, "/"); i >= 0 {
		if token := u.Path[i+1:]; len(token) >= minTokenLen {
			text = strings.ReplaceAll(text, token, "[destino]")
		}
	}
	return text
}

// minTokenLen is where a path segment stops being a word and starts being a
// secret. Discord's webhook tokens are 60+ characters; this is far below that
// and far above anything that collides with prose.
const minTokenLen = 12

func explain(body []byte) string {
	var d struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &d); err == nil && strings.TrimSpace(d.Message) != "" {
		return strings.TrimSpace(d.Message)
	}
	return strings.TrimSpace(string(body))
}
