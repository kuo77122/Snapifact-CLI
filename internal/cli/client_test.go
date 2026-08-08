package cli

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
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

func TestCreateBinarySnapshotUsesExactMultipartContract(t *testing.T) {
	const apiKey = "binary-create-key"
	content := []byte("\x89PNG\r\n\x1a\nimage bytes")
	var requestCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if r.Method != http.MethodPost || r.URL.Path != "/v1/snapshots/binary" {
			t.Fatalf("request = %s %s, want POST /v1/snapshots/binary", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("X-Snapifact-API-Key"); got != apiKey {
			t.Fatalf("API key = %q, want %q", got, apiKey)
		}
		mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "multipart/form-data" || params["boundary"] == "" {
			t.Fatalf("content type = %q, want multipart/form-data with boundary", r.Header.Get("Content-Type"))
		}
		reader := multipart.NewReader(r.Body, params["boundary"])
		metadata, err := reader.NextPart()
		if err != nil {
			t.Fatal(err)
		}
		if metadata.FormName() != "metadata" || metadata.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("metadata headers = %v", metadata.Header)
		}
		var gotMetadata struct {
			ContentType         string `json:"content_type"`
			Title               string `json:"title"`
			DescriptionMarkdown string `json:"description_markdown"`
			Filename            string `json:"filename"`
		}
		if err := json.NewDecoder(metadata).Decode(&gotMetadata); err != nil {
			t.Fatal(err)
		}
		if gotMetadata.ContentType != "image" || gotMetadata.Title != "Review" || gotMetadata.DescriptionMarkdown != "## Notes" || gotMetadata.Filename != "photo.png" {
			t.Fatalf("metadata = %+v", gotMetadata)
		}
		contentPart, err := reader.NextPart()
		if err != nil {
			t.Fatal(err)
		}
		if contentPart.FormName() != "content" || contentPart.FileName() != "photo.png" || contentPart.Header.Get("Content-Type") != "application/octet-stream" {
			t.Fatalf("content headers = %v", contentPart.Header)
		}
		gotContent, err := io.ReadAll(contentPart)
		if err != nil {
			t.Fatal(err)
		}
		if string(gotContent) != string(content) {
			t.Fatalf("content = %q, want %q", gotContent, content)
		}
		if _, err := reader.NextPart(); err != io.EOF {
			t.Fatalf("multipart parts have trailing data: %v", err)
		}
		writeCreateResponse(w)
	}))
	defer server.Close()
	t.Setenv("SNAPIFACT_API_KEY", apiKey)

	response, err := CreateBinarySnapshot(server.URL, "Review", content, "photo.png", "## Notes")
	if err != nil {
		t.Fatal(err)
	}
	if response.URL == "" || requestCount != 1 {
		t.Fatalf("response=%+v request count=%d", response, requestCount)
	}
}

func TestCreateBinarySnapshotOmitsAPIKeyWhenUnset(t *testing.T) {
	var gotKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-Snapifact-API-Key")
		writeCreateResponse(w)
	}))
	defer server.Close()
	t.Setenv("SNAPIFACT_API_KEY", "")

	if _, err := CreateBinarySnapshot(server.URL, "", []byte("\xff\xd8\xffjpeg"), "", ""); err != nil {
		t.Fatal(err)
	}
	if gotKey != "" {
		t.Fatalf("API key = %q, want absent", gotKey)
	}
}

func TestCreateBinarySnapshotBoundsInputBeforeHTTP(t *testing.T) {
	var requestCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
	}))
	defer server.Close()

	_, err := CreateBinarySnapshot(server.URL, "", append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, maxImageContentSize)...), "", "")
	if err == nil || !strings.Contains(err.Error(), "8 MiB") {
		t.Fatalf("error = %v, want 8 MiB limit error", err)
	}
	if requestCount != 0 {
		t.Fatalf("request count = %d, want 0", requestCount)
	}
}

func TestCreateBinarySnapshotReturnsStructuredErrorWithoutRetry(t *testing.T) {
	var requestCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		writeContractError(w, http.StatusBadRequest, "invalid_image", "not an image", "binary-request-id")
	}))
	defer server.Close()

	_, err := CreateBinarySnapshot(server.URL, "", []byte("\x89PNG\r\n\x1a\n"), "", "")
	var response *ErrorResponse
	if !errors.As(err, &response) || response.Code != "invalid_image" || response.RequestID != "binary-request-id" {
		t.Fatalf("error = %#v, want structured invalid_image error", err)
	}
	if requestCount != 1 {
		t.Fatalf("request count = %d, want 1", requestCount)
	}
}

func writeCreateResponse(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"id": "kpm2q6xxyegw5czekhga", "url": "https://view.test/v/kpm2q6xxyegw5czekhga",
		"expires_at": "2026-08-13T00:00:00Z", "delete_token": strings.Repeat("A", 43),
	})
}

func writeContractError(w http.ResponseWriter, status int, code, message, requestID string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "message": message, "request_id": requestID})
}
