// Package chromium finds, unpacks and launches the Chromium that
// `webshadow browser` records. Release builds embed a pinned open-source
// Chromium snapshot for their platform (build tag chromium_bundle) and
// unpack it to a versioned cache on first use; development builds use a
// Chromium named by flag, by WEBSHADOW_CHROMIUM, or found on the system.
package chromium

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// EnvExecutable names a Chromium executable to use instead of the bundled
// one.
const EnvExecutable = "WEBSHADOW_CHROMIUM"

// Platform is a supported Chromium build target.
type Platform struct {
	GOOS, GOARCH string
	// Snapshot is the platform's directory in the Chromium snapshot bucket.
	Snapshot string
	Zip      string
	// Executable is the browser binary inside the unpacked zip.
	Executable string
}

// Platforms are the release targets with a bundled Chromium.
var Platforms = []Platform{
	{"linux", "amd64", "Linux_x64", "chrome-linux.zip", "chrome-linux/chrome"},
	{"darwin", "amd64", "Mac", "chrome-mac.zip", "chrome-mac/Chromium.app/Contents/MacOS/Chromium"},
	{"darwin", "arm64", "Mac_Arm", "chrome-mac.zip", "chrome-mac/Chromium.app/Contents/MacOS/Chromium"},
	{"windows", "amd64", "Win_x64", "chrome-win.zip", "chrome-win/chrome.exe"},
}

// CurrentPlatform returns the platform this binary was built for.
func CurrentPlatform() (Platform, bool) {
	for _, p := range Platforms {
		if p.GOOS == runtime.GOOS && p.GOARCH == runtime.GOARCH {
			return p, true
		}
	}
	return Platform{}, false
}

// Runtime is a Chromium executable ready to launch.
type Runtime struct {
	Executable string
	// Source says where it came from: "flag", "env", "bundled" or
	// "system".
	Source string
	// Revision is the snapshot revision of a bundled Chromium.
	Revision string
}

// Locate finds the Chromium to launch. An explicit path wins, then
// WEBSHADOW_CHROMIUM, then the bundled build (unpacked under
// cacheRoot/<revision> on first use), then a Chromium on the system.
func Locate(cacheRoot, explicit string) (*Runtime, error) {
	if explicit != "" {
		return checked(explicit, "flag")
	}
	if p := os.Getenv(EnvExecutable); p != "" {
		return checked(p, "env")
	}
	if bundled != nil {
		exe, err := Unpack(filepath.Join(cacheRoot, bundled.Revision), bundled.Zip)
		if err != nil {
			return nil, fmt.Errorf("unpacking bundled Chromium: %w", err)
		}
		return &Runtime{Executable: exe, Source: "bundled", Revision: bundled.Revision}, nil
	}
	if exe := findSystem(); exe != "" {
		return &Runtime{Executable: exe, Source: "system"}, nil
	}
	return nil, fmt.Errorf("no Chromium found: this webshadow was built without a bundled Chromium; pass -chromium or set %s", EnvExecutable)
}

func checked(path, source string) (*Runtime, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("chromium %s: %w", path, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("chromium %s is a directory", path)
	}
	return &Runtime{Executable: path, Source: source}, nil
}

// findSystem looks for an installed Chromium or Chrome. It only borrows
// the executable; the profile is always Webshadow's own.
func findSystem() string {
	for _, name := range []string{"chromium", "chromium-browser", "google-chrome", "google-chrome-stable", "chrome"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	var candidates []string
	switch runtime.GOOS {
	case "darwin":
		candidates = []string{
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		}
	case "windows":
		for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)", "LocalAppData"} {
			if dir := os.Getenv(env); dir != "" {
				candidates = append(candidates,
					filepath.Join(dir, "Chromium", "Application", "chrome.exe"),
					filepath.Join(dir, "Google", "Chrome", "Application", "chrome.exe"))
			}
		}
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// completeMarker is written last, so a half-unpacked directory is never
// taken for a usable one.
const completeMarker = ".webshadow-complete"

// Unpack extracts a Chromium zip into dir unless it is already there, and
// returns the executable inside it. It extracts into a temporary sibling
// and renames it into place, so concurrent first runs never see a partial
// tree.
func Unpack(dir string, zipData []byte) (string, error) {
	plat, ok := CurrentPlatform()
	if !ok {
		return "", fmt.Errorf("no bundled Chromium for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	exe := filepath.Join(dir, filepath.FromSlash(plat.Executable))
	if _, err := os.Stat(filepath.Join(dir, completeMarker)); err == nil {
		return exe, nil
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", err
	}
	suffix := make([]byte, 6)
	rand.Read(suffix)
	tmp := dir + ".tmp-" + hex.EncodeToString(suffix)
	defer os.RemoveAll(tmp)
	if err := extractZip(tmp, zipData); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(tmp, completeMarker), nil, 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, dir); err != nil {
		// Another first run may have finished first.
		if _, serr := os.Stat(filepath.Join(dir, completeMarker)); serr != nil {
			return "", err
		}
	}
	if _, err := os.Stat(exe); err != nil {
		return "", fmt.Errorf("bundled Chromium has no %s", plat.Executable)
	}
	return exe, nil
}

// extractZip writes every entry of a zip under dir, keeping executable
// bits and symlinks (macOS app bundles depend on both).
func extractZip(dir string, data []byte) error {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return err
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	for _, f := range zr.File {
		dst, err := within(root, f.Name)
		if err != nil {
			return err
		}
		mode := f.Mode()
		switch {
		case mode.IsDir():
			err = os.MkdirAll(dst, 0o755)
		case mode&fs.ModeSymlink != 0:
			err = extractSymlink(root, dst, f)
		default:
			err = extractFile(dst, f, mode.Perm()|0o600)
		}
		if err != nil {
			return fmt.Errorf("%s: %w", f.Name, err)
		}
	}
	return nil
}

// within resolves a zip entry name under root and rejects names that
// escape it.
func within(root, name string) (string, error) {
	dst := filepath.Join(root, filepath.FromSlash(name))
	if dst != root && !strings.HasPrefix(dst, root+string(filepath.Separator)) {
		return "", fmt.Errorf("zip entry %q escapes the target directory", name)
	}
	return dst, nil
}

func extractSymlink(root, dst string, f *zip.File) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	target, err := io.ReadAll(io.LimitReader(rc, 4096))
	rc.Close()
	if err != nil {
		return err
	}
	resolved := filepath.Join(filepath.Dir(dst), filepath.FromSlash(string(target)))
	if filepath.IsAbs(string(target)) || (resolved != root && !strings.HasPrefix(resolved, root+string(filepath.Separator))) {
		return fmt.Errorf("symlink to %q escapes the target directory", target)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.Symlink(string(target), dst)
}

func extractFile(dst string, f *zip.File, perm fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	_, cerr := io.Copy(out, rc)
	return errors.Join(cerr, out.Close())
}
