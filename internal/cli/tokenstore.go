// Package cli provides the HTTP client and token-storage helpers
// for the snapifact CLI.
package cli

import (
	"encoding/base32"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

const snapshotBase32Alphabet = "abcdefghijklmnopqrstuvwxyz234567"

var snapshotBase32Encoding = base32.NewEncoding(snapshotBase32Alphabet).WithPadding(base32.NoPadding)

const (
	tokenDirName  = "snapifact/tokens"
	defaultExpiry = 7 * 24 * time.Hour // token files older than this are stale
	tokenFilePerm = os.FileMode(0600)
	tokenDirPerm  = os.FileMode(0700)
)

// TokenDir returns the platform-specific token directory.
func TokenDir(stateDir string) string {
	if stateDir != "" {
		return filepath.Join(stateDir, tokenDirName)
	}
	switch runtime.GOOS {
	case "darwin":
		home, _ := os.UserHomeDir()
		return filepath.Join(home, "Library", "Application Support", tokenDirName)
	default: // linux and others
		stateHome := os.Getenv("XDG_STATE_HOME")
		if stateHome == "" {
			home, _ := os.UserHomeDir()
			stateHome = filepath.Join(home, ".local", "state")
		}
		return filepath.Join(stateHome, tokenDirName)
	}
}

// SaveToken writes a delete token to the token directory.
// The file is named by snapshot ID with mode 0600.
func SaveToken(tokenDir, id, token string) error {
	if !ValidSnapshotID(id) {
		return fmt.Errorf("invalid snapshot ID")
	}
	if err := os.MkdirAll(tokenDir, tokenDirPerm); err != nil {
		return fmt.Errorf("create token directory: %w", err)
	}
	// Ensure directory permissions are 0700 even if it already existed.
	if err := os.Chmod(tokenDir, tokenDirPerm); err != nil {
		return fmt.Errorf("set token directory permissions: %w", err)
	}
	path := filepath.Join(tokenDir, id)
	if err := os.WriteFile(path, []byte(token), tokenFilePerm); err != nil {
		return fmt.Errorf("write token file: %w", err)
	}
	// Ensure file permissions are 0600 (WriteFile may not chmod existing files).
	if err := os.Chmod(path, tokenFilePerm); err != nil {
		return fmt.Errorf("set token file permissions: %w", err)
	}
	return nil
}

// ReadToken reads a delete token from the token directory.
func ReadToken(tokenDir, id string) (string, error) {
	if !ValidSnapshotID(id) {
		return "", fmt.Errorf("invalid snapshot ID")
	}
	data, err := os.ReadFile(filepath.Join(tokenDir, id))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("delete token not found for %s", id)
		}
		return "", fmt.Errorf("read token file: %w", err)
	}
	return string(data), nil
}

// RemoveToken removes a token file by snapshot ID. It is not an error
// if the file does not exist.
func RemoveToken(tokenDir, id string) error {
	if !ValidSnapshotID(id) {
		return fmt.Errorf("invalid snapshot ID")
	}
	err := os.Remove(filepath.Join(tokenDir, id))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove token file: %w", err)
	}
	return nil
}

// CleanStaleTokens removes token files whose modification time is older
// than the default expiry (7 days). Non-fatal errors are ignored.
func CleanStaleTokens(tokenDir string) {
	entries, err := os.ReadDir(tokenDir)
	if err != nil {
		return // directory may not exist yet
	}
	cutoff := time.Now().Add(-defaultExpiry)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(tokenDir, entry.Name()))
		}
	}
}

// ValidSnapshotID checks whether id is a canonical new or legacy snapshot ID.
// Re-exported here so both cmd/snapifact and internal/cli can use it without
// importing the app package.
func ValidSnapshotID(id string) bool {
	switch len(id) {
	case 20:
		decoded, err := snapshotBase32Encoding.DecodeString(id)
		return err == nil && len(decoded) == 12 && snapshotBase32Encoding.EncodeToString(decoded) == id
	case 32:
		decoded, err := base64.RawURLEncoding.DecodeString(id)
		return err == nil && len(decoded) == 24 && base64.RawURLEncoding.EncodeToString(decoded) == id
	default:
		return false
	}
}
