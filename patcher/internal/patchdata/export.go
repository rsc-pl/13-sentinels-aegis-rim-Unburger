package patchdata

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"13s-patcher/internal/atomic"
	"13s-patcher/internal/hashutil"
)

// This file implements the maintainer-side bundle exporter: it converts a
// neutral, already-reviewed authoring document into a distributable bundle. The
// authoring document is produced outside this repository by whoever owns the
// translation workflow; nothing private is stored here, and the exporter never
// reads anything but the input document it is given.
//
// See docs/EXPORTING.md for the process.

// ExportOperation is one reviewed change in the authoring document.
type ExportOperation struct {
	ID               string `json:"id"`
	Member           string `json:"member"`
	RecordIndex      int    `json:"record_index"`
	Original         string `json:"original"`
	Replacement      string `json:"replacement"`
	PreserveTokens   bool   `json:"preserve_tokens"`
	AllowTokenChange bool   `json:"allow_token_change,omitempty"`
	// MemberLogicalSHA256 optionally pins the whole member's logical content at
	// authoring time, so a consumer refuses an unexpected source revision.
	MemberLogicalSHA256 string `json:"member_logical_sha256,omitempty"`
}

// ExportInput is the neutral authoring document consumed by the exporter.
type ExportInput struct {
	BundleID       string            `json:"bundle_id"`
	BundleVersion  string            `json:"bundle_version"`
	ReleaseVersion string            `json:"release_version"`
	CreatedUTC     string            `json:"created_utc,omitempty"`
	Notes          string            `json:"notes,omitempty"`
	Game           GameInfo          `json:"game"`
	FormatVersion  string            `json:"format_version"`
	Sources        []SourceSpec      `json:"required_sources"`
	Operations     []ExportOperation `json:"operations"`
	Output         OutputSpec        `json:"output_constraints"`
}

// ExportBundle writes a complete, verifiable bundle into dir. It is
// deterministic: the same input produces byte-identical content files and
// manifest ordering.
func ExportBundle(dir string, in ExportInput) (*Bundle, error) {
	if strings.TrimSpace(in.BundleID) == "" {
		return nil, fmt.Errorf("export: bundle_id is required")
	}
	if strings.TrimSpace(in.BundleVersion) == "" {
		return nil, fmt.Errorf("export: bundle_version is required")
	}
	format := in.FormatVersion
	if format == "" {
		format = SupportedFormatVersion
	}
	m := Manifest{
		SpecVersion:    SpecVersion,
		BundleID:       in.BundleID,
		BundleVersion:  in.BundleVersion,
		ReleaseVersion: in.ReleaseVersion,
		CreatedUTC:     in.CreatedUTC,
		Notes:          in.Notes,
		Game:           in.Game,
		FormatVersion:  format,
		Sources:        append([]SourceSpec(nil), in.Sources...),
		Output:         in.Output,
	}
	if !m.Output.PreserveUnmodified {
		m.Output.PreserveUnmodified = true
	}

	ops := append([]ExportOperation(nil), in.Operations...)
	sort.SliceStable(ops, func(i, j int) bool {
		ki := strings.ToLower(ops[i].Member)
		kj := strings.ToLower(ops[j].Member)
		if ki != kj {
			return ki < kj
		}
		return ops[i].RecordIndex < ops[j].RecordIndex
	})

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	for _, op := range ops {
		if op.ID == "" {
			return nil, fmt.Errorf("export: operation with empty id for %s#%d", op.Member, op.RecordIndex)
		}
		if op.Replacement == "" {
			return nil, fmt.Errorf("export: operation %s has an empty replacement", op.ID)
		}
		rel := filepath.ToSlash(filepath.Join("content", op.ID+".txt"))
		if err := atomic.WriteFile(filepath.Join(dir, filepath.FromSlash(rel)), []byte(op.Replacement), atomic.Options{Overwrite: true}); err != nil {
			return nil, err
		}
		m.ContentFiles = append(m.ContentFiles, ContentFile{
			Path:   rel,
			SHA256: hashutil.Bytes([]byte(op.Replacement)),
			Size:   int64(len(op.Replacement)),
		})
		m.Operations = append(m.Operations, Operation{
			ID:                  op.ID,
			Kind:                KindReplaceTextRecord,
			Member:              op.Member,
			MemberLogicalSHA256: op.MemberLogicalSHA256,
			RecordIndex:         op.RecordIndex,
			OriginalSHA256:      hashutil.Bytes([]byte(op.Original)),
			Original:            op.Original,
			Content:             rel,
			ContentSHA256:       hashutil.Bytes([]byte(op.Replacement)),
			PreserveTokens:      op.PreserveTokens,
			AllowTokenChange:    op.AllowTokenChange,
		})
	}

	manifestPath := filepath.Join(dir, ManifestName)
	if err := atomic.WriteJSON(manifestPath, m, atomic.Options{Overwrite: true}); err != nil {
		return nil, err
	}
	return Load(dir)
}

// DecodeExportInput decodes an authoring document from JSON bytes.
func DecodeExportInput(raw []byte) (ExportInput, error) {
	var in ExportInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return in, err
	}
	return in, nil
}
