// Package shadow handles shadow trees: the directories of Markdown files a
// generator writes from a HAR and a reader answers from.
package shadow

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"
)

// Stats summarizes a tree.
type Stats struct {
	Files int   `json:"files"`
	Bytes int64 `json:"bytes"`
}

// Digest returns a SHA-256 over every regular file's path and contents in
// lexical order, so two trees with the same files get the same digest
// wherever they live. Empty directories do not count.
func Digest(fsys fs.FS) (string, Stats, error) {
	h := sha256.New()
	var st Stats
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		f, err := fsys.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		fh := sha256.New()
		n, err := io.Copy(fh, f)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s\x00%d\x00%x\n", p, n, fh.Sum(nil))
		st.Files++
		st.Bytes += n
		return nil
	})
	if err != nil {
		return "", Stats{}, err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), st, nil
}

// CleanPath turns a model-supplied path into an fs.FS path inside the
// tree: "" and "/" mean the root, a leading "./" or "/" is dropped, and
// any path that would leave the tree is an error.
func CleanPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	p = strings.TrimLeft(p, "/")
	if p == "" {
		return ".", nil
	}
	clean := path.Clean(p)
	if clean == ".." || strings.HasPrefix(clean, "../") || !fs.ValidPath(clean) {
		return "", errors.New("path must stay inside the shadow tree")
	}
	return clean, nil
}
