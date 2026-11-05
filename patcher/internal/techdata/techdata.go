// Package techdata holds the patch's own authoritative technical data: the
// executable patch (IPS records only — offsets plus new payload bytes, never an
// original code copy), the target identity the patch is valid for, and the
// descriptors for resources the patch adds to the game archive.
//
// Everything here is patch-owned. No retail container, key, corpus, archive or
// game-derived image is embedded or read by this package. The one image asset
// it embeds is the patch's own overlay artwork, drawn by this project and
// composited over an image the user derives from their own game files.
package techdata

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

//go:embed data/exefs_patch.ips data/plate_overlay.png data/patch.json
var files embed.FS

// Schema identifies the descriptor layout this package understands.
const Schema = "13s-fidelity-patch.1"

// MaxPairTXShift bounds the whole-block horizontal offset in the descriptor.
// It is larger than the lower-line alignment limit because the progressed-plate
// correction moves both lines, not one; the largest evidence-derived value is
// 351.5 px.
const MaxPairTXShift = 400.0

// IPSMagic and IPSEnd are the IPS32 container markers.
const (
	IPSMagic = "PATCH"
	IPSEnd   = "EOF"
)

// Identity pins the title revision the patch is valid for.
type Identity struct {
	TitleID       string `json:"title_id"`
	UpdateTitleID string `json:"update_title_id"`
	BuildID       string `json:"build_id"`
	UpdateVersion string `json:"update_version"`
	Region        string `json:"region"`
}

// ExecutablePatch describes the IPS delta applied to the program binary.
type ExecutablePatch struct {
	File        string `json:"file"`
	Format      string `json:"format"`
	Target      string `json:"target"`
	Records     int    `json:"records"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
	Description string `json:"description"`
}

// PairTransform is one two-line name block inside an order-plate member:
// the first S4 record of each name group, the keyframe count and the optional
// constant horizontal offset applied to the given-name group after the swap.
type PairTransform struct {
	// GivenBlock and SurnameBlock are the first S4 record of the two name
	// groups (given name in the upper slot, surname in the lower slot).
	GivenBlock   int `json:"given_block"`
	SurnameBlock int `json:"surname_block"`
	// Keyframes is the number of S4 records per name group.
	Keyframes int `json:"keyframes"`
	// GivenTXShift is a constant horizontal offset applied to the given-name
	// group (0 keeps its original horizontal envelope). For the story plates it
	// is the per-character left-edge alignment of the lower given-name line
	// with the upper surname line.
	GivenTXShift float64 `json:"given_tx_shift,omitempty"`
	// GivenTXBasis records why that offset was chosen (measured frame, S3
	// sprite geometry, or "already flush/inset"), so the value is reviewable
	// without re-deriving it.
	GivenTXBasis string `json:"given_tx_basis,omitempty"`
	// PairTXShift is a second, independent horizontal offset applied to BOTH
	// name groups over every keyframe, moving the whole two-line block while
	// preserving the relative alignment set by GivenTXShift. It is only legal
	// on the progressed/mode-select pair; the story pair must keep it at zero.
	PairTXShift float64 `json:"pair_tx_shift,omitempty"`
	// PairTXBasis records the capture/geometry derivation of the block shift.
	PairTXBasis string `json:"pair_tx_basis,omitempty"`
	// Slot names the presentation this pair drives ("story_intro",
	// "progressed_plate"), for reports only.
	Slot string `json:"slot,omitempty"`
}

// MemberTransform describes the byte-level edits a transform resource applies to
// the user-derived base member (kind "order_plate"). It is a small,
// patch-owned recipe: the base bytes are never embedded or committed.
type MemberTransform struct {
	// S4Offset is the absolute file offset of the FMBS S4 transform table.
	S4Offset int `json:"s4_offset"`
	// PairTransform is the story-introduction name block.
	PairTransform
	// GivenName and SurnameName are the baked words, for reports only.
	GivenName   string `json:"given_name,omitempty"`
	SurnameName string `json:"surname_name,omitempty"`
	// SecondPair is the same character's progressed/mode-select name block,
	// carried by the same member when the asset has one. It is edited with the
	// identical verified operation.
	SecondPair *PairTransform `json:"second_pair,omitempty"`
}

// AddedResource is one archive member the patch adds.
type AddedResource struct {
	Member      string `json:"member"`
	Kind        string `json:"kind"`
	Recipe      string `json:"recipe"`
	LogicalSize int64  `json:"logical_size"`
	Overlay     string `json:"overlay"`
	BaseHint    string `json:"base_hint"`
	BaseLabel   string `json:"base_label"`
	Description string `json:"description"`
	// BaseSize and BaseSHA256 pin the user-derived base member a transform
	// resource is applied to. A mismatching base is refused, never patched
	// blindly.
	BaseSize   int64  `json:"base_size,omitempty"`
	BaseSHA256 string `json:"base_sha256,omitempty"`
	// Transform is present for kind "order_plate".
	Transform *MemberTransform `json:"transform,omitempty"`
	// RuntimeStatus records the last runtime observation for this
	// resource. It is never a claim of success: it exists so a build report can
	// never silently present an unproven resource as working.
	RuntimeStatus string `json:"runtime_status,omitempty"`
}

// Manifest is the decoded descriptor document.
type Manifest struct {
	Schema          string          `json:"schema"`
	Game            Identity        `json:"game"`
	ExecutablePatch ExecutablePatch `json:"executable_patch"`
	AddedResources  []AddedResource `json:"added_resources"`
}

var manifest Manifest

func init() {
	raw, err := files.ReadFile("data/patch.json")
	if err != nil {
		panic("techdata: embedded manifest missing: " + err.Error())
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		panic("techdata: embedded manifest invalid: " + err.Error())
	}
	if manifest.Schema != Schema {
		panic("techdata: unexpected manifest schema " + manifest.Schema)
	}
}

// Manifest returns the decoded descriptor document.
func ManifestInfo() Manifest { return manifest }

// Identity returns the target identity.
func IdentityInfo() Identity { return manifest.Game }

// BuildIDUpper is the program build identifier, upper-cased. The value is
// verified against the embedded patch digest by Verify.
func BuildIDUpper() string { return strings.ToUpper(manifest.Game.BuildID) }

// BuildIDLower is the program build identifier, lower-cased.
func BuildIDLower() string { return strings.ToLower(manifest.Game.BuildID) }

// TitleID is the base application id the patch targets.
func TitleID() string { return manifest.Game.TitleID }

// ExecutablePatchBytes returns the IPS delta bytes.
func ExecutablePatchBytes() []byte {
	raw, err := files.ReadFile("data/" + manifest.ExecutablePatch.File)
	if err != nil {
		panic("techdata: embedded executable patch missing: " + err.Error())
	}
	return raw
}

// ExecutablePatchSHA256 returns the digest recorded in the manifest.
func ExecutablePatchSHA256() string { return manifest.ExecutablePatch.SHA256 }

// ExecutablePatchSize returns the recorded size.
func ExecutablePatchSize() int64 { return manifest.ExecutablePatch.Size }

// OverlayImage returns the patch-owned overlay artwork bytes.
func OverlayImage() ([]byte, error) {
	return files.ReadFile("data/" + overlayName())
}

func overlayName() string {
	for _, r := range manifest.AddedResources {
		if r.Overlay != "" {
			return r.Overlay
		}
	}
	return "plate_overlay.png"
}

// AddedResources returns the descriptors for members the patch adds.
func AddedResources() []AddedResource { return manifest.AddedResources }

// BaseMemberPaths returns the game-derived logical members a normal build
// consumes directly, in descriptor order and without duplicates. These are the
// members the extractor stages from the user's own base container: the plate
// scripts and the title atlas. The manual-page background is the project's
// shipped presentation plate (or an optional user override), not a member
// consumed verbatim, so it is not listed here.
func BaseMemberPaths() []string {
	var out []string
	seen := map[string]bool{}
	for _, r := range manifest.AddedResources {
		switch r.Kind {
		case "order_plate", "compose_title":
			if r.BaseHint == "" || seen[r.BaseHint] {
				continue
			}
			seen[r.BaseHint] = true
			out = append(out, r.BaseHint)
		}
	}
	return out
}

// Verify confirms the embedded data is self-consistent: the IPS container
// markers are present, its size and digest match the manifest, and every added
// resource names an overlay that is embedded.
func Verify() error {
	raw := ExecutablePatchBytes()
	if len(raw) < 8 || string(raw[:5]) != IPSMagic || string(raw[len(raw)-3:]) != IPSEnd {
		return fmt.Errorf("embedded executable patch is not an IPS container")
	}
	if int64(len(raw)) != manifest.ExecutablePatch.Size {
		return fmt.Errorf("embedded executable patch size %d does not match the manifest %d", len(raw), manifest.ExecutablePatch.Size)
	}
	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); got != manifest.ExecutablePatch.SHA256 {
		return fmt.Errorf("embedded executable patch digest %s does not match the manifest", got)
	}
	for _, r := range manifest.AddedResources {
		if strings.TrimSpace(r.Member) == "" {
			return fmt.Errorf("added resource with an empty member path")
		}
		if r.Kind == "order_plate" {
			if len(r.BaseSHA256) != 64 || r.BaseSize <= 0 {
				return fmt.Errorf("added resource %s: missing base member identity", r.Member)
			}
			t := r.Transform
			if t == nil || t.S4Offset <= 0 || !validPairTransform(t.PairTransform) {
				return fmt.Errorf("added resource %s: invalid transform descriptor", r.Member)
			}
			// The story-introduction pair is the reference layout and must never
			// receive the progressed-plate whole-block shift.
			if t.PairTXShift != 0 {
				return fmt.Errorf("added resource %s: story-introduction pair must not carry a whole-pair tx shift", r.Member)
			}
			if t.SecondPair == nil {
				return fmt.Errorf("added resource %s: missing progressed second-pair transform descriptor", r.Member)
			}
			if !validPairTransform(*t.SecondPair) {
				return fmt.Errorf("added resource %s: invalid second-pair transform descriptor", r.Member)
			}
		}
		if r.Overlay == "" {
			continue
		}
		if _, err := files.ReadFile("data/" + r.Overlay); err != nil {
			return fmt.Errorf("added resource %s overlay %s is not embedded", r.Member, r.Overlay)
		}
	}
	return nil
}

// validPairTransform checks one two-line name block descriptor.
func validPairTransform(t PairTransform) bool {
	return t.GivenBlock >= 0 && t.SurnameBlock > t.GivenBlock &&
		t.Keyframes >= 2 && t.Keyframes <= 7 &&
		!math.IsNaN(t.GivenTXShift) && !math.IsInf(t.GivenTXShift, 0) &&
		math.Abs(t.GivenTXShift) <= 300 &&
		!math.IsNaN(t.PairTXShift) && !math.IsInf(t.PairTXShift, 0) &&
		math.Abs(t.PairTXShift) <= MaxPairTXShift
}

// OrderPlateResources returns the descriptors of the story-plate order
// transforms, in manifest order.
func OrderPlateResources() []AddedResource {
	var out []AddedResource
	for _, r := range manifest.AddedResources {
		if r.Kind == "order_plate" {
			out = append(out, r)
		}
	}
	return out
}

// ParseIPS counts the records in an IPS32 container and returns the highest
// patched address. It is used to describe and verify the executable patch
// without applying it.
func ParseIPS(raw []byte) (records int, maxAddr uint32, err error) {
	if len(raw) < 8 || string(raw[:5]) != IPSMagic {
		return 0, 0, fmt.Errorf("not an IPS container")
	}
	i := 5
	for i+3 <= len(raw) {
		if i+3 == len(raw)-3 && string(raw[i:i+3]) == IPSEnd {
			break
		}
		if string(raw[i:i+3]) == IPSEnd {
			i += 3
			if i == len(raw) {
				return records, maxAddr, nil
			}
			continue
		}
		if i+3 > len(raw)-1 {
			return 0, 0, fmt.Errorf("truncated IPS record header")
		}
		addr := uint32(raw[i])<<16 | uint32(raw[i+1])<<8 | uint32(raw[i+2])
		i += 3
		if i+2 > len(raw) {
			return 0, 0, fmt.Errorf("truncated IPS record length")
		}
		size := int(raw[i])<<8 | int(raw[i+1])
		i += 2
		if size == 0 { // RLE escape
			if i+3 > len(raw) {
				return 0, 0, fmt.Errorf("truncated IPS RLE record")
			}
			run := int(raw[i])<<8 | int(raw[i+1])
			i += 3
			records++
			if addr+uint32(run) > maxAddr {
				maxAddr = addr + uint32(run)
			}
			continue
		}
		if i+size > len(raw) {
			return 0, 0, fmt.Errorf("truncated IPS record payload")
		}
		i += size
		records++
		if addr+uint32(size) > maxAddr {
			maxAddr = addr + uint32(size)
		}
	}
	return 0, 0, fmt.Errorf("IPS container without an end marker")
}
