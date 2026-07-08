package handlers

import (
	"os"
	"testing"
)

func TestOriginAllowed(t *testing.T) {
	// Default (no CORS_ALLOWED_ORIGINS): only the local dev origins.
	os.Unsetenv("CORS_ALLOWED_ORIGINS")
	if !originAllowed("http://localhost:5173") {
		t.Error("default should allow http://localhost:5173")
	}
	if originAllowed("http://evil.example") {
		t.Error("default should reject http://evil.example")
	}

	// Configured allow-list.
	os.Setenv("CORS_ALLOWED_ORIGINS", "https://app.example.com, http://localhost:5173")
	defer os.Unsetenv("CORS_ALLOWED_ORIGINS")
	if !originAllowed("https://app.example.com") {
		t.Error("should allow a configured origin")
	}
	if originAllowed("http://evil.example") {
		t.Error("should reject an origin that isn't configured")
	}
}
