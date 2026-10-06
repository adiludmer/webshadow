package chromium

import (
	"fmt"
	"os"
	"path/filepath"
)

// NewProfile creates an empty, owner-only browser profile for one session
// under root. A profile is never shared between sessions and never the
// user's own Chrome profile.
func NewProfile(root, sessionID string) (string, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	dir := filepath.Join(root, sessionID)
	if err := os.Mkdir(dir, 0o700); err != nil {
		return "", fmt.Errorf("creating browser profile: %w", err)
	}
	return dir, nil
}

// RemoveProfile deletes a session's profile, with its cookies and cache.
func RemoveProfile(dir string) error { return os.RemoveAll(dir) }
