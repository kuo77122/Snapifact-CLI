package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestValidSnapshotID(t *testing.T) {
	legacy := strings.Repeat("a", 32)
	newID := "kpm2q6xxyegw5czekhga"
	tests := []struct {
		name string
		id   string
		want bool
	}{
		{name: "canonical new", id: newID, want: true},
		{name: "canonical legacy", id: legacy, want: true},
		{name: "wrong length", id: newID[:19], want: false},
		{name: "uppercase new", id: strings.ToUpper(newID), want: false},
		{name: "padded new", id: newID + "=", want: false},
		{name: "punctuation new", id: newID[:19] + "-", want: false},
		{name: "invalid base32 digit", id: newID[:19] + "0", want: false},
		{name: "noncanonical base32 tail bits", id: newID[:19] + "b", want: false},
		{name: "wrong legacy length", id: legacy[:31], want: false},
		{name: "uppercase legacy", id: strings.Repeat("A", 32), want: true},
		{name: "padded legacy", id: legacy[:31] + "=", want: false},
		{name: "invalid punctuation legacy", id: legacy[:31] + "+", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ValidSnapshotID(tt.id); got != tt.want {
				t.Fatalf("ValidSnapshotID(%q) = %t, want %t", tt.id, got, tt.want)
			}
		})
	}
}

func TestTokenStoreSupportsBothSnapshotIDFormats(t *testing.T) {
	for _, id := range []string{"kpm2q6xxyegw5czekhga", strings.Repeat("a", 32)} {
		t.Run(id, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "snapifact", "tokens")
			token := strings.Repeat("A", 43)
			if err := SaveToken(dir, id, token, "2026-08-13T00:00:00Z"); err != nil {
				t.Fatal(err)
			}
			if got, err := ReadToken(dir, id); err != nil || got != token {
				t.Fatalf("ReadToken() = %q, %v", got, err)
			}
			if err := RemoveToken(dir, id); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(dir, id)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("token file remains: %v", err)
			}
		})
	}
}

func TestSaveTokenWritesStrictVersionOneJSON(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tokens")
	id := "kpm2q6xxyegw5czekhga"
	token := strings.Repeat("A", 43)
	expiresAt := "2026-08-13T00:00:00Z"

	if err := SaveToken(dir, id, token, expiresAt); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, id))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatalf("token file is not JSON: %v", err)
	}
	if len(fields) != 3 || fields["version"] != float64(1) || fields["delete_token"] != token || fields["expires_at"] != expiresAt {
		t.Fatalf("token JSON = %#v, want exactly version, delete_token, expires_at", fields)
	}
}

func TestReadTokenAcceptsCanonicalLegacyOnly(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tokens")
	id := strings.Repeat("a", 32)
	token := strings.Repeat("A", 43)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id), []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadToken(dir, id); err != nil || got != token {
		t.Fatalf("ReadToken() = %q, %v", got, err)
	}

	for name, data := range map[string]string{
		"newline legacy":      token + "\n",
		"padded legacy":       token[:42] + "=",
		"noncanonical legacy": token[:42] + "a",
		"malformed JSON":      "not a token",
		"unknown version":     `{"version":2,"delete_token":"` + token + `","expires_at":"2026-08-13T00:00:00Z"}`,
		"unknown field":       `{"version":1,"delete_token":"` + token + `","expires_at":"2026-08-13T00:00:00Z","extra":true}`,
		"trailing JSON":       `{"version":1,"delete_token":"` + token + `","expires_at":"2026-08-13T00:00:00Z"}{}`,
		"bad expiry":          `{"version":1,"delete_token":"` + token + `","expires_at":"tomorrow"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(dir, id), []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadToken(dir, id); err == nil {
				t.Fatalf("ReadToken(%q) unexpectedly succeeded", data)
			}
		})
	}
}

func TestCleanStaleTokensUsesExpiryAndSafeLegacyBoundary(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tokens")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	validToken := strings.Repeat("A", 43)
	write := func(id, content string) string {
		t.Helper()
		path := filepath.Join(dir, id)
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	setMtime := func(path string, at time.Time) {
		t.Helper()
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
	}
	v1 := func(expiresAt string) string {
		return `{"version":1,"delete_token":"` + validToken + `","expires_at":"` + expiresAt + `"}`
	}

	expired := write("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", v1(now.Format(time.RFC3339)))
	fresh := write("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", v1(now.Add(time.Second).Format(time.RFC3339)))
	legacyOld := write("cccccccccccccccccccccccccccccccc", validToken)
	legacyBoundary := write("dddddddddddddddddddddddddddddddd", validToken)
	malformed := write("eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", "not a token")
	noncanonical := write("ffffffffffffffffffffffffffffffff", validToken[:42]+"a")
	unknownVersion := write("gggggggggggggggggggggggggggggggg", `{"version":2,"delete_token":"`+validToken+`","expires_at":"2026-08-05T00:00:00Z"}`)
	invalidName := write("not-a-snapshot", validToken)
	setMtime(legacyOld, now.Add(-defaultExpiry-time.Second))
	setMtime(legacyBoundary, now.Add(-defaultExpiry))
	setMtime(malformed, now.Add(-defaultExpiry-time.Hour))
	setMtime(noncanonical, now.Add(-defaultExpiry-time.Hour))
	setMtime(unknownVersion, now.Add(-defaultExpiry-time.Hour))
	setMtime(invalidName, now.Add(-defaultExpiry-time.Hour))

	subdir := filepath.Join(dir, "hhhhhhhhhhhhhhhhhhhhhhhhhhhhhhhh")
	if err := os.Mkdir(subdir, 0700); err != nil {
		t.Fatal(err)
	}

	cleanStaleTokensAt(dir, now)
	for name, path := range map[string]string{
		"expired v1":       expired,
		"old legacy":       legacyOld,
		"fresh v1":         fresh,
		"boundary legacy":  legacyBoundary,
		"malformed":        malformed,
		"noncanonical raw": noncanonical,
		"unknown version":  unknownVersion,
		"invalid filename": invalidName,
	} {
		_, err := os.Stat(path)
		removed := errors.Is(err, os.ErrNotExist)
		wantRemoved := name == "expired v1" || name == "old legacy"
		if removed != wantRemoved {
			t.Fatalf("%s removed = %t, want %t (err=%v)", name, removed, wantRemoved, err)
		}
	}
	if _, err := os.Stat(subdir); err != nil {
		t.Fatalf("snapshot-named subdirectory was removed: %v", err)
	}
}
