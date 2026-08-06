// Package cli provides the HTTP client and token-storage helpers
// for the snapifact CLI.
package cli

import (
	"bytes"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	defaultExpiry = 7 * 24 * time.Hour // canonical legacy files older than this are stale
	tokenFilePerm = os.FileMode(0600)
	tokenDirPerm  = os.FileMode(0700)
	tokenVersion  = 1
)

type tokenFile struct {
	Version     int    `json:"version"`
	DeleteToken string `json:"delete_token"`
	ExpiresAt   string `json:"expires_at"`
}

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

// SaveToken writes a versioned delete token to the token directory.
// The file is named by snapshot ID with mode 0600.
func SaveToken(tokenDir, id, token, expiresAt string) error {
	if !ValidSnapshotID(id) {
		return fmt.Errorf("invalid snapshot ID")
	}
	if !validDeleteToken(token) {
		return fmt.Errorf("invalid delete token")
	}
	if _, err := time.Parse(time.RFC3339, expiresAt); err != nil {
		return fmt.Errorf("invalid token expiry: %w", err)
	}
	if err := os.MkdirAll(tokenDir, tokenDirPerm); err != nil {
		return fmt.Errorf("create token directory: %w", err)
	}
	// Ensure directory permissions are 0700 even if it already existed.
	if err := os.Chmod(tokenDir, tokenDirPerm); err != nil {
		return fmt.Errorf("set token directory permissions: %w", err)
	}
	path := filepath.Join(tokenDir, id)
	data, err := json.Marshal(tokenFile{Version: tokenVersion, DeleteToken: token, ExpiresAt: expiresAt})
	if err != nil {
		return fmt.Errorf("encode token file: %w", err)
	}
	if err := os.WriteFile(path, data, tokenFilePerm); err != nil {
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
	if validDeleteToken(string(data)) {
		return string(data), nil
	}
	stored, err := decodeTokenFile(data)
	if err != nil {
		return "", fmt.Errorf("parse token file: %w", err)
	}
	return stored.DeleteToken, nil
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

// CleanStaleTokens removes expired versioned tokens and old canonical legacy
// tokens. Non-fatal errors are ignored.
func CleanStaleTokens(tokenDir string) {
	cleanStaleTokensAt(tokenDir, time.Now())
}

func cleanStaleTokensAt(tokenDir string, now time.Time) {
	entries, err := os.ReadDir(tokenDir)
	if err != nil {
		return // directory may not exist yet
	}
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !ValidSnapshotID(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(tokenDir, entry.Name()))
		if err != nil {
			continue
		}
		if validDeleteToken(string(data)) {
			if info.ModTime().Before(now.Add(-defaultExpiry)) {
				_ = os.Remove(filepath.Join(tokenDir, entry.Name()))
			}
			continue
		}
		stored, err := decodeTokenFile(data)
		if err != nil {
			continue
		}
		expiresAt, err := time.Parse(time.RFC3339, stored.ExpiresAt)
		if err == nil && !expiresAt.After(now) {
			_ = os.Remove(filepath.Join(tokenDir, entry.Name()))
		}
	}
}

func validDeleteToken(token string) bool {
	if len(token) != 43 {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	return err == nil && len(decoded) == 32 && base64.RawURLEncoding.EncodeToString(decoded) == token
}

func decodeTokenFile(data []byte) (tokenFile, error) {
	var stored tokenFile
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&stored); err != nil {
		return tokenFile{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return tokenFile{}, fmt.Errorf("trailing JSON")
		}
		return tokenFile{}, err
	}
	if stored.Version != tokenVersion || !validDeleteToken(stored.DeleteToken) {
		return tokenFile{}, fmt.Errorf("invalid versioned token")
	}
	if _, err := time.Parse(time.RFC3339, stored.ExpiresAt); err != nil {
		return tokenFile{}, fmt.Errorf("invalid token expiry: %w", err)
	}
	return stored, nil
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
