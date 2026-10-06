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
	if err := os.Mkdir(filepath.Join(dir, "Default"), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "Default", "Preferences"), []byte(quietPreferences), 0o600); err != nil {
		return "", err
	}
	return dir, nil
}

// quietPreferences turn off profile features that call Google on their
// own (password leak checks, sign-in, spellcheck downloads), so the
// recording holds what the user did.
const quietPreferences = `{
  "credentials_enable_service": false,
  "profile": {"password_manager_enabled": false, "password_manager_leak_detection": false},
  "signin": {"allowed": false},
  "spellcheck": {"dictionaries": [], "use_spelling_service": false},
  "search": {"suggest_enabled": false},
  "translate": {"enabled": false}
}
`

// RemoveProfile deletes a session's profile, with its cookies and cache.
func RemoveProfile(dir string) error { return os.RemoveAll(dir) }
