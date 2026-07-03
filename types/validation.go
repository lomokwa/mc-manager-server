package types

import (
	"fmt"
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
		// server.properties is line-oriented `key=value`: a newline in a key
		// or value would inject extra lines (e.g. enable RCON or point at a
		// malicious resource pack), and '=' in a key corrupts the round-trip.
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
