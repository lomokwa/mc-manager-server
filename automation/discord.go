package automation

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	msg := strings.TrimSpace(string(detail))
	if msg == "" {
		return resp.StatusCode, fmt.Errorf("webhook rejected with %d", resp.StatusCode)
	}
	return resp.StatusCode, fmt.Errorf("webhook rejected with %d: %s", resp.StatusCode, msg)
}
