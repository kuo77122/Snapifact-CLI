package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
			if err := SaveToken(dir, id, "delete-token"); err != nil {
				t.Fatal(err)
			}
			if got, err := ReadToken(dir, id); err != nil || got != "delete-token" {
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
