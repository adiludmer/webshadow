package record

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWrite(t *testing.T) {
	dir := t.TempDir()
	r := &Record{SchemaVersion: SchemaVersion, RunID: NewRunID(time.Date(2026, 10, 4, 16, 30, 5, 0, time.FixedZone("IDT", 3*3600))),
		ScenarioID: "enso", Repetition: 3, GeneratorModelID: ExternalGenerator, ReaderModelID: "local-llama", Status: "success"}
	path, err := Write(dir, r)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "20261004T133005Z", "cases", "enso", "external", "local-llama", "repetition-03.json")
	if path != want {
		t.Fatalf("path = %s, want %s", path, want)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var back Record
	if err := json.Unmarshal(data, &back); err != nil || back.Status != "success" || back.Repetition != 3 {
		t.Fatalf("read back %+v, %v", back, err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Error("temporary file left behind")
	}
}
