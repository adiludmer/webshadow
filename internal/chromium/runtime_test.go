package chromium

import (
	"archive/zip"
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

type zipEntry struct {
	name, body string
	mode       fs.FileMode
}

func makeZip(t *testing.T, entries []zipEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		h.SetMode(e.mode)
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(e.body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestExtractZipKeepsModesAndSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes and symlinks")
	}
	dir := filepath.Join(t.TempDir(), "out")
	data := makeZip(t, []zipEntry{
		{"app/", "", fs.ModeDir | 0o755},
		{"app/bin/chrome", "#!/bin/sh\n", 0o755},
		{"app/lib/Versions/1/lib.so", "lib", 0o644},
		{"app/lib/Current", "Versions/1", fs.ModeSymlink | 0o777},
	})
	if err := extractZip(dir, data); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "app/bin/chrome"))
	if err != nil || info.Mode().Perm()&0o111 == 0 {
		t.Errorf("executable lost its mode: %v %v", info, err)
	}
	if target, err := os.Readlink(filepath.Join(dir, "app/lib/Current")); err != nil || target != "Versions/1" {
		t.Errorf("symlink = %q, %v", target, err)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "app/lib/Current/lib.so")); err != nil || string(data) != "lib" {
		t.Errorf("reading through symlink: %q, %v", data, err)
	}
}

func TestExtractZipRejectsEscapes(t *testing.T) {
	for name, entries := range map[string][]zipEntry{
		"dot-dot path":     {{"../evil", "x", 0o644}},
		"absolute symlink": {{"link", "/etc", fs.ModeSymlink | 0o777}},
		"escaping symlink": {{"a/link", "../../outside", fs.ModeSymlink | 0o777}},
	} {
		t.Run(name, func(t *testing.T) {
			parent := t.TempDir()
			if err := extractZip(filepath.Join(parent, "out"), makeZip(t, entries)); err == nil {
				t.Error("extracted an entry that escapes the target")
			}
			if _, err := os.Stat(filepath.Join(parent, "evil")); err == nil {
				t.Error("file written outside the target")
			}
		})
	}
}

func TestUnpackOnce(t *testing.T) {
	plat, ok := CurrentPlatform()
	if !ok {
		t.Skip("no bundled Chromium for this platform")
	}
	data := makeZip(t, []zipEntry{{plat.Executable, "browser", 0o755}})
	dir := filepath.Join(t.TempDir(), "Linux_x64", "1712200")
	exe, err := Unpack(dir, data)
	if err != nil {
		t.Fatal(err)
	}
	if exe != filepath.Join(dir, filepath.FromSlash(plat.Executable)) {
		t.Errorf("exe = %s", exe)
	}
	// A second run reuses the unpacked tree, even with an unusable payload.
	if again, err := Unpack(dir, []byte("not a zip")); err != nil || again != exe {
		t.Errorf("second Unpack = %s, %v", again, err)
	}
	if matches, _ := filepath.Glob(dir + ".tmp-*"); len(matches) != 0 {
		t.Errorf("temporary directories left behind: %v", matches)
	}
}

func TestLocatePrefersFlagThenEnv(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "chrome")
	os.WriteFile(exe, nil, 0o755)
	t.Setenv(EnvExecutable, "/does/not/exist")
	rt, err := Locate(t.TempDir(), exe)
	if err != nil || rt.Executable != exe || rt.Source != "flag" {
		t.Errorf("Locate with flag = %+v, %v", rt, err)
	}
	if _, err := Locate(t.TempDir(), ""); err == nil {
		t.Error("Locate accepted a missing WEBSHADOW_CHROMIUM")
	}
	t.Setenv(EnvExecutable, exe)
	if rt, err := Locate(t.TempDir(), ""); err != nil || rt.Source != "env" {
		t.Errorf("Locate with env = %+v, %v", rt, err)
	}
}

func TestReadDevToolsPort(t *testing.T) {
	file := filepath.Join(t.TempDir(), "DevToolsActivePort")
	os.WriteFile(file, []byte("40123\n/devtools/browser/abc-123\n"), 0o600)
	port, path, err := readDevToolsPort(file)
	if err != nil || port != 40123 || path != "/devtools/browser/abc-123" {
		t.Errorf("got %d %q %v", port, path, err)
	}
	os.WriteFile(file, []byte("40123\n"), 0o600)
	if _, _, err := readDevToolsPort(file); err == nil {
		t.Error("accepted a half-written file")
	}
}

func TestArgs(t *testing.T) {
	o := LaunchOptions{ProfileDir: "/p", ProxyAddr: "127.0.0.1:8080", SPKIHash: "abc=", Headless: true, StartURL: "https://example.com/"}
	args := o.Args()
	for _, want := range []string{
		"--user-data-dir=/p",
		"--proxy-server=http://127.0.0.1:8080",
		"--proxy-bypass-list=<-loopback>",
		"--ignore-certificate-errors-spki-list=abc=",
		"--remote-debugging-port=0",
		"--remote-debugging-address=127.0.0.1",
		"--disable-quic",
		"--headless=new",
	} {
		if !slices.Contains(args, want) {
			t.Errorf("args missing %s", want)
		}
	}
	if args[len(args)-1] != "https://example.com/" {
		t.Errorf("start URL not last: %v", args)
	}
	for _, a := range args {
		if strings.HasPrefix(a, "--ignore-certificate-errors") && a != "--ignore-certificate-errors-spki-list=abc=" {
			t.Errorf("certificate checks weakened beyond the Webshadow key: %s", a)
		}
	}
}
