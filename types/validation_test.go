package types

import (
	"strings"
	"testing"
)

func TestValidateServerProperties_UnknownKeyIgnored(t *testing.T) {
	err := ValidateServerProperties(map[string]string{"some-unrecognized-key": "anything"})
	if err != nil {
		t.Errorf("expected no error for unknown key, got %v", err)
	}
}

func TestValidateServerProperties_EmptyMap(t *testing.T) {
	if err := ValidateServerProperties(map[string]string{}); err != nil {
		t.Errorf("expected no error for empty map, got %v", err)
	}
}

func TestValidateServerProperties_OneOfValid(t *testing.T) {
	cases := map[string]string{
		"gamemode":                  "survival",
		"difficulty":                "hard",
		"op-permission-level":       "4",
		"function-permission-level": "1",
	}
	for key, value := range cases {
		if err := ValidateServerProperties(map[string]string{key: value}); err != nil {
			t.Errorf("expected %q=%q to be valid, got error: %v", key, value, err)
		}
	}
}

func TestValidateServerProperties_OneOfInvalid(t *testing.T) {
	cases := map[string]string{
		"gamemode":                  "godmode",
		"difficulty":                "impossible",
		"op-permission-level":       "5",
		"function-permission-level": "0",
	}
	for key, value := range cases {
		err := ValidateServerProperties(map[string]string{key: value})
		if err == nil {
			t.Errorf("expected %q=%q to be invalid, got no error", key, value)
			continue
		}
		if !strings.Contains(err.Error(), key) {
			t.Errorf("expected error to mention key %q, got %v", key, err)
		}
	}
}

func TestValidateServerProperties_BoolValid(t *testing.T) {
	for _, value := range []string{"true", "false"} {
		if err := ValidateServerProperties(map[string]string{"pvp": value}); err != nil {
			t.Errorf("expected pvp=%q to be valid, got error: %v", value, err)
		}
	}
}

func TestValidateServerProperties_BoolInvalid(t *testing.T) {
	invalidValues := []string{"yes", "1", "True", ""}
	for _, value := range invalidValues {
		err := ValidateServerProperties(map[string]string{"pvp": value})
		if err == nil {
			t.Errorf("expected pvp=%q to be invalid, got no error", value)
		}
	}
}

func TestValidateServerProperties_MultipleKeysStopsAtFirstError(t *testing.T) {
	err := ValidateServerProperties(map[string]string{
		"white-list": "notabool",
	})
	if err == nil {
		t.Fatal("expected an error for invalid white-list value")
	}
	if !strings.Contains(err.Error(), "white-list") {
		t.Errorf("expected error to mention white-list, got %v", err)
	}
}

func TestValidateServerProperties_AllBoolKeysAcceptTrueFalse(t *testing.T) {
	boolKeys := []string{
		"enable-jmx-monitoring", "enable-command-block", "enable-query",
		"enforce-secure-profile", "pvp", "generate-structures",
		"require-resource-pack", "use-native-transport", "online-mode",
		"enable-status", "allow-flight", "broadcast-rcon-to-ops",
		"allow-nether", "enable-rcon", "sync-chunk-writes",
		"prevent-proxy-connections", "hide-online-players", "force-gamemode",
		"hardcore", "white-list", "broadcast-console-to-ops", "spawn-npcs",
		"spawn-animals", "log-ips", "spawn-monsters", "enforce-whitelist",
	}
	for _, key := range boolKeys {
		if err := ValidateServerProperties(map[string]string{key: "true"}); err != nil {
			t.Errorf("expected %q=true to be valid, got error: %v", key, err)
		}
		if err := ValidateServerProperties(map[string]string{key: "false"}); err != nil {
			t.Errorf("expected %q=false to be valid, got error: %v", key, err)
		}
		if err := ValidateServerProperties(map[string]string{key: "maybe"}); err == nil {
			t.Errorf("expected %q=maybe to be invalid", key)
		}
	}
}

func TestValidateEmail_EmptyClearsIsValid(t *testing.T) {
	if err := ValidateEmail(""); err != nil {
		t.Errorf("expected empty email to be valid (clears it), got %v", err)
	}
}

func TestValidateEmail_ValidAddress(t *testing.T) {
	if err := ValidateEmail("user@example.com"); err != nil {
		t.Errorf("expected valid email to pass, got %v", err)
	}
}

func TestValidateEmail_RejectsMalformed(t *testing.T) {
	cases := []string{"not-an-email", "missing-domain@", "@missing-local.com", "spaces in@example.com"}
	for _, email := range cases {
		if err := ValidateEmail(email); err == nil {
			t.Errorf("expected %q to be rejected as invalid", email)
		}
	}
}

func TestValidateEmail_RejectsDisplayNameSyntax(t *testing.T) {
	if err := ValidateEmail("Name <user@example.com>"); err == nil {
		t.Error("expected display-name-wrapped address to be rejected")
	}
}

func TestValidateEmail_RejectsTooLong(t *testing.T) {
	long := strings.Repeat("a", 250) + "@example.com"
	if err := ValidateEmail(long); err == nil {
		t.Error("expected an overly long email to be rejected")
	}
}

func TestValidatePassword_TooShort(t *testing.T) {
	if err := ValidatePassword("abc12"); err == nil {
		t.Error("expected a 5-character password to be rejected")
	}
}

func TestValidatePassword_MinimumLengthAccepted(t *testing.T) {
	if err := ValidatePassword("abc123"); err != nil {
		t.Errorf("expected a 6-character password to be valid, got %v", err)
	}
}
