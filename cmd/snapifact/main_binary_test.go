package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCompiledCLIUsesCreateAPIKeyHeader(t *testing.T) {
	const (
		apiKey = "compiled-cli-key"
		id     = "kpm2q6xxyegw5czekhga"
		token  = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	)
	var gotKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-Snapifact-API-Key")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"id": id, "url": "https://view.test/v/" + id,
			"expires_at": "2026-08-13T00:00:00Z", "delete_token": token, "tier": "basic",
		})
	}))
	defer server.Close()

	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "../.."))
	binary := filepath.Join(t.TempDir(), "snapifact")
	build := exec.Command("go", "build", "-o", binary, "./cmd/snapifact")
	build.Dir = root
	if _, err := build.CombinedOutput(); err != nil {
		t.Fatalf("compiled CLI build failed: %v", err)
	}

	cmd := exec.Command(binary, "text")
	cmd.Dir = root
	cmd.Stdin = strings.NewReader("compiled content")
	cmd.Env = append(os.Environ(),
		"SNAPIFACT_SERVER="+server.URL,
		"SNAPIFACT_STATE_DIR="+t.TempDir(),
		"SNAPIFACT_API_KEY="+apiKey,
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("compiled CLI failed: %v", err)
	}
	if gotKey != apiKey || !strings.HasPrefix(strings.TrimSpace(stdout.String()), "https://view.test/v/") || stderr.Len() != 0 {
		t.Fatalf("compiled output/header mismatch: key=%q stdout=%q stderr=%q", gotKey, stdout.String(), stderr.String())
	}
	if strings.Contains(stdout.String()+stderr.String(), apiKey) {
		t.Fatal("API key appeared in compiled CLI output")
	}
}
