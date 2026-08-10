package main

import (
	"bytes"
	"encoding/json"
	"io"
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

type imageRequest struct {
	path       string
	apiKey     string
	deleteKey  string
	filename   string
	content    []byte
	metadata   map[string]string
	requestNum atomic.Int32
}

func newImageServer(t *testing.T, tier string, onDelete func(*http.Request)) (*httptest.Server, *imageRequest) {
	t.Helper()
	got := &imageRequest{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.requestNum.Add(1)
		if r.Method == http.MethodDelete {
			got.deleteKey = r.Header.Get("X-Snapifact-API-Key")
			if onDelete != nil {
				onDelete(r)
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/v1/snapshots/binary" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		got.path = r.URL.Path
		got.apiKey = r.Header.Get("X-Snapifact-API-Key")
		mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "multipart/form-data" {
			t.Errorf("request content type = %q, want multipart/form-data", r.Header.Get("Content-Type"))
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		reader := multipart.NewReader(r.Body, params["boundary"])
		metadataPart, err := reader.NextPart()
		if err != nil {
			t.Errorf("metadata part: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if metadataPart.FormName() != "metadata" || metadataPart.Header.Get("Content-Type") != "application/json" {
			t.Errorf("metadata headers = %v", metadataPart.Header)
		}
		if err := json.NewDecoder(metadataPart).Decode(&got.metadata); err != nil {
			t.Errorf("metadata: %v", err)
		}
		contentPart, err := reader.NextPart()
		if err != nil {
			t.Errorf("content part: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		got.filename = contentPart.FileName()
		got.content, _ = io.ReadAll(contentPart)
		if _, err := reader.NextPart(); err != io.EOF {
			t.Errorf("multipart has extra parts: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"id": "kpm2q6xxyegw5czekhga", "url": "https://view.test/v/kpm2q6xxyegw5czekhga",
			"expires_at": "2026-08-13T00:00:00Z", "delete_token": strings.Repeat("A", 43), "tier": tier,
		})
	}))
	return server, got
}

func TestImageFromPathAndStdinUsesBinaryEndpoint(t *testing.T) {
	image := []byte("\x89PNG\r\n\x1a\nimage bytes")
	path := filepath.Join(t.TempDir(), "photo.png")
	if err := os.WriteFile(path, image, 0600); err != nil {
		t.Fatal(err)
	}

	for name, input := range map[string]struct {
		args  []string
		stdin string
		file  string
	}{
		"path":          {args: []string{"image", path}, file: "photo.png"},
		"explicit dash": {args: []string{"image", "-"}, stdin: string(image)},
		"omitted path":  {args: []string{"image"}, stdin: string(image)},
	} {
		t.Run(name, func(t *testing.T) {
			server, got := newImageServer(t, "", nil)
			defer server.Close()
			t.Setenv("SNAPIFACT_SERVER", server.URL)
			t.Setenv("SNAPIFACT_STATE_DIR", t.TempDir())

			var stdout, stderr bytes.Buffer
			if exitCode := run(input.args, strings.NewReader(input.stdin), &stdout, &stderr); exitCode != 0 {
				t.Fatalf("exit code = %d, stderr = %s", exitCode, stderr.String())
			}
			if !strings.HasPrefix(strings.TrimSpace(stdout.String()), "https://view.test/v/") || stderr.Len() != 0 {
				t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
			if got.path != "/v1/snapshots/binary" || string(got.content) != string(image) || got.metadata["content_type"] != "image" {
				t.Fatalf("request path=%q metadata=%v content=%q", got.path, got.metadata, got.content)
			}
			if got.filename != input.file || got.metadata["filename"] != input.file {
				t.Fatalf("filename=%q metadata filename=%q, want %q", got.filename, got.metadata["filename"], input.file)
			}
		})
	}
}

func TestImageOptionsCanFollowPathAndProduceJSON(t *testing.T) {
	imagePath := filepath.Join(t.TempDir(), "review.jpeg")
	if err := os.WriteFile(imagePath, []byte("\xff\xd8\xffjpeg"), 0600); err != nil {
		t.Fatal(err)
	}
	descriptionPath := filepath.Join(t.TempDir(), "description.md")
	if err := os.WriteFile(descriptionPath, []byte("## Image notes\n"), 0600); err != nil {
		t.Fatal(err)
	}
	server, got := newImageServer(t, "", nil)
	defer server.Close()
	t.Setenv("SNAPIFACT_SERVER", server.URL)
	t.Setenv("SNAPIFACT_STATE_DIR", t.TempDir())

	var stdout, stderr bytes.Buffer
	args := []string{"image", imagePath, "--title", "Review", "--description-file", descriptionPath, "--json"}
	if exitCode := run(args, nil, &stdout, &stderr); exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %s", exitCode, stderr.String())
	}
	var output createOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("stdout JSON = %q: %v", stdout.String(), err)
	}
	if output.ID == "" || got.metadata["title"] != "Review" || got.metadata["description_markdown"] != "## Image notes\n" || got.metadata["filename"] != "review.jpeg" {
		t.Fatalf("output=%+v metadata=%v", output, got.metadata)
	}
}

func TestImageRejectsInvalidAndOversizedInputBeforeHTTP(t *testing.T) {
	oversizedPath := filepath.Join(t.TempDir(), "large.png")
	oversized := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 8<<20)...)
	if err := os.WriteFile(oversizedPath, oversized, 0600); err != nil {
		t.Fatal(err)
	}
	for name, input := range map[string]struct {
		args  []string
		stdin string
	}{
		"unreadable path": {args: []string{"image", filepath.Join(t.TempDir(), "not-image")}},
		"oversized path":  {args: []string{"image", oversizedPath}},
		"oversized stdin": {args: []string{"image"}, stdin: string(oversized)},
	} {
		t.Run(name, func(t *testing.T) {
			server, got := newImageServer(t, "", nil)
			defer server.Close()
			t.Setenv("SNAPIFACT_SERVER", server.URL)
			t.Setenv("SNAPIFACT_STATE_DIR", t.TempDir())
			var stdout, stderr bytes.Buffer
			if exitCode := run(input.args, strings.NewReader(input.stdin), &stdout, &stderr); exitCode == 0 {
				t.Fatal("invalid image unexpectedly succeeded")
			}
			if stdout.Len() != 0 || got.requestNum.Load() != 0 {
				t.Fatalf("stdout=%q request count=%d", stdout.String(), got.requestNum.Load())
			}
		})
	}
}

func TestImageInvalidBytesReachServerAndPreserveStructuredError(t *testing.T) {
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/snapshots/binary" {
			t.Fatalf("request = %s %s, want POST /v1/snapshots/binary", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"code": "invalid_image", "message": "server rejected image", "request_id": "image-request-id",
		})
	}))
	defer server.Close()
	t.Setenv("SNAPIFACT_SERVER", server.URL)
	t.Setenv("SNAPIFACT_STATE_DIR", t.TempDir())

	var stdout, stderr bytes.Buffer
	if exitCode := run([]string{"image"}, strings.NewReader("not a PNG or JPEG"), &stdout, &stderr); exitCode == 0 {
		t.Fatal("server image error unexpectedly succeeded")
	}
	var output errorOutput
	if err := json.Unmarshal(stderr.Bytes(), &output); err != nil {
		t.Fatalf("stderr JSON = %q: %v", stderr.String(), err)
	}
	if output.Code != "invalid_image" || output.Message != "server rejected image" || output.RequestID != "image-request-id" {
		t.Fatalf("structured error = %+v", output)
	}
	if stdout.Len() != 0 || requestCount.Load() != 1 {
		t.Fatalf("stdout=%q request count=%d, want one request and no output", stdout.String(), requestCount.Load())
	}
}

func TestImageHelpHasNoSideEffects(t *testing.T) {
	server, got := newImageServer(t, "", nil)
	defer server.Close()
	stateDir := filepath.Join(t.TempDir(), "state")
	t.Setenv("SNAPIFACT_SERVER", server.URL)
	t.Setenv("SNAPIFACT_STATE_DIR", stateDir)

	var stdout, stderr bytes.Buffer
	if exitCode := run([]string{"image", "missing.png", "--help"}, nil, &stdout, &stderr); exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %s", exitCode, stderr.String())
	}
	if !strings.Contains(stdout.String()+stderr.String(), "Usage: snapifact image") || got.requestNum.Load() != 0 {
		t.Fatalf("help=%q request count=%d", stdout.String()+stderr.String(), got.requestNum.Load())
	}
	if _, err := os.Stat(stateDir); !os.IsNotExist(err) {
		t.Fatalf("help touched state directory: %v", err)
	}
}

func TestImageAPIKeyTierAndDowngradeCompensation(t *testing.T) {
	for _, tier := range []string{"basic", "pro", "admin", "anonymous"} {
		t.Run(tier, func(t *testing.T) {
			var deleteKey string
			server, got := newImageServer(t, tier, func(r *http.Request) { deleteKey = r.Header.Get("X-Snapifact-API-Key") })
			defer server.Close()
			t.Setenv("SNAPIFACT_SERVER", server.URL)
			t.Setenv("SNAPIFACT_STATE_DIR", t.TempDir())
			t.Setenv("SNAPIFACT_API_KEY", "image-secret")

			var stdout, stderr bytes.Buffer
			exitCode := run([]string{"image"}, strings.NewReader("\xff\xd8\xffjpeg"), &stdout, &stderr)
			if tier == "anonymous" {
				if exitCode == 0 || stdout.Len() != 0 || deleteKey != "" {
					t.Fatalf("downgrade exit=%d stdout=%q delete key=%q", exitCode, stdout.String(), deleteKey)
				}
			} else if exitCode != 0 || !strings.HasPrefix(strings.TrimSpace(stdout.String()), "https://view.test/v/") {
				t.Fatalf("accepted tier exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
			}
			if got.apiKey != "image-secret" || strings.Contains(stdout.String()+stderr.String(), "image-secret") {
				t.Fatalf("create key/output mismatch: key=%q stdout=%q stderr=%q", got.apiKey, stdout.String(), stderr.String())
			}
		})
	}
}

func TestImageErrorRedactsAPIKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "invalid_image", "message": "bad image-secret", "request_id": "request"})
	}))
	defer server.Close()
	t.Setenv("SNAPIFACT_SERVER", server.URL)
	t.Setenv("SNAPIFACT_STATE_DIR", t.TempDir())
	t.Setenv("SNAPIFACT_API_KEY", "image-secret")

	var stdout, stderr bytes.Buffer
	if exitCode := run([]string{"image"}, strings.NewReader("\x89PNG\r\n\x1a\n"), &stdout, &stderr); exitCode == 0 {
		t.Fatal("server error unexpectedly succeeded")
	}
	if stdout.Len() != 0 || strings.Contains(stdout.String()+stderr.String(), "image-secret") {
		t.Fatalf("error output leaked secret: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}
