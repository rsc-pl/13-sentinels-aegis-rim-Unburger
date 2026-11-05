package patchdata

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"13s-patcher/internal/hashutil"
)

// ManifestName is the file a bundle directory must contain.
const ManifestName = "manifest.json"

// Bundle is a loaded, verified patch-data bundle.
type Bundle struct {
	// Dir is the bundle directory (absolute).
	Dir string
	// ManifestPath is the path of the loaded manifest.
	ManifestPath string
	// ManifestSHA256 is the digest of the manifest bytes read from disk.
	ManifestSHA256 string
	// Manifest is the parsed document.
	Manifest Manifest
	// Replacement resolves an operation to its final replacement text.
	Replacement map[string]string // operation id -> text
	// content holds verified content-file bytes keyed by manifest path.
	content map[string][]byte
}

// PathHint returns the manifest-relative path of a content file without joining it
// to the filesystem.
func cleanContentPath(rel string) (string, error) {
	if rel == "" {
		return "", fmt.Errorf("empty content path")
	}
	slash := filepath.ToSlash(rel)
	if strings.HasPrefix(slash, "/") || strings.Contains(slash, "..") {
		return "", fmt.Errorf("content path %q must be relative and must not traverse upward", rel)
	}
	return filepath.FromSlash(slash), nil
}

// Load reads and verifies a bundle directory.
func Load(dir string) (*Bundle, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	mp := filepath.Join(abs, ManifestName)
	raw, err := os.ReadFile(mp)
	if err != nil {
		return nil, fmt.Errorf("patch data: %w", err)
	}
	b := &Bundle{Dir: abs, ManifestPath: mp, ManifestSHA256: hashutil.Bytes(raw), Replacement: map[string]string{}, content: map[string][]byte{}}
	if err := json.Unmarshal(raw, &b.Manifest); err != nil {
		return nil, fmt.Errorf("%s: %w", mp, err)
	}
	if err := b.loadContent(); err != nil {
		return nil, err
	}
	if err := b.resolve(); err != nil {
		return nil, err
	}
	if err := b.Validate(); err != nil {
		return nil, err
	}
	return b, nil
}

func (b *Bundle) loadContent() error {
	for _, cf := range b.Manifest.ContentFiles {
		rel, err := cleanContentPath(cf.Path)
		if err != nil {
			return fmt.Errorf("contents: %w", err)
		}
		raw, err := os.ReadFile(filepath.Join(b.Dir, rel))
		if err != nil {
			return fmt.Errorf("content file %s: %w", cf.Path, err)
		}
		if cf.Size != 0 && int64(len(raw)) != cf.Size {
			return fmt.Errorf("content file %s: size %d != manifest %d", cf.Path, len(raw), cf.Size)
		}
		if cf.SHA256 != "" {
			if got := hashutil.Bytes(raw); got != cf.SHA256 {
				return fmt.Errorf("content file %s: sha256 %s != manifest %s", cf.Path, got, cf.SHA256)
			}
		}
		b.content[cf.Path] = raw
	}
	return nil
}

func (b *Bundle) resolve() error {
	for _, op := range b.Manifest.Operations {
		text := op.Replacement
		if op.Content != "" {
			raw, ok := b.content[op.Content]
			if !ok {
				return fmt.Errorf("operation %s: content %q is not listed in content_files", op.ID, op.Content)
			}
			if op.ContentSHA256 != "" && hashutil.Bytes(raw) != op.ContentSHA256 {
				return fmt.Errorf("operation %s: content sha256 mismatch", op.ID)
			}
			if text != "" {
				return fmt.Errorf("operation %s: both inline replacement and content file set", op.ID)
			}
			text = string(raw)
		}
		if text == "" {
			return fmt.Errorf("operation %s: replacement text is empty", op.ID)
		}
		b.Replacement[op.ID] = text
	}
	return nil
}

// Validate checks the manifest's structural and cross-file consistency.
func (b *Bundle) Validate() error {
	m := &b.Manifest
	if m.SpecVersion != SpecVersion {
		return fmt.Errorf("spec_version %d is not supported (want %d)", m.SpecVersion, SpecVersion)
	}
	if strings.TrimSpace(m.BundleID) == "" {
		return fmt.Errorf("bundle_id is required")
	}
	if strings.TrimSpace(m.BundleVersion) == "" {
		return fmt.Errorf("bundle_version is required")
	}
	if m.FormatVersion != "" && m.FormatVersion != SupportedFormatVersion {
		return fmt.Errorf("format_version %q is not supported (want %q)", m.FormatVersion, SupportedFormatVersion)
	}
	if len(m.Sources) == 0 {
		return fmt.Errorf("required_sources must list at least one input identity")
	}
	for i, s := range m.Sources {
		if strings.TrimSpace(s.Role) == "" {
			return fmt.Errorf("required_sources[%d]: role is required", i)
		}
		if s.Size < 0 {
			return fmt.Errorf("required_sources[%d]: negative size", i)
		}
		if s.SHA256 == "" || len(s.SHA256) != 64 {
			return fmt.Errorf("required_sources[%d]: sha256 must be 64 hex characters", i)
		}
	}
	seenID := map[string]bool{}
	seenOp := map[string]bool{}
	for i, op := range m.Operations {
		label := op.ID
		if label == "" {
			label = fmt.Sprintf("operations[%d]", i)
		}
		if op.ID == "" {
			return fmt.Errorf("operations[%d]: id is required", i)
		}
		if seenID[op.ID] {
			return fmt.Errorf("operations: duplicate id %q", op.ID)
		}
		seenID[op.ID] = true
		key := strings.ToLower(strings.ReplaceAll(op.Member, "\\", "/")) + "#" + fmt.Sprint(op.RecordIndex)
		if seenOp[key] {
			return fmt.Errorf("operation %s: member/record %s is targeted twice", label, key)
		}
		seenOp[key] = true
		switch op.Kind {
		case KindReplaceTextRecord:
		default:
			return fmt.Errorf("operation %s: unsupported kind %q", label, op.Kind)
		}
		if strings.TrimSpace(op.Member) == "" {
			return fmt.Errorf("operation %s: member is required", label)
		}
		if op.RecordIndex < 0 {
			return fmt.Errorf("operation %s: negative record_index", label)
		}
		if len(op.OriginalSHA256) != 64 {
			return fmt.Errorf("operation %s: original_sha256 must be 64 hex characters", label)
		}
		if _, ok := b.Replacement[op.ID]; !ok {
			return fmt.Errorf("operation %s: no replacement resolved", label)
		}
	}
	if !m.Output.PreserveUnmodified {
		return fmt.Errorf("output_constraints.preserve_unmodified_members must be true")
	}
	if m.Output.MemberCountDelta < 0 {
		return fmt.Errorf("output_constraints.member_count_delta must not be negative")
	}
	return nil
}

// OperationCount returns the number of patch operations.
func (b *Bundle) OperationCount() int { return len(b.Manifest.Operations) }

// SourceByRole returns the first source with the given role.
func (b *Bundle) SourceByRole(role string) (SourceSpec, bool) {
	for _, s := range b.Manifest.Sources {
		if strings.EqualFold(s.Role, role) {
			return s, true
		}
	}
	return SourceSpec{}, false
}

// ContentFile returns the verified bytes of a manifest content path.
func (b *Bundle) ContentFile(rel string) ([]byte, bool) {
	raw, ok := b.content[rel]
	return raw, ok
}
