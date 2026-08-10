package main

import (
	"bytes"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// CLI integration tests for snapifact text and delete commands.
// ---------------------------------------------------------------------------

type contractTestServer struct {
	*httptest.Server
	mu             sync.Mutex
	nextID         uint64
	requestCount   atomic.Int32
	lastCreateBody string
	snapshots      map[string]string
}

func newContractTestServer(t *testing.T) *contractTestServer {
	t.Helper()
	server := &contractTestServer{snapshots: make(map[string]string)}
	server.Server = httptest.NewServer(http.HandlerFunc(server.handle))
	return server
}

func (s *contractTestServer) handle(w http.ResponseWriter, r *http.Request) {
	s.requestCount.Add(1)
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/snapshots":
		s.handleCreate(w, r)
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v1/snapshots/"):
		s.handleDelete(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v/"):
		s.handleView(w, r)
	default:
		writeContractError(w, http.StatusNotFound, "not_found", "not found", "contract-request-id")
	}
}

func (s *contractTestServer) handleCreate(w http.ResponseWriter, r *http.Request) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeContractError(w, http.StatusUnsupportedMediaType, "unsupported_media", "content type must be application/json", "contract-request-id")
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeContractError(w, http.StatusBadRequest, "invalid_request", "could not read request", "contract-request-id")
		return
	}
	if len(body) > 5<<20 {
		writeContractError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "snapshot is too large", "contract-request-id")
		return
	}
	var request any
	if err := json.Unmarshal(body, &request); err != nil {
		writeContractError(w, http.StatusBadRequest, "invalid_request", "invalid JSON", "contract-request-id")
		return
	}

	s.mu.Lock()
	s.nextID++
	var rawID [12]byte
	binary.BigEndian.PutUint64(rawID[4:], s.nextID)
	id := base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding).EncodeToString(rawID[:])
	token := strings.Repeat("A", 43)
	s.lastCreateBody = string(body)
	s.snapshots[id] = token
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"id":           id,
		"url":          "https://view.test/v/" + id,
		"expires_at":   time.Now().UTC().Add(7 * 24 * time.Hour).Format(time.RFC3339),
		"delete_token": token,
	})
}

func (s *contractTestServer) handleDelete(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/v1/snapshots/")
	s.mu.Lock()
	token, ok := s.snapshots[id]
	if ok && r.Header.Get("Authorization") != "Bearer "+token {
		s.mu.Unlock()
		writeContractError(w, http.StatusForbidden, "forbidden", "invalid delete credentials", "contract-request-id")
		return
	}
	if ok {
		delete(s.snapshots, id)
	}
	s.mu.Unlock()
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *contractTestServer) handleView(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/v/")
	s.mu.Lock()
	_, ok := s.snapshots[id]
	s.mu.Unlock()
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func writeContractError(w http.ResponseWriter, status int, code, message, requestID string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "message": message, "request_id": requestID})
}

func (s *contractTestServer) LastCreateBody() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastCreateBody
}

func (s *contractTestServer) CreateCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return int(s.nextID)
}

func (s *contractTestServer) RequestCount() int {
	return int(s.requestCount.Load())
}

func (s *contractTestServer) SeedSnapshot(t *testing.T) (id, token string) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	var rawID [12]byte
	binary.BigEndian.PutUint64(rawID[4:], s.nextID)
	id = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding).EncodeToString(rawID[:])
	token = strings.Repeat("s", 43)
	s.snapshots[id] = token
	return id, token
}

// testHarness starts a server-independent HTTP contract handler and sets test
// environment variables for the CLI.
func testHarness(t *testing.T) (serverURL string, server *contractTestServer, cleanup func()) {
	t.Helper()
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	server = newContractTestServer(t)

	// env overrides for CLI test seams
	t.Setenv("SNAPIFACT_SERVER", server.URL)
	t.Setenv("SNAPIFACT_STATE_DIR", stateDir)

	return server.URL, server, server.Close
}

// createOutput is the output we expect from --json mode.
type createOutput struct {
	ID          string `json:"id"`
	URL         string `json:"url"`
	ExpiresAt   string `json:"expires_at"`
	DeleteToken string `json:"delete_token"`
	Tier        string `json:"tier,omitempty"`
}

// errorOutput is the structured error from the server.
type errorOutput struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

func TestTextFromPathDefaultOutput(t *testing.T) {
	_, _, cleanup := testHarness(t)
	defer cleanup()

	content := "hello from file path"
	src := t.TempDir()
	srcPath := filepath.Join(src, "test.txt")
	if err := os.WriteFile(srcPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	exitCode := run([]string{"text", srcPath}, nil, &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %s", exitCode, stderr.String())
	}
	url := strings.TrimSpace(stdout.String())
	if !strings.HasPrefix(url, "https://view.test/v/") {
		t.Fatalf("stdout = %q, want URL", url)
	}
	if len(url) <= len("https://view.test/v/")+1 {
		t.Fatalf("stdout URL too short: %q", url)
	}
	if stderr.Len() > 0 {
		t.Fatalf("unexpected stderr: %s", stderr.String())
	}
}

func TestTextCommandUsesTextContentType(t *testing.T) {
	_, server, cleanup := testHarness(t)
	defer cleanup()

	var stdout, stderr bytes.Buffer
	if exitCode := run([]string{"text"}, strings.NewReader("plain UTF-8 text"), &stdout, &stderr); exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %s", exitCode, stderr.String())
	}
	var request struct {
		ContentType string `json:"content_type"`
		Content     struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal([]byte(server.LastCreateBody()), &request); err != nil {
		t.Fatal(err)
	}
	if request.ContentType != "text" || request.Content.Text != "plain UTF-8 text" {
		t.Fatalf("text request = %+v", request)
	}
}

func TestFileCommandIsUnknown(t *testing.T) {
	_, server, cleanup := testHarness(t)
	defer cleanup()

	var stdout, stderr bytes.Buffer
	if exitCode := run([]string{"file"}, strings.NewReader("must not upload"), &stdout, &stderr); exitCode == 0 {
		t.Fatal("file command unexpectedly succeeded")
	}
	if !strings.Contains(stderr.String(), "unknown command: file") || server.RequestCount() != 0 {
		t.Fatalf("unknown command output = stdout %q stderr %q requests %d", stdout.String(), stderr.String(), server.RequestCount())
	}
}

func TestExtractIDAcceptsCanonicalAndLegacyIDsFromURLs(t *testing.T) {
	newID := "kpm2q6xxyegw5czekhga"
	legacyID := strings.Repeat("a", 32)
	for _, tt := range []struct {
		name  string
		input string
		want  string
	}{
		{name: "new ID", input: newID, want: newID},
		{name: "new URL", input: "https://view.test/v/" + newID, want: newID},
		{name: "legacy ID", input: legacyID, want: legacyID},
		{name: "legacy URL", input: "https://view.test/v/" + legacyID + "/", want: legacyID},
		{name: "malformed", input: "https://view.test/v/not-an-id", want: ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractID(tt.input); got != tt.want {
				t.Fatalf("extractID(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestMermaidFromStdinDefaultOutput(t *testing.T) {
	_, _, cleanup := testHarness(t)
	defer cleanup()

	var stdout, stderr bytes.Buffer
	if exitCode := run([]string{"mermaid"}, strings.NewReader("flowchart TD\n  A --> B\n"), &stdout, &stderr); exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %s", exitCode, stderr.String())
	}
	if !strings.HasPrefix(strings.TrimSpace(stdout.String()), "https://view.test/v/") {
		t.Fatalf("stdout = %q, want URL", stdout.String())
	}
}

func TestHTMLFromPathAndStdinDefaultOutput(t *testing.T) {
	_, _, cleanup := testHarness(t)
	defer cleanup()

	source := "<!doctype html><script>document.body.textContent='ok'</script>"
	path := filepath.Join(t.TempDir(), "demo.html")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	for name, input := range map[string]struct {
		args  []string
		stdin string
	}{
		"path":  {args: []string{"html", path}},
		"stdin": {args: []string{"html"}, stdin: source},
	} {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if exitCode := run(input.args, strings.NewReader(input.stdin), &stdout, &stderr); exitCode != 0 {
				t.Fatalf("exit code = %d, stderr = %s", exitCode, stderr.String())
			}
			if !strings.HasPrefix(strings.TrimSpace(stdout.String()), "https://view.test/v/") {
				t.Fatalf("stdout = %q, want URL", stdout.String())
			}
			if stderr.Len() != 0 {
				t.Fatalf("unexpected stderr = %q", stderr.String())
			}
		})
	}
}

func TestDiffFromPathAndStdinDefaultOutput(t *testing.T) {
	_, _, cleanup := testHarness(t)
	defer cleanup()

	patch := "--- a/file.txt\n+++ b/file.txt\n@@ -1 +1 @@\n-old\n+new\n"
	srcPath := filepath.Join(t.TempDir(), "change.patch")
	if err := os.WriteFile(srcPath, []byte(patch), 0600); err != nil {
		t.Fatal(err)
	}
	for name, input := range map[string]struct {
		args  []string
		stdin string
	}{
		"file":  {args: []string{"diff", srcPath}},
		"stdin": {args: []string{"diff"}, stdin: patch},
	} {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if exitCode := run(input.args, strings.NewReader(input.stdin), &stdout, &stderr); exitCode != 0 {
				t.Fatalf("exit code = %d, stderr = %s", exitCode, stderr.String())
			}
			if !strings.HasPrefix(strings.TrimSpace(stdout.String()), "https://view.test/v/") {
				t.Fatalf("stdout = %q, want URL", stdout.String())
			}
			if stderr.Len() != 0 {
				t.Fatalf("unexpected stderr = %q", stderr.String())
			}
		})
	}
}

func TestCompareFromFilesJSONOutputAndMetadata(t *testing.T) {
	_, server, cleanup := testHarness(t)
	defer cleanup()

	root := t.TempDir()
	beforePath := filepath.Join(root, "before.txt")
	afterPath := filepath.Join(root, "after.txt")
	descriptionPath := filepath.Join(root, "description.md")
	if err := os.WriteFile(beforePath, []byte("before\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(afterPath, []byte("after\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(descriptionPath, []byte("## Compare\n"), 0600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if exitCode := run([]string{"compare", "--json", "--title", "Review", "--description-file", descriptionPath, beforePath, afterPath}, nil, &stdout, &stderr); exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %s", exitCode, stderr.String())
	}
	var out createOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("stdout JSON error = %v, body = %s", err, stdout.String())
	}
	if out.ID == "" || out.URL == "" || out.DeleteToken == "" {
		t.Fatalf("incomplete JSON output: %+v", out)
	}
	var request struct {
		ContentType         string `json:"content_type"`
		Title               string `json:"title"`
		DescriptionMarkdown string `json:"description_markdown"`
		Content             struct {
			Before struct {
				Text     string `json:"text"`
				Filename string `json:"filename"`
			} `json:"before"`
			After struct {
				Text     string `json:"text"`
				Filename string `json:"filename"`
			} `json:"after"`
		} `json:"content"`
	}
	if err := json.Unmarshal([]byte(server.LastCreateBody()), &request); err != nil {
		t.Fatal(err)
	}
	if request.ContentType != "compare" || request.Title != "Review" || request.DescriptionMarkdown != "## Compare\n" || request.Content.Before.Filename != "before.txt" || request.Content.After.Filename != "after.txt" || request.Content.Before.Text != "before\n" || request.Content.After.Text != "after\n" {
		t.Fatalf("compare request = %+v", request)
	}
}

func TestCompareReadFailureDoesNotUpload(t *testing.T) {
	_, server, cleanup := testHarness(t)
	defer cleanup()

	var stdout, stderr bytes.Buffer
	if exitCode := run([]string{"compare", filepath.Join(t.TempDir(), "missing-before"), "missing-after"}, nil, &stdout, &stderr); exitCode == 0 {
		t.Fatal("missing before file unexpectedly succeeded")
	}
	if !strings.Contains(stderr.String(), "read before file") || stdout.Len() != 0 {
		t.Fatalf("read failure output = stdout %q stderr %q", stdout.String(), stderr.String())
	}
	if count := server.CreateCount(); count != 0 {
		t.Fatalf("snapshot count = %d", count)
	}
}

func TestTextFromStdinDefaultOutput(t *testing.T) {
	_, _, cleanup := testHarness(t)
	defer cleanup()

	input := "stdin content here"
	var stdout, stderr bytes.Buffer
	exitCode := run([]string{"text"}, strings.NewReader(input), &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %s", exitCode, stderr.String())
	}
	url := strings.TrimSpace(stdout.String())
	if !strings.HasPrefix(url, "https://view.test/v/") {
		t.Fatalf("stdout = %q, want URL", url)
	}
	if stderr.Len() > 0 {
		t.Fatalf("unexpected stderr: %s", stderr.String())
	}
}

func TestTextFromExplicitStdinDash(t *testing.T) {
	_, _, cleanup := testHarness(t)
	defer cleanup()

	var stdout, stderr bytes.Buffer
	exitCode := run([]string{"text", "-"}, strings.NewReader("dash stdin"), &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %s", exitCode, stderr.String())
	}
	if !strings.HasPrefix(stdout.String(), "https://view.test/v/") {
		t.Fatalf("stdout = %q, want URL", stdout.String())
	}
}

func TestCSVFromPathJSONOutputAndMetadata(t *testing.T) {
	_, server, cleanup := testHarness(t)
	defer cleanup()

	root := t.TempDir()
	content := "name,value\r\nalpha,1\r\n"
	path := filepath.Join(root, "report.csv")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if exitCode := run([]string{"csv", "--json", "--title", "Report", path}, nil, &stdout, &stderr); exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %s", exitCode, stderr.String())
	}
	var out createOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("stdout JSON error = %v, body = %s", err, stdout.String())
	}
	var request struct {
		ContentType string `json:"content_type"`
		Title       string `json:"title"`
		Content     struct {
			Text     string `json:"text"`
			Filename string `json:"filename"`
		} `json:"content"`
	}
	if err := json.Unmarshal([]byte(server.LastCreateBody()), &request); err != nil {
		t.Fatal(err)
	}
	if request.ContentType != "csv" || request.Content.Filename != "report.csv" || request.Title != "Report" || request.Content.Text != content {
		t.Fatalf("CSV request = %+v", request)
	}
}

func TestCSVFromExplicitStdinDashIsURLOnly(t *testing.T) {
	_, server, cleanup := testHarness(t)
	defer cleanup()

	for name, args := range map[string][]string{
		"implicit stdin": {"csv"},
		"explicit dash":  {"csv", "-"},
	} {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if exitCode := run(args, strings.NewReader("name,value\nstdin,2\n"), &stdout, &stderr); exitCode != 0 {
				t.Fatalf("exit code = %d, stderr = %s", exitCode, stderr.String())
			}
			if !strings.HasPrefix(strings.TrimSpace(stdout.String()), "https://view.test/v/") {
				t.Fatalf("stdout = %q, want URL", stdout.String())
			}
			if strings.Contains(stdout.String(), "delete_token") || stderr.Len() != 0 {
				t.Fatalf("unexpected URL-only output: stdout=%q stderr=%q", stdout.String(), stderr.String())
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
			if request.ContentType != "csv" || request.Content.Filename != "" {
				t.Fatalf("stdin CSV request = %+v, want csv with no filename", request)
			}
		})
	}
}

func TestTextJSONOutput(t *testing.T) {
	_, _, cleanup := testHarness(t)
	defer cleanup()

	var stdout, stderr bytes.Buffer
	exitCode := run([]string{"text", "--json"}, strings.NewReader("json test"), &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %s", exitCode, stderr.String())
	}
	var out createOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("stdout JSON error = %v, body = %s", err, stdout.String())
	}
	if out.ID == "" || out.URL == "" || out.ExpiresAt == "" || out.DeleteToken == "" {
		t.Fatalf("incomplete JSON output: %+v", out)
	}
	if !strings.HasPrefix(out.URL, "https://view.test/v/") {
		t.Fatalf("URL = %q", out.URL)
	}
}

func TestTextWithTitle(t *testing.T) {
	_, _, cleanup := testHarness(t)
	defer cleanup()

	var stdout, stderr bytes.Buffer
	exitCode := run([]string{"text", "--title", "My Title", "--json"}, strings.NewReader("titled content"), &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %s", exitCode, stderr.String())
	}
	var out createOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("stdout JSON error = %v", err)
	}
	if out.ID == "" {
		t.Fatal("no ID in output")
	}
}

func TestTextServerErrorOutput(t *testing.T) {
	_, _, cleanup := testHarness(t)
	defer cleanup()

	// Send invalid content to trigger server error (source too large)
	largeContent := strings.Repeat("a", 6<<20) // 6 MiB > 5 MiB limit
	var stdout, stderr bytes.Buffer
	exitCode := run([]string{"text"}, strings.NewReader(largeContent), &stdout, &stderr)
	if exitCode == 0 {
		t.Fatalf("expected non-zero exit for server error, got 0")
	}
	var errResp errorOutput
	if err := json.Unmarshal(stderr.Bytes(), &errResp); err != nil {
		t.Fatalf("stderr JSON error = %v, body = %s", err, stderr.String())
	}
	if errResp.Code == "" || errResp.Message == "" || errResp.RequestID == "" {
		t.Fatalf("incomplete error: %+v", errResp)
	}
	if stdout.Len() > 0 {
		t.Fatalf("expected empty stdout on error, got %s", stdout.String())
	}
}

func TestCreateErrorRedactsConfiguredAPIKey(t *testing.T) {
	const apiKey = "configured-key"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"code": "invalid_request", "message": "echoed " + apiKey, "request_id": "req",
		})
	}))
	defer server.Close()
	t.Setenv("SNAPIFACT_SERVER", server.URL)
	t.Setenv("SNAPIFACT_STATE_DIR", t.TempDir())
	t.Setenv("SNAPIFACT_API_KEY", apiKey)

	var stdout, stderr bytes.Buffer
	if exitCode := run([]string{"text"}, strings.NewReader("content"), &stdout, &stderr); exitCode == 0 {
		t.Fatal("server error unexpectedly succeeded")
	}
	if stdout.Len() != 0 || strings.Contains(stderr.String(), apiKey) {
		t.Fatalf("API key leaked in create error: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestDeleteByID(t *testing.T) {
	_, server, cleanup := testHarness(t)
	defer cleanup()

	snapshotID, token := server.SeedSnapshot(t)

	stateDir := os.Getenv("SNAPIFACT_STATE_DIR")
	tokenDir := filepath.Join(stateDir, "snapifact", "tokens")
	if err := os.MkdirAll(tokenDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tokenDir, snapshotID), []byte(token), 0600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	exitCode := run([]string{"delete", snapshotID}, nil, &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %s", exitCode, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(tokenDir, snapshotID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("token file was not removed after delete: %v", err)
	}
}

func TestDeleteByURL(t *testing.T) {
	_, server, cleanup := testHarness(t)
	defer cleanup()

	snapshotID, token := server.SeedSnapshot(t)

	stateDir := os.Getenv("SNAPIFACT_STATE_DIR")
	tokenDir := filepath.Join(stateDir, "snapifact", "tokens")
	if err := os.MkdirAll(tokenDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tokenDir, snapshotID), []byte(token), 0600); err != nil {
		t.Fatal(err)
	}

	url := "https://view.test/v/" + snapshotID
	var stdout, stderr bytes.Buffer
	exitCode := run([]string{"delete", url}, nil, &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %s", exitCode, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(tokenDir, snapshotID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("token file was not removed after delete by URL: %v", err)
	}
}

func TestDeleteAlreadyGone(t *testing.T) {
	_, server, cleanup := testHarness(t)
	defer cleanup()

	snapshotID, token := server.SeedSnapshot(t)

	stateDir := os.Getenv("SNAPIFACT_STATE_DIR")
	tokenDir := filepath.Join(stateDir, "snapifact", "tokens")
	if err := os.MkdirAll(tokenDir, 0700); err != nil {
		t.Fatal(err)
	}
	tokenPath := filepath.Join(tokenDir, snapshotID)
	if err := os.WriteFile(tokenPath, []byte(token), 0600); err != nil {
		t.Fatal(err)
	}

	var stdout1, stderr1 bytes.Buffer
	exitCode1 := run([]string{"delete", snapshotID}, nil, &stdout1, &stderr1)
	if exitCode1 != 0 {
		t.Fatalf("first delete exit = %d, stderr = %s", exitCode1, stderr1.String())
	}
	if _, err := os.Stat(tokenPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("token file not removed after first delete: %v", err)
	}

	var stdout2, stderr2 bytes.Buffer
	exitCode2 := run([]string{"delete", snapshotID}, nil, &stdout2, &stderr2)
	if exitCode2 != 0 {
		t.Fatalf("second delete (already gone) exit = %d, stderr = %s", exitCode2, stderr2.String())
	}
}

func TestDeleteServerErrorOutput(t *testing.T) {
	// Verify that delete command preserves structured JSON errors from the server.
	fakeID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{
			"code":       "internal_error",
			"message":    "delete failed",
			"request_id": "test-req-id",
		})
	})
	fakeServer := httptest.NewServer(handler)
	defer fakeServer.Close()
	t.Setenv("SNAPIFACT_SERVER", fakeServer.URL)

	stateDir := t.TempDir()
	t.Setenv("SNAPIFACT_STATE_DIR", stateDir)
	tokenDir := filepath.Join(stateDir, "snapifact", "tokens")
	os.MkdirAll(tokenDir, 0700)
	os.WriteFile(filepath.Join(tokenDir, fakeID), []byte(strings.Repeat("A", 43)), 0600)

	var stdout, stderr bytes.Buffer
	exitCode := run([]string{"delete", fakeID}, nil, &stdout, &stderr)
	if exitCode == 0 {
		t.Fatal("expected non-zero exit for server error")
	}

	var errResp errorOutput
	if err := json.Unmarshal(stderr.Bytes(), &errResp); err != nil {
		t.Fatalf("stderr JSON error: %v, body = %s", err, stderr.String())
	}
	if errResp.Code != "internal_error" || errResp.Message != "delete failed" || errResp.RequestID != "test-req-id" {
		t.Fatalf("unexpected error: %+v", errResp)
	}
	if stdout.Len() > 0 {
		t.Fatalf("expected empty stdout on error, got %s", stdout.String())
	}
}

func TestTokenPermissions(t *testing.T) {
	_, _, cleanup := testHarness(t)
	defer cleanup()

	var stdout, stderr bytes.Buffer
	exitCode := run([]string{"text", "--json"}, strings.NewReader("perm check"), &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %s", exitCode, stderr.String())
	}
	var out createOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatal(err)
	}

	stateDir := os.Getenv("SNAPIFACT_STATE_DIR")
	tokenDir := filepath.Join(stateDir, "snapifact", "tokens")
	tokenPath := filepath.Join(tokenDir, out.ID)

	info, err := os.Stat(tokenPath)
	if err != nil {
		t.Fatalf("token file %s not created: %v", tokenPath, err)
	}
	if mode := info.Mode().Perm(); mode != 0600 {
		t.Fatalf("token file mode = %04o, want 0600", mode)
	}
	dirInfo, err := os.Stat(tokenDir)
	if err != nil {
		t.Fatalf("token dir not found: %v", err)
	}
	if mode := dirInfo.Mode().Perm(); mode != 0700 {
		t.Fatalf("token dir mode = %04o, want 0700", mode)
	}
}

func TestStaleTokenCleanup(t *testing.T) {
	_, _, cleanup := testHarness(t)
	defer cleanup()

	stateDir := os.Getenv("SNAPIFACT_STATE_DIR")
	tokenDir := filepath.Join(stateDir, "snapifact", "tokens")
	if err := os.MkdirAll(tokenDir, 0700); err != nil {
		t.Fatal(err)
	}

	oldPath := filepath.Join(tokenDir, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err := os.WriteFile(oldPath, []byte(strings.Repeat("A", 43)), 0600); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(oldPath, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	freshPath := filepath.Join(tokenDir, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	if err := os.WriteFile(freshPath, []byte(strings.Repeat("A", 42)+"Q"), 0600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	exitCode := run([]string{"text", "--json"}, strings.NewReader("cleanup test"), &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %s", exitCode, stderr.String())
	}

	if _, err := os.Stat(oldPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale token file was not cleaned up: %v", err)
	}
	if _, err := os.Stat(freshPath); err != nil {
		t.Fatalf("fresh token file was incorrectly removed: %v", err)
	}
}

func TestTokenNotInNormalStdout(t *testing.T) {
	_, _, cleanup := testHarness(t)
	defer cleanup()

	var stdout, stderr bytes.Buffer
	exitCode := run([]string{"text"}, strings.NewReader("no token leak"), &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %s", exitCode, stderr.String())
	}
	out := strings.TrimSpace(stdout.String())
	if strings.HasPrefix(out, "{") {
		t.Fatalf("default mode printed JSON: %s", out)
	}
	if strings.Contains(out, "delete_token") {
		t.Fatalf("default mode leaked delete_token: %s", out)
	}
}

func TestKeyedDowngradeDeletesWithoutAPIKeyAndPrintsNoSuccess(t *testing.T) {
	const token = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	const id = "kpm2q6xxyegw5czekhga"
	var createKey, deleteKey string
	var deleteCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			createKey = r.Header.Get("X-Snapifact-API-Key")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"id":"`+id+`","url":"https://view.test/v/`+id+`","expires_at":"2026-08-13T00:00:00Z","delete_token":"`+token+`","tier":"anonymous"}`)
		case http.MethodDelete:
			deleteCount.Add(1)
			deleteKey = r.Header.Get("X-Snapifact-API-Key")
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	t.Setenv("SNAPIFACT_SERVER", server.URL)
	t.Setenv("SNAPIFACT_STATE_DIR", t.TempDir())
	t.Setenv("SNAPIFACT_API_KEY", "configured-key")

	var stdout, stderr bytes.Buffer
	if exitCode := run([]string{"text"}, strings.NewReader("content"), &stdout, &stderr); exitCode == 0 {
		t.Fatal("keyed downgrade unexpectedly succeeded")
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if got := stderr.String(); got != "error: server did not apply the configured API key; snapshot was deleted\n" {
		t.Fatalf("stderr = %q, want exact downgrade error", got)
	}
	if createKey != "configured-key" || deleteKey != "" || deleteCount.Load() != 1 {
		t.Fatalf("create key = %q, delete key = %q, delete count = %d", createKey, deleteKey, deleteCount.Load())
	}
}

func TestKeyedAcceptedTiersPreserveJSONResponse(t *testing.T) {
	for _, tier := range []string{"basic", "pro", "admin"} {
		t.Run(tier, func(t *testing.T) {
			const token = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
			const id = "kpm2q6xxyegw5czekhga"
			var createKey string
			var deleteCount atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodDelete {
					deleteCount.Add(1)
					w.WriteHeader(http.StatusNoContent)
					return
				}
				createKey = r.Header.Get("X-Snapifact-API-Key")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				_ = json.NewEncoder(w).Encode(map[string]string{
					"id": id, "url": "https://view.test/v/" + id,
					"expires_at": "2026-08-13T00:00:00Z", "delete_token": token, "tier": tier,
				})
			}))
			defer server.Close()
			t.Setenv("SNAPIFACT_SERVER", server.URL)
			t.Setenv("SNAPIFACT_STATE_DIR", t.TempDir())
			t.Setenv("SNAPIFACT_API_KEY", "configured-key")

			var stdout, stderr bytes.Buffer
			if exitCode := run([]string{"text", "--json"}, strings.NewReader("content"), &stdout, &stderr); exitCode != 0 {
				t.Fatalf("exit code = %d, stderr = %s", exitCode, stderr.String())
			}
			var output createOutput
			if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
				t.Fatal(err)
			}
			if output.Tier != tier || createKey != "configured-key" || deleteCount.Load() != 0 {
				t.Fatalf("tier=%q create key=%q delete count=%d", output.Tier, createKey, deleteCount.Load())
			}
			if strings.Contains(stdout.String()+stderr.String(), "configured-key") {
				t.Fatal("API key appeared in CLI output")
			}
		})
	}
}

func TestKeyedDowngradeTiersAreAllCompensated(t *testing.T) {
	for _, tier := range []string{"", "anonymous", "unknown", "Basic", " basic "} {
		t.Run(tier, func(t *testing.T) {
			const token = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
			const id = "kpm2q6xxyegw5czekhga"
			var deleteCount atomic.Int32
			var deleteKey string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodDelete {
					deleteCount.Add(1)
					deleteKey = r.Header.Get("X-Snapifact-API-Key")
					w.WriteHeader(http.StatusNoContent)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				_ = json.NewEncoder(w).Encode(map[string]string{
					"id": id, "url": "https://view.test/v/" + id,
					"expires_at": "2026-08-13T00:00:00Z", "delete_token": token, "tier": tier,
				})
			}))
			defer server.Close()
			t.Setenv("SNAPIFACT_SERVER", server.URL)
			t.Setenv("SNAPIFACT_STATE_DIR", t.TempDir())
			t.Setenv("SNAPIFACT_API_KEY", "configured-key")

			var stdout, stderr bytes.Buffer
			if exitCode := run([]string{"text", "--json"}, strings.NewReader("content"), &stdout, &stderr); exitCode == 0 {
				t.Fatal("downgrade unexpectedly succeeded")
			}
			if stdout.Len() != 0 || stderr.String() != "error: server did not apply the configured API key; snapshot was deleted\n" {
				t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
			if deleteCount.Load() != 1 || deleteKey != "" || strings.Contains(stderr.String(), "configured-key") {
				t.Fatalf("delete count=%d delete key=%q stderr=%q", deleteCount.Load(), deleteKey, stderr.String())
			}
		})
	}
}

func TestKeyedDowngradeDeleteFailureSavesRecoveryToken(t *testing.T) {
	const token = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	const id = "kpm2q6xxyegw5czekhga"
	var deleteKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleteKey = r.Header.Get("X-Snapifact-API-Key")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "delete_failed", "message": "try again " + token, "request_id": "req"})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"id": id, "url": "https://view.test/v/" + id,
			"expires_at": "2026-08-13T00:00:00Z", "delete_token": token, "tier": "anonymous",
		})
	}))
	defer server.Close()
	stateDir := t.TempDir()
	t.Setenv("SNAPIFACT_SERVER", server.URL)
	t.Setenv("SNAPIFACT_STATE_DIR", stateDir)
	t.Setenv("SNAPIFACT_API_KEY", "configured-key")

	var stdout, stderr bytes.Buffer
	if exitCode := run([]string{"text"}, strings.NewReader("content"), &stdout, &stderr); exitCode == 0 {
		t.Fatal("downgrade unexpectedly succeeded")
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), "Snapshot URL: https://view.test/v/"+id) || !strings.Contains(stderr.String(), "saved locally") {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if strings.Contains(stderr.String(), token) || strings.Contains(stderr.String(), "configured-key") || deleteKey != "" {
		t.Fatalf("secret or API key leaked: stderr=%q delete key=%q", stderr.String(), deleteKey)
	}
	data, err := os.ReadFile(filepath.Join(stateDir, "snapifact", "tokens", id))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"version":1`) {
		t.Fatalf("recovery token file = %q", data)
	}
}

func TestKeyedDowngradeDeleteAndTokenSaveFailureIsLastResort(t *testing.T) {
	const token = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	const id = "kpm2q6xxyegw5czekhga"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "delete_failed", "message": "try again", "request_id": "req"})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"id": id, "url": "https://view.test/v/" + id,
			"expires_at": "not-an-expiry", "delete_token": token, "tier": "anonymous",
		})
	}))
	defer server.Close()
	t.Setenv("SNAPIFACT_SERVER", server.URL)
	t.Setenv("SNAPIFACT_STATE_DIR", t.TempDir())
	t.Setenv("SNAPIFACT_API_KEY", "configured-key")

	var stdout, stderr bytes.Buffer
	if exitCode := run([]string{"text"}, strings.NewReader("content"), &stdout, &stderr); exitCode == 0 {
		t.Fatal("downgrade unexpectedly succeeded")
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), "Snapshot delete token: "+token) || !strings.Contains(stderr.String(), "Save error:") || !strings.Contains(stderr.String(), "Delete error:") {
		t.Fatalf("last-resort stderr=%q", stderr.String())
	}
	if strings.Contains(stderr.String(), "configured-key") {
		t.Fatal("API key appeared in last-resort output")
	}
}

func TestKeyedAcceptedTierWithMalformedExpiryUsesExistingCompensation(t *testing.T) {
	const token = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	const id = "kpm2q6xxyegw5czekhga"
	var deleteCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleteCount.Add(1)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"id": id, "url": "https://view.test/v/" + id,
			"expires_at": "not-an-expiry", "delete_token": token, "tier": "basic",
		})
	}))
	defer server.Close()
	t.Setenv("SNAPIFACT_SERVER", server.URL)
	t.Setenv("SNAPIFACT_STATE_DIR", t.TempDir())
	t.Setenv("SNAPIFACT_API_KEY", "configured-key")

	var stdout, stderr bytes.Buffer
	if exitCode := run([]string{"text"}, strings.NewReader("content"), &stdout, &stderr); exitCode == 0 {
		t.Fatal("malformed expiry unexpectedly succeeded")
	}
	if stdout.Len() != 0 || deleteCount.Load() != 1 || !strings.Contains(stderr.String(), "token save error") || strings.Contains(stderr.String(), "WARNING") {
		t.Fatalf("stdout=%q stderr=%q delete count=%d", stdout.String(), stderr.String(), deleteCount.Load())
	}
}

func TestNoAutoRetryOnTimeout(t *testing.T) {
	var reqCount atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqCount.Add(1)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"code":"service_unavailable","message":"not retried","request_id":"test-req-id"}` + "\n"))
	})
	fakeServer := httptest.NewServer(handler)
	defer fakeServer.Close()
	t.Setenv("SNAPIFACT_SERVER", fakeServer.URL)
	t.Setenv("SNAPIFACT_STATE_DIR", t.TempDir())

	var stdout, stderr bytes.Buffer
	exitCode := run([]string{"text"}, strings.NewReader("no retry"), &stdout, &stderr)
	if exitCode == 0 {
		t.Fatalf("expected non-zero exit for server error, got 0")
	}
	if n := reqCount.Load(); n != 1 {
		t.Fatalf("expected exactly 1 POST, got %d", n)
	}
}

func TestTokenSaveFailureCompensatingDeleteSuccess(t *testing.T) {
	// Token save fails → compensating delete succeeds → no URL printed, non-zero exit
	_, _, cleanup := testHarness(t)
	defer cleanup()

	root := t.TempDir()
	writable := filepath.Join(root, "writable")
	os.MkdirAll(writable, 0755)
	readonlyParent := filepath.Join(writable, "readonly")
	os.MkdirAll(readonlyParent, 0555)
	t.Setenv("SNAPIFACT_STATE_DIR", filepath.Join(readonlyParent, "sub"))

	var stdout, stderr bytes.Buffer
	exitCode := run([]string{"text", "--json"}, strings.NewReader("compensating delete success"), &stdout, &stderr)
	if exitCode == 0 {
		t.Fatalf("expected non-zero exit for token save failure, got 0")
	}
	stderrOutput := stderr.String()
	if strings.Contains(stderrOutput, "WARNING") {
		t.Fatalf("expected compensating-delete-success message, got recovery warning: %s", stderrOutput)
	}
	if !strings.Contains(stderrOutput, "token save error") {
		t.Fatalf("expected token save error in stderr, got: %s", stderrOutput)
	}
	if stdout.Len() > 0 {
		t.Fatalf("expected no stdout after token save failure, got: %s", stdout.String())
	}
}

func TestTokenSaveFailureCompensatingDeleteFailsRecoveryWarning(t *testing.T) {
	// Token save fails → compensating delete also fails → recovery warning printed
	// Use a fake server that accepts POST (returns a fake success) but rejects DELETE.
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/snapshots", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{
			"id":           "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			"url":          "https://view.test/v/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			"expires_at":   time.Now().Add(7 * 24 * time.Hour).Format(time.RFC3339),
			"delete_token": strings.Repeat("A", 43),
		})
	})
	mux.HandleFunc("/v1/snapshots/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]any{
			"code":       "internal_error",
			"message":    "simulated delete failure",
			"request_id": "test-request-id",
		})
	})
	fakeServer := httptest.NewServer(mux)
	defer fakeServer.Close()

	t.Setenv("SNAPIFACT_SERVER", fakeServer.URL)

	root := t.TempDir()
	writable := filepath.Join(root, "writable")
	os.MkdirAll(writable, 0755)
	readonlyParent := filepath.Join(writable, "readonly")
	os.MkdirAll(readonlyParent, 0555)
	t.Setenv("SNAPIFACT_STATE_DIR", filepath.Join(readonlyParent, "sub"))

	var stdout, stderr bytes.Buffer
	exitCode := run([]string{"text", "--json"}, strings.NewReader("recovery test"), &stdout, &stderr)
	if exitCode == 0 {
		t.Fatalf("expected non-zero exit for token save failure, got 0")
	}
	stderrOutput := stderr.String()
	if !strings.Contains(stderrOutput, "WARNING") {
		t.Fatalf("expected recovery warning on stderr, got: %s", stderrOutput)
	}
	if !strings.Contains(stderrOutput, "Snapshot URL:") {
		t.Fatalf("expected snapshot URL in recovery warning, got: %s", stderrOutput)
	}
	if !strings.Contains(stderrOutput, "Snapshot delete token:") {
		t.Fatalf("expected delete token in recovery warning, got: %s", stderrOutput)
	}
	if stdout.Len() > 0 {
		t.Fatalf("expected no stdout on token save failure, got: %s", stdout.String())
	}
}

func TestNoArgsShowsUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exitCode := run(nil, nil, &stdout, &stderr)
	if exitCode != 1 {
		t.Fatalf("exit code = %d, want 1", exitCode)
	}
	if !strings.Contains(stderr.String(), "usage") {
		t.Fatalf("expected usage message, got: %s", stderr.String())
	}
}

func TestMarkdownFromPathDefaultOutput(t *testing.T) {
	_, _, cleanup := testHarness(t)
	defer cleanup()

	content := "# Hello\n\n**bold** text"
	src := t.TempDir()
	srcPath := filepath.Join(src, "doc.md")
	if err := os.WriteFile(srcPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	exitCode := run([]string{"markdown", srcPath}, nil, &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %s", exitCode, stderr.String())
	}
	url := strings.TrimSpace(stdout.String())
	if !strings.HasPrefix(url, "https://view.test/v/") {
		t.Fatalf("stdout = %q, want URL", url)
	}
	if stderr.Len() > 0 {
		t.Fatalf("unexpected stderr: %s", stderr.String())
	}
}

func TestMarkdownPathThenOptionsCreatesTitledJSONSnapshot(t *testing.T) {
	_, server, cleanup := testHarness(t)
	defer cleanup()

	contentPath := filepath.Join(t.TempDir(), "doc.md")
	if err := os.WriteFile(contentPath, []byte("# Snapshot\n"), 0600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if exitCode := run([]string{"markdown", contentPath, "-title", "My First Snapshot", "-json"}, nil, &stdout, &stderr); exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %s", exitCode, stderr.String())
	}
	var response createOutput
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		t.Fatalf("stdout JSON error = %v, body = %s", err, stdout.String())
	}
	if response.ID == "" || response.URL == "" || response.DeleteToken == "" {
		t.Fatalf("incomplete JSON response: %+v", response)
	}
	if server.CreateCount() != 1 {
		t.Fatalf("create count = %d, want 1", server.CreateCount())
	}

	var request struct {
		Title string `json:"title"`
	}
	if err := json.Unmarshal([]byte(server.LastCreateBody()), &request); err != nil {
		t.Fatal(err)
	}
	if request.Title != "My First Snapshot" {
		t.Fatalf("title = %q, want %q", request.Title, "My First Snapshot")
	}
}

func TestParseSnapshotArgs(t *testing.T) {
	for _, tt := range []struct {
		name      string
		args      []string
		wantArgs  []string
		wantJSON  bool
		wantTitle string
		wantDesc  string
		wantErr   bool
	}{
		{name: "options before", args: []string{"--json", "--title", "Snapshot", "content.md"}, wantArgs: []string{"content.md"}, wantJSON: true, wantTitle: "Snapshot"},
		{name: "options after", args: []string{"content.md", "--json", "--title", "Snapshot"}, wantArgs: []string{"content.md"}, wantJSON: true, wantTitle: "Snapshot"},
		{name: "options interspersed", args: []string{"--title", "Snapshot", "content.md", "--json"}, wantArgs: []string{"content.md"}, wantJSON: true, wantTitle: "Snapshot"},
		{name: "single and double dash", args: []string{"-json", "--title=Snapshot", "content.md"}, wantArgs: []string{"content.md"}, wantJSON: true, wantTitle: "Snapshot"},
		{name: "equals forms", args: []string{"-json=true", "--title=Snapshot", "content.md"}, wantArgs: []string{"content.md"}, wantJSON: true, wantTitle: "Snapshot"},
		{name: "json false", args: []string{"content.md", "--json=false"}, wantArgs: []string{"content.md"}, wantTitle: ""},
		{name: "dash-prefixed string value", args: []string{"-title", "-json", "content.md"}, wantArgs: []string{"content.md"}, wantTitle: "-json"},
		{name: "dash-prefixed description value", args: []string{"content.md", "--description-file", "-description.md"}, wantArgs: []string{"content.md"}, wantDesc: "-description.md"},
		{name: "terminator and dash-prefixed operand", args: []string{"--json", "--", "-content.md"}, wantArgs: []string{"-content.md"}, wantJSON: true},
		{name: "separated bool value remains operand", args: []string{"--json", "false", "content.md"}, wantArgs: []string{"false", "content.md"}, wantJSON: true},
		{name: "unknown option", args: []string{"content.md", "--unknown"}, wantErr: true},
		{name: "missing string value", args: []string{"--title"}, wantErr: true},
		{name: "missing string value after operand", args: []string{"content.md", "--title"}, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			flags := flag.NewFlagSet("snapshot", flag.ContinueOnError)
			flags.SetOutput(io.Discard)
			jsonMode := flags.Bool("json", false, "output full JSON response")
			title := flags.String("title", "", "snapshot title")
			descFile := flags.String("description-file", "", "description path")

			args, err := parseSnapshotArgs(flags, tt.args)
			if tt.wantErr {
				if err == nil {
					t.Fatal("parseSnapshotArgs unexpectedly succeeded")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseSnapshotArgs error = %v", err)
			}
			if !equalStrings(args, tt.wantArgs) {
				t.Fatalf("operands = %#v, want %#v", args, tt.wantArgs)
			}
			if *jsonMode != tt.wantJSON || *title != tt.wantTitle || *descFile != tt.wantDesc {
				t.Fatalf("parsed flags = json:%v title:%q description:%q", *jsonMode, *title, *descFile)
			}
		})
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestSharedUploadsAcceptTrailingAndInterspersedOptions(t *testing.T) {
	for _, command := range []string{"diff", "text", "markdown", "mermaid", "html", "csv"} {
		for _, ordering := range []string{"trailing", "interspersed"} {
			t.Run(command+"/"+ordering, func(t *testing.T) {
				_, server, cleanup := testHarness(t)
				defer cleanup()

				path := filepath.Join(t.TempDir(), "content")
				if err := os.WriteFile(path, []byte("shared upload content"), 0600); err != nil {
					t.Fatal(err)
				}
				title := "Trailing"
				args := []string{command, path, "--title", title, "--json"}
				if ordering == "interspersed" {
					title = "Interspersed"
					args = []string{command, "--title", title, path, "--json"}
				}

				var stdout, stderr bytes.Buffer
				if exitCode := run(args, nil, &stdout, &stderr); exitCode != 0 {
					t.Fatalf("exit code = %d, stderr = %s", exitCode, stderr.String())
				}
				var response createOutput
				if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
					t.Fatalf("stdout JSON error = %v, body = %s", err, stdout.String())
				}
				var request struct {
					ContentType string `json:"content_type"`
					Title       string `json:"title"`
					Content     struct {
						Text     string `json:"text"`
						Filename string `json:"filename"`
					} `json:"content"`
				}
				if err := json.Unmarshal([]byte(server.LastCreateBody()), &request); err != nil {
					t.Fatal(err)
				}
				wantFilename := ""
				if command == "csv" {
					wantFilename = "content"
				}
				if request.ContentType != command || request.Title != title || request.Content.Text != "shared upload content" || request.Content.Filename != wantFilename || server.CreateCount() != 1 {
					t.Fatalf("request = %+v, create count = %d", request, server.CreateCount())
				}
			})
		}
	}
}

func TestCompareAcceptsOptionsBetweenAndAfterOperands(t *testing.T) {
	for _, ordering := range []string{"between", "after"} {
		t.Run(ordering, func(t *testing.T) {
			_, server, cleanup := testHarness(t)
			defer cleanup()

			root := t.TempDir()
			beforePath := filepath.Join(root, "before.txt")
			afterPath := filepath.Join(root, "after.txt")
			if err := os.WriteFile(beforePath, []byte("before"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(afterPath, []byte("after"), 0600); err != nil {
				t.Fatal(err)
			}
			title := "Between"
			args := []string{"compare", beforePath, "--title", title, afterPath, "--json"}
			if ordering == "after" {
				title = "After"
				args = []string{"compare", beforePath, afterPath, "--title", title, "--json"}
			}

			var stdout, stderr bytes.Buffer
			if exitCode := run(args, nil, &stdout, &stderr); exitCode != 0 {
				t.Fatalf("exit code = %d, stderr = %s", exitCode, stderr.String())
			}
			var response createOutput
			if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
				t.Fatalf("stdout JSON error = %v, body = %s", err, stdout.String())
			}
			var request struct {
				ContentType string `json:"content_type"`
				Title       string `json:"title"`
				Content     struct {
					Before struct {
						Text string `json:"text"`
					} `json:"before"`
					After struct {
						Text string `json:"text"`
					} `json:"after"`
				} `json:"content"`
			}
			if err := json.Unmarshal([]byte(server.LastCreateBody()), &request); err != nil {
				t.Fatal(err)
			}
			if request.ContentType != "compare" || request.Title != title || request.Content.Before.Text != "before" || request.Content.After.Text != "after" || server.CreateCount() != 1 {
				t.Fatalf("request = %+v, create count = %d", request, server.CreateCount())
			}
		})
	}
}

type failOnRead struct {
	read bool
}

func (r *failOnRead) Read([]byte) (int, error) {
	r.read = true
	return 0, errors.New("stdin must not be read")
}

func TestMalformedAndExtraArgsFailBeforeReadsAndSideEffects(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
	}{
		{name: "upload unknown option", args: []string{"markdown", "--unknown"}},
		{name: "upload extra operand", args: []string{"markdown", filepath.Join("missing", "content"), "extra"}},
		{name: "upload missing value after operand", args: []string{"markdown", filepath.Join("missing", "content"), "--title"}},
		{name: "compare unknown option", args: []string{"compare", "missing-before", "missing-after", "--unknown"}},
		{name: "compare extra operand", args: []string{"compare", "missing-before", "missing-after", "extra"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, server, cleanup := testHarness(t)
			defer cleanup()

			stateDir := os.Getenv("SNAPIFACT_STATE_DIR")
			tokenPath := filepath.Join(stateDir, "snapifact", "tokens", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
			if err := os.MkdirAll(filepath.Dir(tokenPath), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(tokenPath, []byte("stale-token"), 0600); err != nil {
				t.Fatal(err)
			}

			stdin := &failOnRead{}
			var stdout, stderr bytes.Buffer
			if exitCode := run(tt.args, stdin, &stdout, &stderr); exitCode == 0 {
				t.Fatal("malformed arguments unexpectedly succeeded")
			}
			if stdin.read {
				t.Fatal("stdin was read before argument validation")
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}
			if server.CreateCount() != 0 {
				t.Fatalf("create count = %d, want 0", server.CreateCount())
			}
			if content, err := os.ReadFile(tokenPath); err != nil || string(content) != "stale-token" {
				t.Fatalf("stale token changed: content=%q err=%v", content, err)
			}
		})
	}
}

func TestMarkdownFromStdinDefaultOutput(t *testing.T) {
	_, _, cleanup := testHarness(t)
	defer cleanup()

	var stdout, stderr bytes.Buffer
	exitCode := run([]string{"markdown"}, strings.NewReader("# Markdown\n\nContent"), &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %s", exitCode, stderr.String())
	}
	if !strings.HasPrefix(stdout.String(), "https://view.test/v/") {
		t.Fatalf("stdout = %q, want URL", stdout.String())
	}
}

func TestMarkdownJSONOutput(t *testing.T) {
	_, _, cleanup := testHarness(t)
	defer cleanup()

	var stdout, stderr bytes.Buffer
	exitCode := run([]string{"markdown", "--json"}, strings.NewReader("# JSON md"), &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %s", exitCode, stderr.String())
	}
	var out createOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("stdout JSON error = %v, body = %s", err, stdout.String())
	}
	if out.ID == "" || out.URL == "" || out.ExpiresAt == "" || out.DeleteToken == "" {
		t.Fatalf("incomplete JSON output: %+v", out)
	}
}

func TestMarkdownWithDescriptionFile(t *testing.T) {
	_, _, cleanup := testHarness(t)
	defer cleanup()

	root := t.TempDir()
	contentPath := filepath.Join(root, "doc.md")
	if err := os.WriteFile(contentPath, []byte("# Main content"), 0644); err != nil {
		t.Fatal(err)
	}
	descPath := filepath.Join(root, "desc.md")
	if err := os.WriteFile(descPath, []byte("## Description\n\nSome notes."), 0644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	exitCode := run([]string{"markdown", "--description-file", descPath, contentPath}, nil, &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %s", exitCode, stderr.String())
	}
	if !strings.HasPrefix(stdout.String(), "https://view.test/v/") {
		t.Fatalf("stdout = %q, want URL", stdout.String())
	}
}

func TestTextWithDescriptionFile(t *testing.T) {
	_, _, cleanup := testHarness(t)
	defer cleanup()

	root := t.TempDir()
	descPath := filepath.Join(root, "desc.md")
	if err := os.WriteFile(descPath, []byte("## Notes\n\nAbout this text."), 0644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	exitCode := run([]string{"text", "--description-file", descPath, "--json"}, strings.NewReader("text content"), &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %s", exitCode, stderr.String())
	}
	var out createOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.ID == "" {
		t.Fatal("no ID in output")
	}
}

func TestMarkdownAmbiguousStdinRejected(t *testing.T) {
	_, _, cleanup := testHarness(t)
	defer cleanup()

	// Both content and description from stdin is ambiguous
	var stdout, stderr bytes.Buffer
	exitCode := run([]string{"markdown", "--description-file", "-", "-"}, strings.NewReader("content and desc"), &stdout, &stderr)
	if exitCode == 0 {
		t.Fatalf("expected non-zero exit for ambiguous stdin, got 0")
	}
	if !strings.Contains(stderr.String(), "cannot read both content and description from stdin") {
		t.Fatalf("expected ambiguous stdin error, got: %s", stderr.String())
	}
}

func TestMarkdownWithDescriptionFromStdinNoAmbiguity(t *testing.T) {
	_, _, cleanup := testHarness(t)
	defer cleanup()

	// Content from file, description from stdin — OK
	root := t.TempDir()
	contentPath := filepath.Join(root, "doc.md")
	if err := os.WriteFile(contentPath, []byte("# From file"), 0644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	// stdin provides description, content is from file
	exitCode := run([]string{"markdown", "--description-file", "-", contentPath}, strings.NewReader("## Description from stdin"), &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %s", exitCode, stderr.String())
	}
	if !strings.HasPrefix(stdout.String(), "https://view.test/v/") {
		t.Fatalf("stdout = %q, want URL", stdout.String())
	}
}

func TestUnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exitCode := run([]string{"unknown"}, strings.NewReader(""), &stdout, &stderr)
	if exitCode != 1 {
		t.Fatalf("exit code = %d, want 1", exitCode)
	}
	if !strings.Contains(stderr.String(), "unknown") {
		t.Fatalf("expected error message, got: %s", stderr.String())
	}
}

func TestDeleteWithoutArgsShowsUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exitCode := run([]string{"delete"}, nil, &stdout, &stderr)
	if exitCode != 1 {
		t.Fatalf("exit code = %d, want 1", exitCode)
	}
	if !strings.Contains(stderr.String(), "usage") {
		t.Fatalf("expected usage message, got: %s", stderr.String())
	}
}

func TestDeleteWithExtraOperandFailsBeforeSideEffects(t *testing.T) {
	_, server, cleanup := testHarness(t)
	defer cleanup()

	snapshotID, token := server.SeedSnapshot(t)
	stateDir := os.Getenv("SNAPIFACT_STATE_DIR")
	tokenDir := filepath.Join(stateDir, "snapifact", "tokens")
	if err := os.MkdirAll(tokenDir, 0700); err != nil {
		t.Fatal(err)
	}
	tokenPath := filepath.Join(tokenDir, snapshotID)
	if err := os.WriteFile(tokenPath, []byte(token), 0600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if exitCode := run([]string{"delete", snapshotID, "extra"}, nil, &stdout, &stderr); exitCode == 0 {
		t.Fatal("delete with extra operand unexpectedly succeeded")
	}
	if !strings.Contains(strings.ToLower(stderr.String()), "usage") {
		t.Fatalf("stderr = %q, want usage error", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if server.RequestCount() != 0 {
		t.Fatalf("HTTP request count = %d, want 0", server.RequestCount())
	}
	if content, err := os.ReadFile(tokenPath); err != nil || string(content) != token {
		t.Fatalf("token changed: content=%q err=%v", content, err)
	}
}

func TestGlobalHelpContract(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if exitCode := run([]string{"--help"}, nil, &stdout, &stderr); exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %s", exitCode, stderr.String())
	}
	got := stdout.String()
	for _, fragment := range []string{
		"usage: snapifact <command> [options]",
		"  diff       upload a unified diff snapshot",
		"  compare    compare two UTF-8 files",
		"  image      upload a PNG or JPEG image snapshot",
		"stdin",
		"Options may appear before or after operands",
		"-- before a dash-prefixed path",
		"--title <text>",
		"--description-file <path|->",
		"--json",
		"--description-file - reads the description from stdin",
		"cannot be combined with content also read from stdin",
		"snapifact compare before.txt after.txt",
		"snapifact delete kpm2q6xxyegw5czekhga",
		"snapifact <command> --help",
	} {
		if !strings.Contains(got, fragment) {
			t.Fatalf("global help = %q, missing %q", got, fragment)
		}
	}
}

func TestVersionOutputIsExact(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if exitCode := run([]string{"--version"}, nil, &stdout, &stderr); exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %s", exitCode, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "snapifact dev" {
		t.Fatalf("version output = %q, want %q", got, "snapifact dev")
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr = %q", stderr.String())
	}
}

func TestCLIVersionUsesLinkerValue(t *testing.T) {
	previous := version
	version = "v1.2.3"
	defer func() { version = previous }()

	if got := cliVersion(); got != "v1.2.3" {
		t.Fatalf("cliVersion() = %q, want %q", got, "v1.2.3")
	}
}

func TestVersionCommandOutputIsExact(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if exitCode := run([]string{"version"}, nil, &stdout, &stderr); exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %s", exitCode, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "snapifact dev" {
		t.Fatalf("version output = %q, want %q", got, "snapifact dev")
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr = %q", stderr.String())
	}
}

func TestVersionHelpContract(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if exitCode := run([]string{"version", "--help"}, nil, &stdout, &stderr); exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %s", exitCode, stderr.String())
	}
	got := stdout.String() + stderr.String()
	for _, fragment := range []string{
		"Usage: snapifact version",
		"Arguments: none",
		"snapifact version --help",
	} {
		if !strings.Contains(got, fragment) {
			t.Fatalf("version help = %q, missing %q", got, fragment)
		}
	}
}

func TestEveryCommandHelpContract(t *testing.T) {
	sharedFragments := []string{
		"[path]",
		"Omit [path] or use - to read content from stdin",
		"Options may appear before or after operands",
		"-- before a dash-prefixed path",
		"--title <text>",
		"--description-file <path|->",
		"--json",
		"printf",
		"--description-file -",
	}
	for _, command := range []string{"diff", "text", "markdown", "mermaid", "html", "csv"} {
		t.Run(command, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if exitCode := run([]string{command, "--help"}, nil, &stdout, &stderr); exitCode != 0 {
				t.Fatalf("exit code = %d, stderr = %s", exitCode, stderr.String())
			}
			got := stdout.String() + stderr.String()
			for _, fragment := range append([]string{"Usage: snapifact " + command + " [options] [path]"}, sharedFragments...) {
				if !strings.Contains(got, fragment) {
					t.Fatalf("help output = %q, missing %q", got, fragment)
				}
			}
			if command == "text" && !strings.Contains(got, "UTF-8 plain text or source code") {
				t.Fatalf("text help = %q, missing UTF-8 text description", got)
			}
		})
	}

	t.Run("image", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		if exitCode := run([]string{"image", "--help"}, nil, &stdout, &stderr); exitCode != 0 {
			t.Fatalf("exit code = %d, stderr = %s", exitCode, stderr.String())
		}
		got := stdout.String() + stderr.String()
		for _, fragment := range []string{
			"Usage: snapifact image [options] [path|-]",
			"zero or one PNG or JPEG file",
			"Omit [path] or use - to read a PNG or JPEG from stdin",
			"Image content is limited to 8 MiB",
			"SNAPIFACT_API_KEY is optional and applies to create requests, including image creation; never sent on delete, view, or raw",
		} {
			if !strings.Contains(got, fragment) {
				t.Fatalf("image help = %q, missing %q", got, fragment)
			}
		}
	})

	t.Run("compare", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		if exitCode := run([]string{"compare", "--help"}, nil, &stdout, &stderr); exitCode != 0 {
			t.Fatalf("exit code = %d, stderr = %s", exitCode, stderr.String())
		}
		got := stdout.String() + stderr.String()
		for _, fragment := range []string{
			"Usage: snapifact compare [options] <before-file> <after-file>",
			"Exactly two UTF-8 file operands are required",
			"The operand - is a file path, not a stdin shorthand",
			"Options may appear before or after operands",
			"-- before dash-prefixed paths",
			"--title <text>",
			"--description-file <path|->",
			"--json",
			"snapifact compare before.txt after.txt",
		} {
			if !strings.Contains(got, fragment) {
				t.Fatalf("compare help = %q, missing %q", got, fragment)
			}
		}
	})

	t.Run("delete", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		if exitCode := run([]string{"delete", "--help"}, nil, &stdout, &stderr); exitCode != 0 {
			t.Fatalf("exit code = %d, stderr = %s", exitCode, stderr.String())
		}
		got := stdout.String() + stderr.String()
		for _, fragment := range []string{
			"Usage: snapifact delete <id-or-url>",
			"Exactly one snapshot ID or URL is required",
			"snapifact delete kpm2q6xxyegw5czekhga",
			"snapifact delete https://view.test/v/kpm2q6xxyegw5czekhga",
		} {
			if !strings.Contains(got, fragment) {
				t.Fatalf("delete help = %q, missing %q", got, fragment)
			}
		}
	})
}

func TestHelpHasNoFileStdinTokenOrHTTPSideEffects(t *testing.T) {
	server := newContractTestServer(t)
	defer server.Close()
	stateDir := filepath.Join(t.TempDir(), "state")
	t.Setenv("SNAPIFACT_SERVER", server.URL)
	t.Setenv("SNAPIFACT_STATE_DIR", stateDir)

	commands := [][]string{{"--help"}, {"version", "--help"}, {"delete", "--help"}}
	for _, command := range []string{"diff", "compare", "text", "markdown", "mermaid", "html", "csv", "image"} {
		commands = append(commands, []string{command, "missing-file", "--help"})
	}
	for _, args := range commands {
		t.Run(strings.Join(args, "/"), func(t *testing.T) {
			stdin := &failOnRead{}
			var stdout, stderr bytes.Buffer
			if exitCode := run(args, stdin, &stdout, &stderr); exitCode != 0 {
				t.Fatalf("exit code = %d, stdout = %q, stderr = %q", exitCode, stdout.String(), stderr.String())
			}
			if stdin.read {
				t.Fatal("help read stdin")
			}
			if server.RequestCount() != 0 {
				t.Fatalf("help made %d HTTP requests", server.RequestCount())
			}
		})
	}
	if _, err := os.Stat(stateDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("help touched state directory: stat error = %v", err)
	}
}

func TestHTTPContractHandlerCapturesCreateRequest(t *testing.T) {
	server := newContractTestServer(t)
	defer server.Close()

	wrongType, err := http.Post(server.URL+"/v1/snapshots", "text/plain", strings.NewReader(`{"content_type":"text","content":{"text":"contract"}}`))
	if err != nil {
		t.Fatal(err)
	}
	wrongType.Body.Close()
	if wrongType.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("wrong content type status = %d, want %d", wrongType.StatusCode, http.StatusUnsupportedMediaType)
	}

	resp, err := http.Post(server.URL+"/v1/snapshots", "application/json", strings.NewReader(`{"content_type":"text","content":{"text":"contract"}}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	if got := server.LastCreateBody(); !strings.Contains(got, `"content_type":"text"`) {
		t.Fatalf("captured create body = %q", got)
	}
}
