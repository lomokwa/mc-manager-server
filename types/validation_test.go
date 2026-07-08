package types

import "testing"

func TestValidateServerProperties_RejectsInjection(t *testing.T) {
	cases := map[string]map[string]string{
		"newline in value": {"motd": "hello\nrcon.password=pwned"},
		"newline in key":   {"bad\nkey": "x"},
		"CR in value":      {"motd": "hello\rrcon.password=pwned"},
		"'=' in key":       {"bad=key": "x"},
		"empty key":        {"": "x"},
	}
	for name, props := range cases {
		if err := ValidateServerProperties(props); err == nil {
			t.Errorf("%s: expected an error, got nil", name)
		}
	}
}

func TestValidateServerProperties_AllowsValidProperties(t *testing.T) {
	props := map[string]string{
		"motd":       "Hello World",
		"pvp":        "true",
		"difficulty": "hard",
		"custom-key": "custom value",
	}
	if err := ValidateServerProperties(props); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateServerProperties_RejectsBadKnownValues(t *testing.T) {
	if err := ValidateServerProperties(map[string]string{"pvp": "yes"}); err == nil {
		t.Error("expected an error for a non-boolean pvp value")
	}
	if err := ValidateServerProperties(map[string]string{"difficulty": "impossible"}); err == nil {
		t.Error("expected an error for an unknown difficulty")
	}
}
