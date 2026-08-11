package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"reflect"
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
	var gotBody struct {
		ContentType string `json:"content_type"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-Snapifact-API-Key")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
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
	if gotBody.ContentType != "text" {
		t.Fatalf("content_type = %q, want text", gotBody.ContentType)
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

func TestCreateSnapshotWithPasswordSendsPasswordOnceAndPreservesAPIKey(t *testing.T) {
	const (
		apiKey   = "create-api-key"
		password = "correct horse battery staple"
	)
	var requestCount int
	var request map[string]any
	var gotKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		gotKey = r.Header.Get("X-Snapifact-API-Key")
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		writeCreateResponse(w)
	}))
	defer server.Close()
	t.Setenv("SNAPIFACT_API_KEY", apiKey)

	if _, err := CreateSnapshotWithDescriptionAndFilenameAndPassword(server.URL, "markdown", "Review", "body", "", "", password); err != nil {
		t.Fatal(err)
	}
	if requestCount != 1 || gotKey != apiKey {
		t.Fatalf("request count = %d, API key = %q", requestCount, gotKey)
	}
	if request["password"] != password {
		t.Fatalf("password = %#v, want %q", request["password"], password)
	}
}

func TestCompatibilityJSONBuildersOmitPassword(t *testing.T) {
	for name, body := range map[string]io.Reader{
		"single":  buildCreateBody("markdown", "", "body", "", ""),
		"compare": buildCompareBody("", "before", "before.txt", "after", "after.txt", ""),
	} {
		raw, err := io.ReadAll(body)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), `"password"`) {
			t.Fatalf("%s body = %s, contains password", name, raw)
		}
	}
}

func TestCreateCompareSnapshotWithPasswordIncludesOneTopLevelPassword(t *testing.T) {
	var request map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Count(string(body), `"password"`) != 1 {
			t.Fatalf("request body = %s, want one password field", body)
		}
		if err := json.Unmarshal(body, &request); err != nil {
			t.Fatal(err)
		}
		writeCreateResponse(w)
	}))
	defer server.Close()
	t.Setenv("SNAPIFACT_API_KEY", "key")

	if _, err := CreateCompareSnapshotWithDescriptionAndPassword(server.URL, "Review", "before", "before.txt", "after", "after.txt", "", "unicode-password-✓"); err != nil {
		t.Fatal(err)
	}
	if request["password"] != "unicode-password-✓" {
		t.Fatalf("password = %#v", request["password"])
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
		metadataRaw, err := io.ReadAll(metadata)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(metadataRaw), `"password"`) {
			t.Fatalf("metadata = %s, contains password", metadataRaw)
		}
		var gotMetadata struct {
			ContentType         string `json:"content_type"`
			Title               string `json:"title"`
			DescriptionMarkdown string `json:"description_markdown"`
			Filename            string `json:"filename"`
		}
		if err := json.Unmarshal(metadataRaw, &gotMetadata); err != nil {
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

func TestCreateBinarySnapshotWithPasswordPreservesPartOrderAndMetadata(t *testing.T) {
	const password = "correct horse battery staple"
	content := []byte("image bytes")
	var requestCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			t.Fatal(err)
		}
		reader := multipart.NewReader(r.Body, params["boundary"])
		metadata, err := reader.NextPart()
		if err != nil {
			t.Fatal(err)
		}
		var gotMetadata map[string]any
		if err := json.NewDecoder(metadata).Decode(&gotMetadata); err != nil {
			t.Fatal(err)
		}
		if gotMetadata["password"] != password {
			t.Fatalf("metadata password = %#v", gotMetadata["password"])
		}
		contentPart, err := reader.NextPart()
		if err != nil {
			t.Fatal(err)
		}
		gotContent, err := io.ReadAll(contentPart)
		if err != nil || string(gotContent) != string(content) {
			t.Fatalf("content = %q, error = %v", gotContent, err)
		}
		if _, err := reader.NextPart(); err != io.EOF {
			t.Fatalf("multipart parts have trailing data: %v", err)
		}
		writeCreateResponse(w)
	}))
	defer server.Close()
	t.Setenv("SNAPIFACT_API_KEY", "key")

	if _, err := CreateBinarySnapshotWithPassword(server.URL, "Review", content, "photo.png", "", password); err != nil {
		t.Fatal(err)
	}
	if requestCount != 1 {
		t.Fatalf("request count = %d, want 1", requestCount)
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

func TestCreatePDFSnapshotUsesExactMultipartContract(t *testing.T) {
	const (
		apiKey   = "pdf-create-key"
		password = "pdf-password"
	)
	commentsEnabled := true
	content := []byte{'%', 'P', 'D', 'F', '\n', 0, 0xff, 0x80}
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
		var gotMetadata map[string]any
		if err := json.NewDecoder(metadata).Decode(&gotMetadata); err != nil {
			t.Fatal(err)
		}
		wantMetadata := map[string]any{
			"content_type":         "pdf",
			"title":                "Review",
			"description_markdown": "Notes",
			"filename":             "report.pdf",
			"comments_enabled":     true,
			"password":             password,
		}
		if !reflect.DeepEqual(gotMetadata, wantMetadata) {
			t.Fatalf("metadata = %#v, want %#v", gotMetadata, wantMetadata)
		}
		contentPart, err := reader.NextPart()
		if err != nil {
			t.Fatal(err)
		}
		if contentPart.FormName() != "content" || contentPart.FileName() != "" || contentPart.Header.Get("Content-Type") != "application/pdf" {
			t.Fatalf("content headers = %v", contentPart.Header)
		}
		gotContent, err := io.ReadAll(contentPart)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(gotContent, content) {
			t.Fatalf("content = %v, want %v", gotContent, content)
		}
		if _, err := reader.NextPart(); err != io.EOF {
			t.Fatalf("multipart parts have trailing data: %v", err)
		}
		writeCreateResponse(w)
	}))
	defer server.Close()

	response, err := CreatePDFSnapshot(server.URL, "Review", content, "report.pdf", "Notes", &commentsEnabled, password, apiKey)
	if err != nil {
		t.Fatal(err)
	}
	if response.URL == "" || requestCount != 1 {
		t.Fatalf("response=%+v request count=%d", response, requestCount)
	}
}

func TestCreatePDFSnapshotPreservesCommentsPresence(t *testing.T) {
	for name, commentsEnabled := range map[string]*bool{
		"absent": nil,
		"true":   ptr(true),
		"false":  ptr(false),
	} {
		t.Run(name, func(t *testing.T) {
			var gotMetadata map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
				if err != nil {
					t.Fatal(err)
				}
				part, err := multipart.NewReader(r.Body, params["boundary"]).NextPart()
				if err != nil {
					t.Fatal(err)
				}
				if err := json.NewDecoder(part).Decode(&gotMetadata); err != nil {
					t.Fatal(err)
				}
				writeCreateResponse(w)
			}))
			defer server.Close()

			if _, err := CreatePDFSnapshot(server.URL, "", []byte("%PDF"), "", "", commentsEnabled, "", ""); err != nil {
				t.Fatal(err)
			}
			value, present := gotMetadata["comments_enabled"]
			if commentsEnabled == nil {
				if present {
					t.Fatalf("comments_enabled = %#v, want absent", value)
				}
				return
			}
			if !present || value != *commentsEnabled {
				t.Fatalf("comments_enabled = %#v (present=%t), want %t", value, present, *commentsEnabled)
			}
		})
	}
}

func ptr(value bool) *bool {
	return &value
}

func TestCreatePDFSnapshotRejectsLocalBoundsBeforeHTTP(t *testing.T) {
	var requestCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		writeCreateResponse(w)
	}))
	defer server.Close()

	if _, err := CreatePDFSnapshot(server.URL, "", make([]byte, MaxPDFContentSize), "", "", nil, "", ""); err != nil {
		t.Fatalf("exact content boundary = %v", err)
	}
	if requestCount != 1 {
		t.Fatalf("request count after exact content = %d, want 1", requestCount)
	}

	if _, err := CreatePDFSnapshot(server.URL, "", make([]byte, MaxPDFContentSize+1), "", "", nil, "", ""); err == nil || !strings.Contains(err.Error(), "16 MiB") {
		t.Fatalf("oversized content error = %v, want 16 MiB limit", err)
	}
	if requestCount != 1 {
		t.Fatalf("request count after oversized content = %d, want 1", requestCount)
	}

	if _, err := CreatePDFSnapshot(server.URL, strings.Repeat("x", MaxPDFWireSize), nil, "", "", nil, "", ""); err == nil || !strings.Contains(err.Error(), "17 MiB") {
		t.Fatalf("oversized wire error = %v, want 17 MiB limit", err)
	}
	if requestCount != 1 {
		t.Fatalf("request count after wire bound = %d, want 1", requestCount)
	}
}

func TestCreatePDFSnapshotAcceptsExactWireBoundary(t *testing.T) {
	var requestCount int
	var lastBodyLength int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		lastBodyLength = len(body)
		writeCreateResponse(w)
	}))
	defer server.Close()

	if _, err := CreatePDFSnapshot(server.URL, "x", nil, "", "", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	perTitleByteOverhead := lastBodyLength - 1
	titleLength := MaxPDFWireSize - perTitleByteOverhead
	if _, err := CreatePDFSnapshot(server.URL, strings.Repeat("x", titleLength), nil, "", "", nil, "", ""); err != nil {
		t.Fatalf("exact wire boundary = %v", err)
	}
	if lastBodyLength != MaxPDFWireSize {
		t.Fatalf("wire body length = %d, want %d", lastBodyLength, MaxPDFWireSize)
	}
	if _, err := CreatePDFSnapshot(server.URL, strings.Repeat("x", titleLength+1), nil, "", "", nil, "", ""); err == nil || !strings.Contains(err.Error(), "17 MiB") {
		t.Fatalf("over-boundary error = %v, want 17 MiB limit", err)
	}
	if requestCount != 2 {
		t.Fatalf("request count = %d, want 2", requestCount)
	}
}

func TestCreatePDFSnapshotReturnsFixedServerErrorWithoutRetry(t *testing.T) {
	var requestCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		writeContractError(w, http.StatusBadRequest, "invalid_pdf", "PDF failed structural validation", "pdf-request-id")
	}))
	defer server.Close()

	_, err := CreatePDFSnapshot(server.URL, "", []byte("not a PDF"), "", "", nil, "", "")
	var response *ErrorResponse
	if !errors.As(err, &response) || response.Code != "invalid_pdf" || response.Message != "PDF failed structural validation" || response.RequestID != "pdf-request-id" {
		t.Fatalf("error = %#v, want fixed invalid_pdf error", err)
	}
	if requestCount != 1 {
		t.Fatalf("request count = %d, want 1", requestCount)
	}
}

func TestCreatePDFSnapshotDoesNotRetryAmbiguousTransportFailure(t *testing.T) {
	var requestCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
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

	if _, err := CreatePDFSnapshot(server.URL, "", []byte("%PDF"), "", "", nil, "", ""); err == nil {
		t.Fatal("ambiguous transport failure unexpectedly succeeded")
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
