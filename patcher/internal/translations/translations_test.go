package translations_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"13s-patcher/internal/hashutil"
	"13s-patcher/internal/translations"
)

// target returns a synthetic overlay identity for tests.
func target() translations.SourceIdentity {
	return translations.SourceIdentity{Role: "overlay", Size: 42, SHA256: strings.Repeat("ab", 32)}
}

// writeDataset publishes a valid dataset (both files) into a temp dir.
func writeDataset(t *testing.T, recs []translations.Record) string {
	t.Helper()
	dir := t.TempDir()
	man := translations.BuildManifest(translations.ExpectedGame, target(), recs)
	if err := translations.Write(dir, man, recs); err != nil {
		t.Fatal(err)
	}
	return dir
}

// bankCount counts distinct bank keys in raw records, matching the loader.
func bankCount(raw []byte) int {
	var recs []translations.Record
	for _, l := range strings.Split(string(raw), "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		var r translations.Record
		if json.Unmarshal([]byte(l), &r) != nil {
			continue
		}
		recs = append(recs, r)
	}
	return len(translations.BankInfos(recs))
}

// writeRawDataset writes crafted changes bytes plus a matching manifest, so
// invalid records can be fed to Load without going through Write's gates.
func writeRawDataset(t *testing.T, raw []byte) string {
	t.Helper()
	dir := t.TempDir()
	man := translations.Manifest{
		Schema:        translations.Schema,
		Format:        translations.FormatName,
		Game:          translations.ExpectedGame,
		RecordCount:   strings.Count(string(raw), "\n"),
		BankCount:     bankCount(raw),
		ChangesSHA256: hashutil.Bytes(raw),
	}
	mraw, err := json.MarshalIndent(man, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, translations.ChangesFile), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, translations.ManifestFile), mraw, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// rewriteManifest loads the manifest, lets fn mutate it and writes it back.
func rewriteManifest(t *testing.T, dir string, fn func(m *translations.Manifest)) {
	t.Helper()
	path := filepath.Join(dir, translations.ManifestFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var man translations.Manifest
	if err := json.Unmarshal(raw, &man); err != nil {
		t.Fatal(err)
	}
	fn(&man)
	out, err := json.MarshalIndent(man, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadValidDataset(t *testing.T) {
	recs := []translations.Record{
		{BankKey: "fms/b.fms", RecordIndex: 2, Text: "second"},
		{BankKey: "fms/a.fms", RecordIndex: 5, Text: "fifth"},
		{BankKey: "fms/a.fms", RecordIndex: 1, Text: "first"},
	}
	dir := writeDataset(t, recs)
	if _, err := os.Stat(filepath.Join(dir, translations.ChangesFile)); err != nil {
		t.Fatalf("changes.jsonl missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "records.jsonl")); err == nil {
		t.Fatal("records.jsonl must not be written; changes.jsonl is the only records file")
	}
	ds, err := translations.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ds.Records) != 3 || ds.Manifest.RecordCount != 3 || ds.Manifest.BankCount != 2 {
		t.Fatalf("unexpected dataset: %+v", ds.Manifest)
	}
	if ds.Manifest.Game != translations.ExpectedGame {
		t.Fatalf("game identity not propagated: %+v", ds.Manifest.Game)
	}
	if ds.Records[0].BankKey != "fms/a.fms" || ds.Records[0].RecordIndex != 1 {
		t.Fatalf("records not sorted by bank key then index: %+v", ds.Records)
	}
	if ds.Manifest.Banks[0].BankKey != "fms/a.fms" || ds.Manifest.Banks[0].Records != 2 {
		t.Fatalf("bank summary wrong: %+v", ds.Manifest.Banks)
	}
}

func TestLoadAcceptsSupportedSchemas(t *testing.T) {
	want := []string{"13s-fidelity-translations.2", "13s-translations.2"}
	if len(translations.SupportedSchemas) != len(want) {
		t.Fatalf("unexpected supported schemas: %v", translations.SupportedSchemas)
	}
	for i := range want {
		if translations.SupportedSchemas[i] != want[i] {
			t.Fatalf("unexpected supported schemas: %v", translations.SupportedSchemas)
		}
	}
	recs := []translations.Record{
		{BankKey: "fms/a.fms", RecordIndex: 1, Text: "plain"},
		{BankKey: "fms/a.fms", RecordIndex: 4, Text: "tokens", TokensChanged: true},
	}
	for _, schema := range translations.SupportedSchemas {
		t.Run(schema, func(t *testing.T) {
			dir := writeDataset(t, append([]translations.Record(nil), recs...))
			rewriteManifest(t, dir, func(m *translations.Manifest) { m.Schema = schema })
			ds, err := translations.Load(dir)
			if err != nil {
				t.Fatalf("schema %q must load: %v", schema, err)
			}
			if ds.Manifest.Schema != schema {
				t.Fatalf("schema not preserved: got %q want %q", ds.Manifest.Schema, schema)
			}
			if len(ds.Records) != 2 || !ds.Records[1].TokensChanged || ds.Records[0].TokensChanged {
				t.Fatalf("tokens_changed did not round-trip: %+v", ds.Records)
			}
		})
	}
}

func TestLoadRejectsUnsupportedSchemas(t *testing.T) {
	cases := []struct {
		name   string
		schema string
	}{
		{"empty", ""},
		{"historical .1", "13s-fidelity-translations.1"},
		{"historical .3", "13s-fidelity-translations.3"},
		{"current .1", "13s-translations.1"},
		{"current .3", "13s-translations.3"},
		{"unrelated", "13s-something-else.2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeDataset(t, []translations.Record{{BankKey: "fms/a.fms", RecordIndex: 0, Text: "x"}})
			rewriteManifest(t, dir, func(m *translations.Manifest) { m.Schema = tc.schema })
			if _, err := translations.Load(dir); err == nil {
				t.Fatalf("schema %q must be refused", tc.schema)
			}
		})
	}
}

func TestWriteAlwaysEmitsHistoricalSchema(t *testing.T) {
	recs := []translations.Record{{BankKey: "fms/a.fms", RecordIndex: 0, Text: "x"}}
	man := translations.BuildManifest(translations.ExpectedGame, target(), recs)
	man.Schema = translations.SchemaCurrent
	dir := t.TempDir()
	if err := translations.Write(dir, man, recs); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, translations.ManifestFile))
	if err != nil {
		t.Fatal(err)
	}
	var written translations.Manifest
	if err := json.Unmarshal(raw, &written); err != nil {
		t.Fatal(err)
	}
	if written.Schema != translations.Schema {
		t.Fatalf("Write emitted schema %q, want historical canonical %q", written.Schema, translations.Schema)
	}
}

// TestLoadSupportsCurrentDatasetShape pins the size of the currently published
// dataset (5448 records across 14 banks) so no hard-coded 12-bank/3464-record
// limit can creep back into the loader.
func TestLoadSupportsCurrentDatasetShape(t *testing.T) {
	perBank := []int{4, 125, 27, 67, 39, 338, 4, 306, 4461, 23, 1, 50, 1, 2}
	total := 0
	var recs []translations.Record
	for b, n := range perBank {
		bank := fmt.Sprintf("fms/bank%02d.fms", b)
		for i := 0; i < n; i++ {
			recs = append(recs, translations.Record{
				BankKey:     bank,
				RecordIndex: i * 7, // sparse indices, as in the real dataset
				Text:        fmt.Sprintf("bank %d record %d", b, i),
			})
		}
		total += n
	}
	if total != 5448 {
		t.Fatalf("fixture has %d records, want 5448", total)
	}
	dir := writeDataset(t, recs)
	ds, err := translations.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ds.Records) != 5448 || ds.Manifest.BankCount != 14 || len(ds.Manifest.Banks) != 14 {
		t.Fatalf("current dataset shape not supported: %d records, %d banks", len(ds.Records), ds.Manifest.BankCount)
	}
}

func TestWriteLoadRoundTripIsDeterministic(t *testing.T) {
	recs := []translations.Record{
		{BankKey: "fms/b.fms", RecordIndex: 2, Text: "second"},
		{BankKey: "fms/a.fms", RecordIndex: 5, Text: "fifth"},
		{BankKey: "fms/a.fms", RecordIndex: 1, Text: "first"},
	}
	man := translations.BuildManifest(translations.ExpectedGame, target(), recs)
	out := t.TempDir()
	if err := translations.Write(out, man, recs); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(filepath.Join(out, translations.ChangesFile))
	if err != nil {
		t.Fatal(err)
	}
	firstMan, err := os.ReadFile(filepath.Join(out, translations.ManifestFile))
	if err != nil {
		t.Fatal(err)
	}
	// rewrite with the same input (shuffled order) and compare bytes
	shuffled := []translations.Record{recs[1], recs[2], recs[0]}
	if err := translations.Write(out, translations.BuildManifest(translations.ExpectedGame, target(), shuffled), shuffled); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(filepath.Join(out, translations.ChangesFile))
	secondMan, _ := os.ReadFile(filepath.Join(out, translations.ManifestFile))
	if string(first) != string(second) || string(firstMan) != string(secondMan) {
		t.Fatal("write is not deterministic")
	}
	if _, err := translations.Load(out); err != nil {
		t.Fatal(err)
	}
}

func TestLoadEnforcesManifestContract(t *testing.T) {
	base := []translations.Record{
		{BankKey: "fms/a.fms", RecordIndex: 0, Text: "one"},
		{BankKey: "fms/b.fms", RecordIndex: 1, Text: "two"},
	}
	cases := []struct {
		name string
		mut  func(m *translations.Manifest)
	}{
		{"record_count", func(m *translations.Manifest) { m.RecordCount++ }},
		{"bank_count", func(m *translations.Manifest) { m.BankCount++ }},
		{"changes_digest", func(m *translations.Manifest) { m.ChangesSHA256 = strings.Repeat("00", 32) }},
		{"schema", func(m *translations.Manifest) { m.Schema = "13s-fidelity-translations.1" }},
		{"format", func(m *translations.Manifest) { m.Format = "cpk-fmsb-9" }},
		{"game_title", func(m *translations.Manifest) { m.Game.Title = "Some Other Game" }},
		{"game_title_id", func(m *translations.Manifest) { m.Game.TitleID = "0000000000000000" }},
		{"bank_key", func(m *translations.Manifest) { m.Banks[0].BankKey = "fms/zzz.fms" }},
		{"bank_records", func(m *translations.Manifest) { m.Banks[0].Records++ }},
		{"bank_digest", func(m *translations.Manifest) { m.Banks[0].SHA256 = strings.Repeat("00", 32) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeDataset(t, append([]translations.Record(nil), base...))
			rewriteManifest(t, dir, tc.mut)
			if _, err := translations.Load(dir); err == nil {
				t.Fatal("a tampered manifest must be refused")
			}
		})
	}
}

func TestLoadRejectsTamperedRecords(t *testing.T) {
	dir := writeDataset(t, []translations.Record{{BankKey: "fms/a.fms", RecordIndex: 0, Text: "text"}})
	if err := os.WriteFile(filepath.Join(dir, translations.ChangesFile),
		[]byte(`{"bank_key":"fms/a.fms","record_index":0,"text":"tampered"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := translations.Load(dir); err == nil {
		t.Fatal("tampered records must be refused")
	}
}

func TestLoadRejectsInvalidRecords(t *testing.T) {
	cases := []struct {
		name string
		raw  []byte
	}{
		{"invalid utf-8", []byte("{\"bank_key\":\"fms/a.fms\",\"record_index\":0,\"text\":\"\xff\"}\n")},
		{"NUL", []byte(`{"bank_key":"fms/a.fms","record_index":0,"text":"a\u0000b"}` + "\n")},
		{"BOM", []byte("{\"bank_key\":\"fms/a.fms\",\"record_index\":0,\"text\":\"\ufeffx\"}\n")},
		{"duplicate target", []byte(
			`{"bank_key":"fms/a.fms","record_index":0,"text":"x"}` + "\n" +
				`{"bank_key":"fms/a.fms","record_index":0,"text":"y"}` + "\n")},
		{"out of order", []byte(
			`{"bank_key":"fms/a.fms","record_index":5,"text":"x"}` + "\n" +
				`{"bank_key":"fms/a.fms","record_index":3,"text":"y"}` + "\n")},
		{"empty bank_key", []byte(`{"bank_key":"","record_index":0,"text":"x"}` + "\n")},
		{"negative index", []byte(`{"bank_key":"fms/a.fms","record_index":-1,"text":"x"}` + "\n")},
		{"member field only", []byte(`{"member":"fms/a.fms","record_index":0,"text":"x"}` + "\n")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeRawDataset(t, tc.raw)
			if _, err := translations.Load(dir); err == nil {
				t.Fatal("invalid records must be refused")
			}
		})
	}
}

func TestLoadFailsClosedWhenDatasetMissing(t *testing.T) {
	if _, err := translations.Load(""); err == nil {
		t.Fatal("empty path must be refused")
	}
	empty := t.TempDir()
	if _, err := translations.Load(empty); err == nil {
		t.Fatal("a directory without a manifest must be refused")
	}
	// manifest present, changes file absent
	only := t.TempDir()
	man := translations.Manifest{
		Schema: translations.Schema, Format: translations.FormatName,
		Game: translations.ExpectedGame, RecordCount: 1,
		ChangesSHA256: strings.Repeat("00", 32),
	}
	raw, err := json.MarshalIndent(man, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(only, translations.ManifestFile), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := translations.Load(only); err == nil {
		t.Fatal("a manifest without its changes file must be refused")
	}
}

func TestWriteRejectsDuplicateTarget(t *testing.T) {
	recs := []translations.Record{
		{BankKey: "fms/a.fms", RecordIndex: 1, Text: "x"},
		{BankKey: "fms/a.fms", RecordIndex: 1, Text: "y"},
	}
	man := translations.BuildManifest(translations.ExpectedGame, target(), recs)
	if err := translations.Write(t.TempDir(), man, recs); err == nil {
		t.Fatal("duplicate target must be refused")
	}
}

func TestBankInfoDigestIsStable(t *testing.T) {
	recs := []translations.Record{
		{BankKey: "fms/a.fms", RecordIndex: 0, Text: "one"},
		{BankKey: "fms/a.fms", RecordIndex: 1, Text: "two"},
	}
	infos := translations.BankInfos(recs)
	if len(infos) != 1 || infos[0].Records != 2 || infos[0].BankKey != "fms/a.fms" {
		t.Fatalf("unexpected bank info: %+v", infos)
	}
	shuffled := []translations.Record{recs[1], recs[0]}
	infos2 := translations.BankInfos(shuffled)
	if infos[0].SHA256 != infos2[0].SHA256 {
		t.Fatal("bank digest must not depend on input order")
	}
}
