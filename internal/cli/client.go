package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"strings"
	"time"
)

// ServerURL returns the API server URL, defaulting to the production
// endpoint. The SNAPIFACT_SERVER env var is used as a test seam.
func ServerURL() string {
	if u := strings.TrimRight(os.Getenv("SNAPIFACT_SERVER"), "/"); u != "" {
		return u
	}
	return "https://api.snapifact.dev"
}

// CreateResponse is the JSON body returned by POST /v1/snapshots.
type CreateResponse struct {
	ID          string `json:"id"`
	URL         string `json:"url"`
	ExpiresAt   string `json:"expires_at"`
	DeleteToken string `json:"delete_token"`
	Tier        string `json:"tier,omitempty"`
}

// ErrorResponse is the JSON error body returned by the server.
type ErrorResponse struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

var errNoToken = errors.New("delete token not found")

// MaxImageContentSize is the fixed backend limit for image content.
const MaxImageContentSize = 8 << 20

const maxImageContentSize = MaxImageContentSize

// CreateSnapshot sends a file content to the server and returns the response.
// It does NOT retry on timeout — the caller handles that.
func CreateSnapshot(serverURL, title, content string) (*CreateResponse, error) {
	return CreateSnapshotWithDescription(serverURL, "file", title, content, "")
}

// DeleteSnapshot sends a DELETE request to the server. It returns nil on
// both 204 (success) and 404 (already gone).
func DeleteSnapshot(serverURL, id, token string) error {
	req, err := http.NewRequest(http.MethodDelete, serverURL+"/v1/snapshots/"+id, nil)
	if err != nil {
		return fmt.Errorf("create delete request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("delete snapshot: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusNoContent, http.StatusNotFound:
		return nil
	default:
		raw, _ := io.ReadAll(resp.Body)
		return parseError(raw)
	}
}

// parseError reads the server error body and returns it as a structured error.
func parseError(raw []byte) error {
	var errResp ErrorResponse
	if json.Unmarshal(raw, &errResp) == nil && errResp.Code != "" {
		return &errResp
	}
	return fmt.Errorf("server error (HTTP %d): %s", 0, string(raw))
}

func (e *ErrorResponse) Error() string {
	return fmt.Sprintf("%s: %s (request_id=%s)", e.Code, e.Message, e.RequestID)
}

// buildCreateBody builds the JSON request body for a single-source create.
// contentType should be "diff", "file", "markdown", "mermaid", "html", or "csv".
func buildCreateBody(contentType, title, content, filename, description string) io.Reader {
	source := map[string]string{"text": content}
	if filename != "" {
		source["filename"] = filename
	}
	req := map[string]any{
		"content_type": contentType,
		"content":      source,
	}
	if title != "" {
		req["title"] = title
	}
	if description != "" {
		req["description_markdown"] = description
	}
	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(req)
	return &buf
}

// CreateSnapshotWithDescription sends a single-source snapshot with an optional
// description to the server and returns the response.
// contentType should be "diff", "file", "markdown", "mermaid", "html", or "csv".
func CreateSnapshotWithDescription(serverURL, contentType, title, content, description string) (*CreateResponse, error) {
	return CreateSnapshotWithDescriptionAndFilename(serverURL, contentType, title, content, "", description)
}

// CreateSnapshotWithDescriptionAndFilename sends a named single-source snapshot.
func CreateSnapshotWithDescriptionAndFilename(serverURL, contentType, title, content, filename, description string) (*CreateResponse, error) {
	body := buildCreateBody(contentType, title, content, filename, description)
	return createSnapshotRequest(serverURL, body)
}

// CreateCompareSnapshotWithDescription sends exactly two named sources with an optional description.
func CreateCompareSnapshotWithDescription(serverURL, title, before, beforeFilename, after, afterFilename, description string) (*CreateResponse, error) {
	body := buildCompareBody(title, before, beforeFilename, after, afterFilename, description)
	return createSnapshotRequest(serverURL, body)
}

func buildCompareBody(title, before, beforeFilename, after, afterFilename, description string) io.Reader {
	content := map[string]any{
		"before": map[string]string{"text": before, "filename": beforeFilename},
		"after":  map[string]string{"text": after, "filename": afterFilename},
	}
	req := map[string]any{"content_type": "compare", "content": content}
	if title != "" {
		req["title"] = title
	}
	if description != "" {
		req["description_markdown"] = description
	}
	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(req)
	return &buf
}

func createSnapshotRequest(serverURL string, body io.Reader) (*CreateResponse, error) {
	req, err := http.NewRequest(http.MethodPost, serverURL+"/v1/snapshots", body)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	applyCreateHeaders(req)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("create snapshot: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read create response: %w", err)
	}

	return parseCreateResponse(resp.StatusCode, raw)
}

// CreateBinarySnapshot sends an image as the backend's two-part multipart request.
// It does NOT retry on timeout — the caller handles that.
func CreateBinarySnapshot(serverURL, title string, content []byte, filename, description string) (*CreateResponse, error) {
	if len(content) > maxImageContentSize {
		return nil, fmt.Errorf("image content exceeds 8 MiB limit")
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	metadataHeader := make(textproto.MIMEHeader)
	metadataHeader.Set("Content-Disposition", `form-data; name="metadata"`)
	metadataHeader.Set("Content-Type", "application/json")
	metadata, err := writer.CreatePart(metadataHeader)
	if err != nil {
		return nil, fmt.Errorf("create metadata part: %w", err)
	}
	metadataBody := map[string]string{"content_type": "image"}
	if title != "" {
		metadataBody["title"] = title
	}
	if description != "" {
		metadataBody["description_markdown"] = description
	}
	if filename != "" {
		metadataBody["filename"] = filename
	}
	if err := json.NewEncoder(metadata).Encode(metadataBody); err != nil {
		return nil, fmt.Errorf("encode image metadata: %w", err)
	}

	contentHeader := make(textproto.MIMEHeader)
	dispositionParams := map[string]string{"name": "content"}
	if filename != "" {
		dispositionParams["filename"] = filename
	}
	contentHeader.Set("Content-Disposition", mime.FormatMediaType("form-data", dispositionParams))
	contentHeader.Set("Content-Type", "application/octet-stream")
	contentPart, err := writer.CreatePart(contentHeader)
	if err != nil {
		return nil, fmt.Errorf("create content part: %w", err)
	}
	if _, err := contentPart.Write(content); err != nil {
		return nil, fmt.Errorf("write image content: %w", err)
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("close multipart request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, serverURL+"/v1/snapshots/binary", &body)
	if err != nil {
		return nil, fmt.Errorf("create binary request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	applyCreateHeaders(req)

	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("create binary snapshot: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read create response: %w", err)
	}
	return parseCreateResponse(resp.StatusCode, raw)
}

func applyCreateHeaders(req *http.Request) {
	if apiKey := os.Getenv("SNAPIFACT_API_KEY"); apiKey != "" {
		req.Header.Set("X-Snapifact-API-Key", apiKey)
	}
}

func parseCreateResponse(status int, raw []byte) (*CreateResponse, error) {
	if status != http.StatusCreated {
		return nil, parseError(raw)
	}

	var out CreateResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parse create response: %w", err)
	}
	return &out, nil
}
