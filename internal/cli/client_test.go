package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestServerURLDefaultsToOfficialEndpoint(t *testing.T) {
	t.Setenv("SNAPIFACT_SERVER", "")
	if got := ServerURL(); got != "https://api.snapifact.dev" {
		t.Fatalf("ServerURL() = %q, want %q", got, "https://api.snapifact.dev")
	}
}

func TestCreateSnapshotUsesVerbatimAPIKeyAndParsesTier(t *testing.T) {
	const apiKey = "key with spaces"
	var gotKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-Snapifact-API-Key")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"id":           "kpm2q6xxyegw5czekhga",
			"url":          "https://view.test/v/kpm2q6xxyegw5czekhga",
			"expires_at":   "2026-08-13T00:00:00Z",
			"delete_token": strings.Repeat("A", 43),
			"tier":         "basic",
		})
	}))
	defer server.Close()
	t.Setenv("SNAPIFACT_API_KEY", apiKey)

	response, err := CreateSnapshot(server.URL, "title", "content")
	if err != nil {
		t.Fatal(err)
	}
	if gotKey != apiKey {
		t.Fatalf("API key header = %q, want %q", gotKey, apiKey)
	}
	if response.Tier != "basic" {
		t.Fatalf("tier = %q, want basic", response.Tier)
	}
}

func TestCreateSnapshotOmitsAPIKeyWhenUnset(t *testing.T) {
	var gotKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-Snapifact-API-Key")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"kpm2q6xxyegw5czekhga","url":"https://view.test/v/kpm2q6xxyegw5czekhga","expires_at":"2026-08-13T00:00:00Z","delete_token":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}`))
	}))
	defer server.Close()
	t.Setenv("SNAPIFACT_API_KEY", "")

	if _, err := CreateSnapshot(server.URL, "title", "content"); err != nil {
		t.Fatal(err)
	}
	if gotKey != "" {
		t.Fatalf("API key header = %q, want absent", gotKey)
	}
}
