package cli

import (
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestJSONCreateBuildersPreserveOptionalCommentsEnabled(t *testing.T) {
	for name, comments := range map[string]*bool{
		"absent": nil,
		"true":   ptr(true),
		"false":  ptr(false),
	} {
		t.Run(name, func(t *testing.T) {
			for builder, body := range map[string]io.Reader{
				"single":  buildCreateBodyWithPasswordAndComments("slides", "", "body", "slides.md", "", "", comments),
				"compare": buildCompareBodyWithPasswordAndComments("", "before", "before.txt", "after", "after.txt", "", "", comments),
			} {
				raw, err := io.ReadAll(body)
				if err != nil {
					t.Fatal(err)
				}
				var request map[string]any
				if err := json.Unmarshal(raw, &request); err != nil {
					t.Fatal(err)
				}
				value, present := request["comments_enabled"]
				if comments == nil {
					if present {
						t.Fatalf("%s comments_enabled = %#v, want absent", builder, value)
					}
				} else if !present || value != *comments {
					t.Fatalf("%s comments_enabled = %#v (present=%t), want %t", builder, value, present, *comments)
				}
			}
		})
	}
}

func TestBinaryCreateBuilderPreservesOptionalCommentsEnabled(t *testing.T) {
	for name, comments := range map[string]*bool{
		"absent": nil,
		"true":   ptr(true),
		"false":  ptr(false),
	} {
		t.Run(name, func(t *testing.T) {
			var got map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
				if err != nil {
					t.Fatal(err)
				}
				part, err := multipart.NewReader(r.Body, params["boundary"]).NextPart()
				if err != nil {
					t.Fatal(err)
				}
				if err := json.NewDecoder(part).Decode(&got); err != nil {
					t.Fatal(err)
				}
				writeCreateResponse(w)
			}))
			defer server.Close()

			if _, err := CreateBinarySnapshotWithPasswordAndComments(server.URL, "", []byte("image"), "", "", "", comments); err != nil {
				t.Fatal(err)
			}
			value, present := got["comments_enabled"]
			if comments == nil {
				if present {
					t.Fatalf("comments_enabled = %#v, want absent", value)
				}
			} else if !present || value != *comments {
				t.Fatalf("comments_enabled = %#v (present=%t), want %t", value, present, *comments)
			}
		})
	}
}

func TestOwnerCommentRequestsUseDeleteTokenOnly(t *testing.T) {
	const (
		id    = "kpm2q6xxyegw5czekhga"
		token = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	)
	for _, test := range []struct {
		name   string
		call   func(string, string, string) error
		method string
		path   string
	}{
		{name: "close", call: CloseComments, method: http.MethodPost, path: "/v1/snapshots/" + id + "/comments/close"},
		{name: "delete", call: func(serverURL, snapshotID, token string) error {
			return DeleteComment(serverURL, snapshotID, "42", token)
		}, method: http.MethodDelete, path: "/v1/snapshots/" + id + "/comments/42"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var got *http.Request
			var body []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = r
				if r.Method != test.method || r.URL.Path != test.path {
					t.Fatalf("request = %s %s, want %s %s", r.Method, r.URL.Path, test.method, test.path)
				}
				body, _ = io.ReadAll(r.Body)
				w.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()
			t.Setenv("SNAPIFACT_API_KEY", "must-not-be-sent")

			if err := test.call(server.URL, id, token); err != nil {
				t.Fatal(err)
			}
			if got.URL.Path != test.path || got.Header.Get("Authorization") != "Bearer "+token || got.Header.Get("X-Snapifact-API-Key") != "" {
				t.Fatalf("request = %s %s headers=%v", got.Method, got.URL.Path, got.Header)
			}
			if len(body) != 0 {
				t.Fatalf("body = %q, want empty", body)
			}
		})
	}
}
