package types

import (
	"fmt"
	"net/mail"
	"strings"
)

var boolValues = map[string]bool{"true": true, "false": true}

var propertyRules = map[string]func(string) error{
	"gamemode":                  oneOf("survival", "creative", "adventure", "spectator"),
	"difficulty":                oneOf("peaceful", "easy", "normal", "hard"),
	"op-permission-level":       oneOf("1", "2", "3", "4"),
	"function-permission-level": oneOf("1", "2", "3", "4"),
	"enable-jmx-monitoring":     boolVal(),
	"enable-command-block":      boolVal(),
	"enable-query":              boolVal(),
	"enforce-secure-profile":    boolVal(),
	"pvp":                       boolVal(),
	"generate-structures":       boolVal(),
	"require-resource-pack":     boolVal(),
	"use-native-transport":      boolVal(),
	"online-mode":               boolVal(),
	"enable-status":             boolVal(),
	"allow-flight":              boolVal(),
	"broadcast-rcon-to-ops":     boolVal(),
	"allow-nether":              boolVal(),
	"enable-rcon":               boolVal(),
	"sync-chunk-writes":         boolVal(),
	"prevent-proxy-connections": boolVal(),
	"hide-online-players":       boolVal(),
	"force-gamemode":            boolVal(),
	"hardcore":                  boolVal(),
	"white-list":                boolVal(),
	"broadcast-console-to-ops":  boolVal(),
	"spawn-npcs":                boolVal(),
	"spawn-animals":             boolVal(),
	"log-ips":                   boolVal(),
	"spawn-monsters":            boolVal(),
	"enforce-whitelist":         boolVal(),
}

func ValidateServerProperties(properties map[string]string) error {
	for key, value := range properties {
		// server.properties is a line-oriented `key=value` file, so a newline
		// anywhere in a key or value doesn't just corrupt that entry -- it
		// injects whole new settings the caller never named. A value of
		// "10\nenable-rcon=true\nrcon.password=hunter2" would quietly open a
		// remote console. '=' inside a key breaks the round-trip the same way.
		// Only the rules below know each property's shape; this guard is what
		// keeps an unrecognised key (which those rules skip entirely) from
		// being a free write primitive into the file.
		if key == "" {
			return fmt.Errorf("property keys must not be empty")
		}
		if strings.ContainsAny(key, "=\r\n") || strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("invalid property %q: keys must not contain '=' and keys/values must not contain newlines", key)
		}
		if rule, exists := propertyRules[key]; exists {
			if err := rule(value); err != nil {
				return fmt.Errorf("invalid value for %q: %w", key, err)
			}
		}
	}
	return nil
}

func oneOf(allowed ...string) func(string) error {
	return func(value string) error {
		for _, a := range allowed {
			if value == a {
				return nil
			}
		}
		return fmt.Errorf("must be one of %v, got %q", allowed, value)
	}
}

func boolVal() func(string) error {
	return func(value string) error {
		if !boolValues[value] {
			return fmt.Errorf("must be \"true\" or \"false\", got %q", value)
		}
		return nil
	}
}

// ValidateDisplayName restricts profile display names to a reasonable length
// with no control characters, the same free-text-field guard as
// ValidateServerProperties above (a newline in a display name has no
// injection consequence here, but it renders badly and cheap to reject).
func ValidateDisplayName(name string) error {
	if len(name) > 32 {
		return fmt.Errorf("display name must be 32 characters or fewer")
	}
	if strings.ContainsAny(name, "\r\n\t") {
		return fmt.Errorf("display name must not contain control characters")
	}
	return nil
}

// ValidateEmail allows a caller to clear their email (empty string) or set
// it to a syntactically valid address. Uses net/mail's parser rather than a
// hand-rolled regex since it already implements RFC 5322 address parsing.
func ValidateEmail(email string) error {
	if email == "" {
		return nil
	}
	if len(email) > 255 {
		return fmt.Errorf("email must be 255 characters or fewer")
	}
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email {
		return fmt.Errorf("invalid email address")
	}
	return nil
}

// ValidatePassword enforces the same minimum length the frontend already
// checks at registration, so the API rejects a weak password even when
// called directly rather than through the client's form.
func ValidatePassword(password string) error {
	if len(password) < 6 {
		return fmt.Errorf("password must be at least 6 characters")
	}
	return nil
}
