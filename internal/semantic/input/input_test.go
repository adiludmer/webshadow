package input

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/adiludmer/webshadow/internal/cluster/model"
	"github.com/adiludmer/webshadow/internal/semantic/ir"
	"github.com/adiludmer/webshadow/internal/semantic/privacy"
)

const fixture = "../testdata/amazon-search"

var (
	loadOnce sync.Once
	loaded   *Input
	loadErr  error
)

// loadFixture loads the Amazon fixture once for every test that only reads
// it; decoding its 53,042 sequence edges takes a few seconds.
func loadFixture(t *testing.T) *Input {
	t.Helper()
	loadOnce.Do(func() { loaded, loadErr = Load(fixture) })
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	return loaded
}

func TestLoadAmazonFixture(t *testing.T) {
	in := loadFixture(t)
	c := in.Manifest.Counts
	if c.Families != 294 || c.Static != 151 || c.Links != 1151 || c.Episodes != 110 || c.Edges != 53042 {
		t.Errorf("manifest counts = %+v", c)
	}
	if len(in.Families) != c.Families || len(in.Links) != c.Links || len(in.Episodes) != c.Episodes || len(in.Sequences) != c.Edges {
		t.Errorf("decoded %d families, %d links, %d episodes, %d edges", len(in.Families), len(in.Links), len(in.Episodes), len(in.Sequences))
	}
	if len(in.Evidence.Families)+len(in.Evidence.Static) != c.Families {
		t.Errorf("evidence pack has %d + %d entries", len(in.Evidence.Families), len(in.Evidence.Static))
	}
	if len(in.Files) != len(Files) || !strings.HasSuffix(in.Files[0].Name, ".json.gz") || len(in.Files[0].SHA256) != 64 {
		t.Errorf("files = %+v", in.Files)
	}
	f := in.Families[0]
	for _, ref := range []string{
		ir.Ref(ir.RefFamily, f.ID),
		ir.Ref(ir.RefVariant, f.ID, f.ResponseVariants[0].ID),
		ir.Ref(ir.RefObservation, f.Observations[0].SessionID, f.Observations[0].ExchangeID),
		ir.Ref(ir.RefValueLink, in.Links[0].ID),
		ir.Ref(ir.RefEpisode, in.Episodes[0].ID),
		ir.Ref(ir.RefSequence, in.Sequences[0].ID),
		ir.Ref(ir.RefTrace, in.Traces[0].ID),
	} {
		if !in.Has(ref) {
			t.Errorf("missing %s", ref)
		}
	}
	if in.Has("fam:000000000000") {
		t.Error("an unknown family resolved")
	}
}

// The fixture was redacted before it was committed. Loading it must find
// nothing left to redact, and no value at a credential location may be
// longer than the redactor's threshold without being a tag.
func TestFixtureHoldsNoSecrets(t *testing.T) {
	in := loadFixture(t)
	if in.Redaction.Replaced != 0 {
		t.Errorf("loading the fixture replaced %d strings; it was not fully redacted", in.Redaction.Replaced)
	}
	for _, l := range in.Links {
		for _, o := range l.Locations {
			if o.Location.Part != model.PartCookie && o.Location.Part != model.PartSetCookie {
				continue
			}
			if len(l.Value) >= privacy.MinSecretLen && !privacy.IsTag(l.Value) {
				t.Errorf("link %s carries a raw %s value", l.ID, o.Location)
			}
		}
	}
}

func TestRedactWriteRoundTrip(t *testing.T) {
	raw, err := ReadRaw(fixture)
	if err != nil {
		t.Fatal(err)
	}
	raw.Redact()
	dir := t.TempDir()
	if err := raw.Write(dir, false); err != nil {
		t.Fatal(err)
	}
	in, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(in.Families) != 294 || in.Files[0].Name != "families.json" {
		t.Errorf("round trip lost data: %d families, files %+v", len(in.Families), in.Files)
	}
}

func TestReadRawRejectsOtherVersions(t *testing.T) {
	dir := t.TempDir()
	raw, err := ReadRaw(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if err := raw.Write(dir, false); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"version": 99, "id": "cl_x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRaw(dir); err == nil || !strings.Contains(err.Error(), "version 99") {
		t.Errorf("err = %v", err)
	}
	if _, err := ReadRaw(t.TempDir()); err == nil || !strings.Contains(err.Error(), "cluster output directory") {
		t.Errorf("empty dir err = %v", err)
	}
}
