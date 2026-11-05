// Package patchdata defines and loads the public patch-data bundle format.
//
// A bundle is a directory containing a JSON manifest (manifest.json) plus any
// content files the manifest references. The manifest:
//
//   - names the bundle and its own release version (independent from the app),
//   - declares the game base version and format version it targets,
//   - lists the exact source archive identities (size + SHA-256) it builds from,
//   - lists opaque, individually validated patch operations,
//   - hashes every content file,
//   - declares the output constraints a successful build must satisfy.
//
// The format deliberately stores only what a consumer needs: an operation
// identifies a member and a record index and pins the expected original text by
// SHA-256 (never an authoring history), plus the replacement text. No private
// authoring data is required or represented.
package patchdata

// SpecVersion is the manifest schema version this package understands.
const SpecVersion = 1

// SupportedFormatVersion is the resource format family the operations target.
const SupportedFormatVersion = "cpk-fmsb-1"

// Operation kinds.
const (
	// KindReplaceTextRecord replaces one string inside one FMSB text bank member.
	KindReplaceTextRecord = "replace_text_record"
)

// Manifest is the bundle manifest document.
type Manifest struct {
	SpecVersion    int           `json:"spec_version"`
	BundleID       string        `json:"bundle_id"`
	BundleVersion  string        `json:"bundle_version"`
	ReleaseVersion string        `json:"release_version"`
	CreatedUTC     string        `json:"created_utc,omitempty"`
	Notes          string        `json:"notes,omitempty"`
	Game           GameInfo      `json:"game"`
	FormatVersion  string        `json:"format_version"`
	Sources        []SourceSpec  `json:"required_sources"`
	Operations     []Operation   `json:"operations"`
	ContentFiles   []ContentFile `json:"content_files"`
	Output         OutputSpec    `json:"output_constraints"`
}

// GameInfo identifies the supported game and base version.
type GameInfo struct {
	Title       string `json:"title,omitempty"`
	TitleID     string `json:"title_id,omitempty"`
	Region      string `json:"region,omitempty"`
	BaseVersion string `json:"base_version,omitempty"`
}

// SourceSpec is one required input identity.
type SourceSpec struct {
	// Role is a stable machine role, e.g. "patch_archive".
	Role string `json:"role"`
	// Name is a human-readable label shown in preflight output.
	Name string `json:"name,omitempty"`
	// PathHint is an optional relative/absolute hint used for auto-detection.
	PathHint string `json:"path_hint,omitempty"`
	// Size and SHA256 pin the exact accepted input.
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Operation is one validated patch operation.
type Operation struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	// Member is the archive member path, e.g. "Fms/ScriptText.fms".
	Member string `json:"member"`
	// MemberLogicalSHA256 optionally pins the whole member's logical content.
	MemberLogicalSHA256 string `json:"member_logical_sha256,omitempty"`
	// RecordIndex is the zero-based string index inside the bank.
	RecordIndex int `json:"record_index"`
	// OriginalSHA256 pins the expected original record text.
	OriginalSHA256 string `json:"original_sha256"`
	// Original is an optional inline copy of the original text (audit only).
	Original string `json:"original,omitempty"`
	// Replacement is the new text, either inline or via a content file.
	Replacement string `json:"replacement,omitempty"`
	// Content is the manifest-relative path of a content file holding the text.
	Content string `json:"content,omitempty"`
	// ContentSHA256 pins Content when it is used.
	ContentSHA256 string `json:"content_sha256,omitempty"`
	// PreserveTokens requires the renderer control-token sequence to be unchanged
	// (branch/alternate-selection markup stays intact).
	PreserveTokens bool `json:"preserve_tokens"`
	// AllowTokenChange, when true, explicitly permits a control-token change for
	// this operation (a deliberate, reviewed exception).
	AllowTokenChange bool `json:"allow_token_change,omitempty"`
}

// ContentFile is one content file carried by the bundle.
type ContentFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// OutputSpec declares the constraints a successful build must satisfy.
type OutputSpec struct {
	// MemberCountDelta is the expected change in member count (usually 0).
	MemberCountDelta int `json:"member_count_delta,omitempty"`
	// PreserveUnmodified requires every untouched member to keep its stored bytes.
	PreserveUnmodified bool `json:"preserve_unmodified_members"`
	// ExpectedSHA256 optionally pins the exact output digest.
	ExpectedSHA256 string `json:"expected_output_sha256,omitempty"`
	// ExpectedSize optionally pins the exact output size.
	ExpectedSize int64 `json:"expected_output_size,omitempty"`
	// Encoder names the codec encoder to use ("" = "chain").
	Encoder string `json:"encoder,omitempty"`
}
