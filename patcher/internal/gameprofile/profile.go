// Package gameprofile holds the fixed identities of the supported title: the
// known container digests, title ids and version labels the integrated workflow
// validates against. Identifiers are content digests, never keys or game data.
package gameprofile

import "13s-patcher/internal/translations"

// Accepted container identities. A build accepts exactly these inputs; a file
// with the same name but a different digest is refused. The title identity is
// the single source shared with the translation dataset manifest.
const (
	// Title metadata.
	Title         = translations.GameTitle
	Region        = translations.GameRegion
	BaseVersion   = translations.GameBaseVersion
	UpdateVersion = translations.GameUpdateVersion
	TitleIDBase   = translations.GameTitleID
	TitleIDUpdate = "01003FC01670C800"
	TitleIDExtra  = "010045C014650000"

	// Base region archive.
	BaseSize   = int64(4640494528)
	BaseSHA256 = "0d6caff60ebd473b657a8ea04da02537d8ee1cb3390cc90fc02457f468258800"

	// Update container (raw version field 65536 is the 1.10 revision).
	UpdateSize       = int64(24227776)
	UpdateSHA256     = "6e3b28265cc061c452a7c00d1077eb2f88bc193fd378deefc8ed1f9827edf3c8"
	UpdateVersionRaw = uint64(65536)

	// Second-region base archive (research input; never required to build).
	ExtraSize   = int64(4291745092)
	ExtraSHA256 = "4582de8ba9b2e12a03dddecb1ddd78c8e2fab1eec49f5f4bd1592ddb35292444"

	// The effective 1.10 overlay archive: a single added file shipped by the
	// update, applied over the base region archive.
	OverlaySize   = int64(4733704)
	OverlaySHA256 = "13ced0d0e0a7396335682aceedaf43f298edb9fc7e984c0fb27ab777ef0b1450"
)

// Input describes one accepted container.
type Input struct {
	Role           string `json:"role"`
	TitleID        string `json:"title_id"`
	DisplayVersion string `json:"display_version,omitempty"`
	VersionRaw     uint64 `json:"version_raw,omitempty"`
	Size           int64  `json:"size"`
	SHA256         string `json:"sha256"`
	// Required marks inputs the public build flow needs. The second-region
	// archive is research input and is never required.
	Required bool `json:"required"`
}

// KnownInputs lists the accepted container identities.
var KnownInputs = []Input{
	{Role: "base", TitleID: TitleIDBase, DisplayVersion: BaseVersion, Size: BaseSize, SHA256: BaseSHA256, Required: true},
	{Role: "update", TitleID: TitleIDUpdate, DisplayVersion: UpdateVersion, VersionRaw: UpdateVersionRaw, Size: UpdateSize, SHA256: UpdateSHA256, Required: true},
	{Role: "extra", TitleID: TitleIDExtra, DisplayVersion: BaseVersion, Size: ExtraSize, SHA256: ExtraSHA256, Required: false},
}

// Lookup returns the known input matching a (size, digest) pair.
func Lookup(size int64, sha string) *Input {
	for i := range KnownInputs {
		if KnownInputs[i].Size == size && KnownInputs[i].SHA256 == sha {
			return &KnownInputs[i]
		}
	}
	return nil
}

// Game returns the dataset metadata for the supported title.
func Game() translations.GameInfo { return translations.ExpectedGame }

// OverlayTarget returns the dataset target identity for the effective overlay.
func OverlayTarget() translations.SourceIdentity {
	return translations.SourceIdentity{Role: "overlay", Size: OverlaySize, SHA256: OverlaySHA256}
}
