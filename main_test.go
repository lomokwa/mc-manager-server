package main

import (
	"reflect"
	"testing"
)

func TestAllowedOrigins_DefaultsWhenUnset(t *testing.T) {
	t.Setenv("CORS_ALLOWED_ORIGINS", "")

	got := allowedOrigins()
	want := []string{"http://localhost:5173", "http://localhost:8080"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("expected %+v, got %+v", want, got)
	}
}

func TestAllowedOrigins_DefaultsWhenWhitespaceOnly(t *testing.T) {
	t.Setenv("CORS_ALLOWED_ORIGINS", "   ")

	got := allowedOrigins()
	want := []string{"http://localhost:5173", "http://localhost:8080"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("expected %+v, got %+v", want, got)
	}
}

func TestAllowedOrigins_ParsesCommaSeparatedList(t *testing.T) {
	t.Setenv("CORS_ALLOWED_ORIGINS", "https://a.example.com,https://b.example.com")

	got := allowedOrigins()
	want := []string{"https://a.example.com", "https://b.example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("expected %+v, got %+v", want, got)
	}
}

func TestAllowedOrigins_TrimsWhitespaceAndSkipsEmptyEntries(t *testing.T) {
	t.Setenv("CORS_ALLOWED_ORIGINS", " https://a.example.com , , https://b.example.com ")

	got := allowedOrigins()
	want := []string{"https://a.example.com", "https://b.example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("expected %+v, got %+v", want, got)
	}
}
