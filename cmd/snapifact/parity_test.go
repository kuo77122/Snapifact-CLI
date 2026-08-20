package main

import (
	"bytes"
	"encoding/json"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSlidesAndSingleSourcePathFilenames(t *testing.T) {
	_, server, cleanup := testHarness(t)
	defer cleanup()

	path := filepath.Join(t.TempDir(), "nested", "artifact.txt")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("content"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"diff", "text", "markdown", "mermaid", "html", "csv", "slides"} {
		t.Run(command, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if exitCode := run([]string{command, path}, nil, &stdout, &stderr); exitCode != 0 {
				t.Fatalf("exit code = %d, stderr = %s", exitCode, stderr.String())
			}
			var request struct {
				ContentType string `json:"content_type"`
				Content     struct {
					Filename string `json:"filename"`
				} `json:"content"`
			}
			if err := json.Unmarshal([]byte(server.LastCreateBody()), &request); err != nil {
				t.Fatal(err)
			}
			if request.ContentType != command || request.Content.Filename != "artifact.txt" {
				t.Fatalf("request = %+v", request)
			}
		})
	}

	var stdout, stderr bytes.Buffer
	if exitCode := run([]string{"slides"}, strings.NewReader("slide content"), &stdout, &stderr); exitCode != 0 {
		t.Fatalf("stdin exit code = %d, stderr = %s", exitCode, stderr.String())
	}
	var request struct {
		ContentType string `json:"content_type"`
		Content     struct {
			Filename string `json:"filename"`
		} `json:"content"`
	}
	if err := json.Unmarshal([]byte(server.LastCreateBody()), &request); err != nil {
		t.Fatal(err)
	}
	if request.ContentType != "slides" || request.Content.Filename != "" {
		t.Fatalf("stdin request = %+v", request)
	}
}

func TestAllJSONCreatesPreserveOptionalCommentsEnabled(t *testing.T) {
	for _, value := range []string{"absent", "true", "false"} {
		t.Run(value, func(t *testing.T) {
			_, server, cleanup := testHarness(t)
			defer cleanup()
			args := []string{"text"}
			if value != "absent" {
				args = append(args, "--comments-enabled="+value)
			}
			var stdout, stderr bytes.Buffer
			if exitCode := run(args, strings.NewReader("content"), &stdout, &stderr); exitCode != 0 {
				t.Fatalf("exit code = %d, stderr = %s", exitCode, stderr.String())
			}
			var request map[string]any
			if err := json.Unmarshal([]byte(server.LastCreateBody()), &request); err != nil {
				t.Fatal(err)
			}
			got, present := request["comments_enabled"]
			if value == "absent" {
				if present {
					t.Fatalf("comments_enabled = %#v, want absent", got)
				}
			} else if got != (value == "true") {
				t.Fatalf("comments_enabled = %#v, want %s", got, value)
			}
		})
	}
}

func TestPDFJSONOutputUsesFinishCreate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			t.Fatal(err)
		}
		part, err := multipart.NewReader(r.Body, params["boundary"]).NextPart()
		if err != nil {
			t.Fatal(err)
		}
		var metadata map[string]any
		if err := json.NewDecoder(part).Decode(&metadata); err != nil {
			t.Fatal(err)
		}
		writeCreateResponseWithTierAndURL(w, "basic", "https://view.test/v/kpm2q6xxyegw5czekhga")
	}))
	defer server.Close()
	t.Setenv("SNAPIFACT_SERVER", server.URL)
	t.Setenv("SNAPIFACT_STATE_DIR", t.TempDir())

	var stdout, stderr bytes.Buffer
	if exitCode := run([]string{"pdf", "--json", "-"}, strings.NewReader("%PDF"), &stdout, &stderr); exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %s", exitCode, stderr.String())
	}
	var response createOutput
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		t.Fatalf("stdout JSON = %q: %v", stdout.String(), err)
	}
	if response.ID == "" || response.DeleteToken == "" {
		t.Fatalf("response = %+v", response)
	}
}

func TestCommentsCommandsValidateBeforeTokenReadAndRetainToken(t *testing.T) {
	const token = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	stateDir := t.TempDir()
	t.Setenv("SNAPIFACT_SERVER", server.URL)
	t.Setenv("SNAPIFACT_STATE_DIR", stateDir)
	t.Setenv("SNAPIFACT_API_KEY", "api-key-must-not-leak")
	id := "kpm2q6xxyegw5czekhga"
	tokenDir := filepath.Join(stateDir, "snapifact", "tokens")
	if err := os.MkdirAll(tokenDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tokenDir, id), []byte(token), 0600); err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{{"comments", "close", id}, {"comments", "delete", id, "42"}} {
		var stdout, stderr bytes.Buffer
		if exitCode := run(args, nil, &stdout, &stderr); exitCode != 0 {
			t.Fatalf("args=%v exit code=%d stderr=%s", args, exitCode, stderr.String())
		}
		if strings.Contains(stdout.String()+stderr.String(), token) || strings.Contains(stdout.String()+stderr.String(), "api-key-must-not-leak") {
			t.Fatalf("secret leaked for %v: stdout=%q stderr=%q", args, stdout.String(), stderr.String())
		}
	}
	if requests.Load() != 2 {
		t.Fatalf("requests = %d, want 2", requests.Load())
	}
	if _, err := os.Stat(filepath.Join(tokenDir, id)); err != nil {
		t.Fatalf("token was not retained: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if exitCode := run([]string{"comments", "delete", id, "01"}, nil, &stdout, &stderr); exitCode == 0 {
		t.Fatal("non-canonical message id unexpectedly succeeded")
	}
	if requests.Load() != 2 {
		t.Fatalf("invalid message id made a request: %d", requests.Load())
	}
}

func TestCanonicalPositiveMessageIDUsesSignedInt64Range(t *testing.T) {
	for value, want := range map[string]bool{
		"1":                    true,
		"9223372036854775807":  true,
		"0":                    false,
		"01":                   false,
		"-1":                   false,
		"9223372036854775808":  false,
		"18446744073709551615": false,
	} {
		if got := canonicalPositiveMessageID(value); got != want {
			t.Fatalf("canonicalPositiveMessageID(%q) = %t, want %t", value, got, want)
		}
	}
}

func TestCommentsMissingTokenDoesNotRequest(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	t.Setenv("SNAPIFACT_SERVER", server.URL)
	t.Setenv("SNAPIFACT_STATE_DIR", t.TempDir())

	var stdout, stderr bytes.Buffer
	if exitCode := run([]string{"comments", "close", "kpm2q6xxyegw5czekhga"}, nil, &stdout, &stderr); exitCode == 0 {
		t.Fatal("missing token unexpectedly succeeded")
	}
	if requests.Load() != 0 {
		t.Fatalf("requests = %d, want 0", requests.Load())
	}
}

func TestCommentsStructuredErrorRedactsTokenAndRetainsIt(t *testing.T) {
	const token = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"code": "forbidden", "message": "server echoed " + token, "request_id": "comment-request-id",
		})
	}))
	defer server.Close()
	stateDir := t.TempDir()
	t.Setenv("SNAPIFACT_SERVER", server.URL)
	t.Setenv("SNAPIFACT_STATE_DIR", stateDir)
	id := "kpm2q6xxyegw5czekhga"
	tokenDir := filepath.Join(stateDir, "snapifact", "tokens")
	if err := os.MkdirAll(tokenDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tokenDir, id), []byte(token), 0600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if exitCode := run([]string{"comments", "close", id}, nil, &stdout, &stderr); exitCode == 0 {
		t.Fatal("server error unexpectedly succeeded")
	}
	if !strings.Contains(stderr.String(), "forbidden") || !strings.Contains(stderr.String(), "comment-request-id") || strings.Contains(stderr.String(), token) {
		t.Fatalf("stderr = %q", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(tokenDir, id)); err != nil {
		t.Fatalf("token was not retained: %v", err)
	}
}

func TestCommentsAmbiguousFailureMakesOneAttemptAndRetainsToken(t *testing.T) {
	const token = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("test server does not support hijacking")
		}
		conn, _, err := hijacker.Hijack()
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.Close()
	}))
	defer server.Close()
	stateDir := t.TempDir()
	t.Setenv("SNAPIFACT_SERVER", server.URL)
	t.Setenv("SNAPIFACT_STATE_DIR", stateDir)
	id := "kpm2q6xxyegw5czekhga"
	tokenDir := filepath.Join(stateDir, "snapifact", "tokens")
	if err := os.MkdirAll(tokenDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tokenDir, id), []byte(token), 0600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if exitCode := run([]string{"comments", "close", id}, nil, &stdout, &stderr); exitCode == 0 {
		t.Fatal("ambiguous failure unexpectedly succeeded")
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want 1", requests.Load())
	}
	if _, err := os.Stat(filepath.Join(tokenDir, id)); err != nil {
		t.Fatalf("token was not retained: %v", err)
	}
}

func TestCommentsHelpDisclosesIrreversibleOwnerActions(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if exitCode := run([]string{"comments", "--help"}, nil, &stdout, &stderr); exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %s", exitCode, stderr.String())
	}
	got := stdout.String() + stderr.String()
	for _, fragment := range []string{"comments close", "comments delete", "irreversible"} {
		if !strings.Contains(strings.ToLower(got), fragment) {
			t.Fatalf("help = %q, missing %q", got, fragment)
		}
	}
}

func TestCommentsDeleteUsageIncludesMessageID(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if exitCode := run([]string{"comments", "delete", "kpm2q6xxyegw5czekhga"}, nil, &stdout, &stderr); exitCode == 0 {
		t.Fatal("missing message ID unexpectedly succeeded")
	}
	if !strings.Contains(stderr.String(), "<message-id>") {
		t.Fatalf("stderr = %q, want message-id usage", stderr.String())
	}
}
