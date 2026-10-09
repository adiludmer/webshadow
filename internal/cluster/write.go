package cluster

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Files are the files Write produces, in the order it writes them.
var Files = []string{
	"families.json",
	"links.json",
	"traces.json",
	"episodes.json",
	"sequences.json",
	"evidence.json",
	"manifest.json",
}

// Write stores a result in dir, one file per output, and evidence.yaml too
// when withYAML is set. The manifest is written last, so a directory with a
// manifest is complete. Files hold no timestamps, so writing the same result
// twice gives the same bytes.
func Write(dir string, res *Result, withYAML bool) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	outputs := []any{res.Families, res.Links, res.Traces, res.Episodes, res.Sequences, res.Evidence, res.Manifest}
	for i, name := range Files {
		data, err := encode(outputs[i])
		if err != nil {
			return fmt.Errorf("cluster: encode %s: %w", name, err)
		}
		if name == "manifest.json" && withYAML {
			if err := writeYAML(filepath.Join(dir, "evidence.yaml"), res.Evidence); err != nil {
				return err
			}
		}
		if err := writeFile(filepath.Join(dir, name), data); err != nil {
			return err
		}
	}
	return nil
}

func encode(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// writeYAML converts through JSON so the YAML keys and their order are the
// JSON ones.
func writeYAML(path string, v any) error {
	data, err := encode(v)
	if err != nil {
		return err
	}
	var node yaml.Node
	if err := yaml.Unmarshal(data, &node); err != nil {
		return err
	}
	clearStyle(&node)
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(&node); err != nil {
		return err
	}
	return writeFile(path, b.Bytes())
}

// clearStyle drops the JSON flow and quoting style the decoder kept, so the
// output reads as block YAML; the encoder still quotes a string that would
// otherwise read as another type.
func clearStyle(n *yaml.Node) {
	n.Style = 0
	for _, c := range n.Content {
		clearStyle(c)
	}
}

func writeFile(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
