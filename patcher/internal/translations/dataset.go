// Package translations defines the public translation dataset: a deterministic,
// validated list of reviewed text changes plus a manifest that pins the resource
// revision they apply to. The dataset carries only what a build needs — the
// target bank path, the record index and the replacement text — never the
// original game script, the reference corpus or any authoring history.
//
// This package is a consumer only. The dataset is prepared and published by the
// project's translation maintainer outside this module; nothing here reads a
// working corpus, scans a private directory or regenerates the dataset. A
// missing or invalid dataset fails closed.
package translations

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"13s-patcher/internal/atomic"
	"13s-patcher/internal/hashutil"
)

// Schema is the historical canonical dataset layout identifier. It remains the
// identifier this package writes, so fixtures and published datasets authored
// by this module stay byte-identical.
const Schema = "13s-fidelity-translations.2"

// SchemaCurrent is the alternate public dataset layout identifier used by the
// currently published translations/ dataset. It differs from Schema in name
// only: the manifest and record structures are identical, so the same strict
// loader validates both.
const SchemaCurrent = "13s-translations.2"

// SupportedSchemas lists every dataset layout identifier Load accepts. Names
// outside this set (including other versions of either family) fail closed.
var SupportedSchemas = []string{Schema, SchemaCurrent}

func schemaSupported(schema string) bool {
	for _, s := range SupportedSchemas {
		if schema == s {
			return true
		}
	}
	return false
}

// FormatName is the resource format family the changes target.
const FormatName = "cpk-fmsb-1"

// ChangesFile and ManifestFile are the dataset file names.
const (
	ChangesFile  = "changes.jsonl"
	ManifestFile = "manifest.json"
)

// Canonical game identity the dataset targets. These are the single source the
// manifest validation and the gameprofile package share, so the identity can
// never drift between the dataset and the build.
const (
	GameTitle         = "13 Sentinels: Aegis Rim"
	GameTitleID       = "01003FC01670C000"
	GameRegion        = "US"
	GameBaseVersion   = "1.00"
	GameUpdateVersion = "1.10"
)

// ExpectedGame is the game identity a dataset must declare.
var ExpectedGame = GameInfo{
	Title:         GameTitle,
	TitleID:       GameTitleID,
	Region:        GameRegion,
	BaseVersion:   GameBaseVersion,
	UpdateVersion: GameUpdateVersion,
}

// Record is one reviewed text change.
type Record struct {
	// BankKey is the resource-archive bank path (case-insensitive), e.g.
	// "fms/scripttext.fms".
	BankKey string `json:"bank_key"`
	// RecordIndex is the zero-based string index inside the bank's text record
	// list.
	RecordIndex int `json:"record_index"`
	// Text is the replacement string (UTF-8, no BOM, no NUL).
	Text string `json:"text"`
	// TokensChanged marks a change that intentionally alters the renderer control
	// tokens of the record. It is present only for changes whose token difference
	// was explicitly reviewed and declared, so a consumer may permit it while still
	// refusing any undeclared token change.
	TokensChanged bool `json:"tokens_changed,omitempty"`
}

// GameInfo names the supported title and revision.
type GameInfo struct {
	Title         string `json:"title"`
	TitleID       string `json:"title_id"`
	Region        string `json:"region"`
	BaseVersion   string `json:"base_version"`
	UpdateVersion string `json:"update_version"`
}

// SourceIdentity pins the archive revision the changes were reviewed against.
type SourceIdentity struct {
	Role   string `json:"role"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// BankInfo summarises the changes for one bank.
type BankInfo struct {
	BankKey string `json:"bank_key"`
	Records int    `json:"records"`
	// SHA256 is the digest of the bank's replacement texts concatenated in
	// record-index order, each followed by a newline byte.
	SHA256 string `json:"sha256"`
}

// Manifest is the dataset manifest document.
type Manifest struct {
	Schema        string         `json:"schema"`
	Format        string         `json:"format"`
	Game          GameInfo       `json:"game"`
	RecordCount   int            `json:"record_count"`
	BankCount     int            `json:"bank_count"`
	ChangesSHA256 string         `json:"changes_sha256"`
	Banks         []BankInfo     `json:"banks"`
	TargetSource  SourceIdentity `json:"target_source"`
}

// Dataset is a loaded, verified dataset.
type Dataset struct {
	Dir      string
	Manifest Manifest
	Records  []Record
}

// BankKey returns the case-insensitive lookup key for a bank path.
func BankKey(path string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimPrefix(path, "/"), "\\", "/"))
}

// SortRecords orders records by bank key (case-insensitive) then record index.
func SortRecords(recs []Record) {
	sort.SliceStable(recs, func(i, j int) bool {
		ki, kj := BankKey(recs[i].BankKey), BankKey(recs[j].BankKey)
		if ki != kj {
			return ki < kj
		}
		return recs[i].RecordIndex < recs[j].RecordIndex
	})
}

func marshalRecord(b *bytes.Buffer, r Record) {
	raw, _ := json.Marshal(r)
	b.Write(raw)
	b.WriteByte('\n')
}

// EncodeRecords renders the records as the exact bytes of changes.jsonl.
func EncodeRecords(recs []Record) []byte {
	var b bytes.Buffer
	for _, r := range recs {
		marshalRecord(&b, r)
	}
	return b.Bytes()
}

// BankInfos summarises the records per bank, in bank-key order.
func BankInfos(recs []Record) []BankInfo {
	agg := map[string][]Record{}
	var order []string
	for _, r := range recs {
		k := BankKey(r.BankKey)
		if _, ok := agg[k]; !ok {
			order = append(order, k)
		}
		agg[k] = append(agg[k], r)
	}
	sort.Strings(order)
	out := make([]BankInfo, 0, len(order))
	for _, k := range order {
		group := agg[k]
		sort.SliceStable(group, func(i, j int) bool { return group[i].RecordIndex < group[j].RecordIndex })
		var b bytes.Buffer
		for _, r := range group {
			b.WriteString(r.Text)
			b.WriteByte('\n')
		}
		out = append(out, BankInfo{BankKey: group[0].BankKey, Records: len(group), SHA256: hashutil.Bytes(b.Bytes())})
	}
	return out
}

// BuildManifest assembles a manifest for the given records and target identity.
// The records are sorted internally, so the digest does not depend on the order
// they are supplied in.
func BuildManifest(game GameInfo, target SourceIdentity, recs []Record) Manifest {
	sorted := append([]Record(nil), recs...)
	SortRecords(sorted)
	recordsBytes := EncodeRecords(sorted)
	banks := BankInfos(sorted)
	return Manifest{
		Schema:        Schema,
		Format:        FormatName,
		Game:          game,
		RecordCount:   len(sorted),
		BankCount:     len(banks),
		ChangesSHA256: hashutil.Bytes(recordsBytes),
		Banks:         banks,
		TargetSource:  target,
	}
}

// Write writes the dataset deterministically into dir. The records are sorted
// before they are rendered, so the same input always produces byte-identical
// files. It is used by tests and fixtures; the shipped dataset is published by
// the maintainer outside this module.
//
// The written manifest always declares the historical canonical Schema,
// regardless of the schema the supplied manifest carried: Load accepts both
// SupportedSchemas, while Write has one deterministic output.
func Write(dir string, man Manifest, recs []Record) error {
	if strings.TrimSpace(dir) == "" {
		return fmt.Errorf("translations: empty destination directory")
	}
	SortRecords(recs)
	if err := validateRecords(recs, nil); err != nil {
		return err
	}
	if man.Banks == nil {
		man.Banks = BankInfos(recs)
	}
	man.RecordCount = len(recs)
	man.BankCount = len(man.Banks)
	man.Schema = Schema
	if man.Format == "" {
		man.Format = FormatName
	}
	recordsBytes := EncodeRecords(recs)
	if man.ChangesSHA256 == "" {
		man.ChangesSHA256 = hashutil.Bytes(recordsBytes)
	}
	if man.ChangesSHA256 != hashutil.Bytes(recordsBytes) {
		return fmt.Errorf("translations: manifest changes digest does not match the records")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := atomic.WriteFile(filepath.Join(dir, ChangesFile), recordsBytes, atomic.Options{Overwrite: true}); err != nil {
		return err
	}
	return atomic.WriteJSON(filepath.Join(dir, ManifestFile), man, atomic.Options{Overwrite: true})
}

// Load reads and verifies a dataset directory.
func Load(dir string) (*Dataset, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("translations: empty source directory")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	manRaw, err := os.ReadFile(filepath.Join(abs, ManifestFile))
	if err != nil {
		return nil, fmt.Errorf("translations: %w", err)
	}
	var man Manifest
	if err := json.Unmarshal(manRaw, &man); err != nil {
		return nil, fmt.Errorf("translations: %s: %w", ManifestFile, err)
	}
	if !schemaSupported(man.Schema) {
		return nil, fmt.Errorf("translations: unsupported schema %q (want one of %s)", man.Schema, strings.Join(SupportedSchemas, ", "))
	}
	if man.Format != FormatName {
		return nil, fmt.Errorf("translations: unsupported format %q (want %q)", man.Format, FormatName)
	}
	if man.Game != ExpectedGame {
		return nil, fmt.Errorf("translations: manifest game identity does not match the supported title")
	}
	recRaw, err := os.ReadFile(filepath.Join(abs, ChangesFile))
	if err != nil {
		return nil, fmt.Errorf("translations: %w", err)
	}
	if man.ChangesSHA256 != "" && hashutil.Bytes(recRaw) != man.ChangesSHA256 {
		return nil, fmt.Errorf("translations: %s does not match the manifest digest", ChangesFile)
	}
	recs, err := decodeRecords(recRaw)
	if err != nil {
		return nil, err
	}
	if man.RecordCount != len(recs) {
		return nil, fmt.Errorf("translations: manifest declares %d record(s) but %s holds %d", man.RecordCount, ChangesFile, len(recs))
	}
	if got := len(BankInfos(recs)); got != man.BankCount {
		return nil, fmt.Errorf("translations: manifest declares %d bank(s) but %s spans %d", man.BankCount, ChangesFile, got)
	}
	if err := validateRecords(recs, man.Banks); err != nil {
		return nil, err
	}
	return &Dataset{Dir: abs, Manifest: man, Records: recs}, nil
}

func decodeRecords(raw []byte) ([]Record, error) {
	if !utf8.Valid(raw) {
		return nil, fmt.Errorf("translations: %s is not valid UTF-8", ChangesFile)
	}
	if bytes.IndexByte(raw, 0) >= 0 {
		return nil, fmt.Errorf("translations: %s contains a NUL byte", ChangesFile)
	}
	var out []Record
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 0, 1<<20), 32<<20)
	line := 0
	for sc.Scan() {
		line++
		text := strings.TrimSpace(sc.Text())
		if text == "" {
			continue
		}
		var r Record
		if err := json.Unmarshal([]byte(text), &r); err != nil {
			return nil, fmt.Errorf("translations: %s:%d: %w", ChangesFile, line, err)
		}
		out = append(out, r)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("translations: %s holds no records", ChangesFile)
	}
	return out, nil
}

// validateRecords enforces the structural invariants every consumer relies on.
func validateRecords(recs []Record, banks []BankInfo) error {
	seen := map[string]bool{}
	prevKey := ""
	prevIdx := 0
	for i, r := range recs {
		label := fmt.Sprintf("record %d", i)
		if strings.TrimSpace(r.BankKey) == "" {
			return fmt.Errorf("translations: %s: empty bank_key (expected the bank_key field, not member)", label)
		}
		if r.RecordIndex < 0 {
			return fmt.Errorf("translations: %s: negative record_index", label)
		}
		if !utf8.ValidString(r.Text) {
			return fmt.Errorf("translations: %s %s#%d: text is not valid UTF-8", label, r.BankKey, r.RecordIndex)
		}
		if strings.ContainsRune(r.Text, 0) {
			return fmt.Errorf("translations: %s %s#%d: text contains NUL", label, r.BankKey, r.RecordIndex)
		}
		if strings.HasPrefix(r.Text, "\ufeff") {
			return fmt.Errorf("translations: %s %s#%d: text starts with a BOM", label, r.BankKey, r.RecordIndex)
		}
		key := BankKey(r.BankKey)
		id := fmt.Sprintf("%s#%d", key, r.RecordIndex)
		if seen[id] {
			return fmt.Errorf("translations: duplicate change for %s", id)
		}
		seen[id] = true
		if prevKey == key && r.RecordIndex <= prevIdx {
			return fmt.Errorf("translations: %s is not in bank/index order", id)
		}
		prevKey, prevIdx = key, r.RecordIndex
	}
	if banks != nil {
		want := BankInfos(recs)
		if len(want) != len(banks) {
			return fmt.Errorf("translations: manifest lists %d bank(s) but records span %d", len(banks), len(want))
		}
		for i := range want {
			if want[i] != banks[i] {
				return fmt.Errorf("translations: bank summary mismatch for %s", want[i].BankKey)
			}
		}
	}
	return nil
}
