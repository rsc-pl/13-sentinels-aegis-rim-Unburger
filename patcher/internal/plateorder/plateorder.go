// Package plateorder applies the surname-first order transform to the
// per-protagonist story-introduction plate scripts (GUI/change_<name>.mbs).
//
// The name lines on these plates are baked sprites whose presentation slots are
// the S4 transform records of the FMBS container. Each two-line name block is
// two consecutive groups of keyframes: the given name occupies the upper slot
// and the surname the lower slot. The transform exchanges only the ty (vertical
// centre) floats of the two groups for all keyframes, and optionally applies one
// constant horizontal offset to the given-name group. That offset is the
// per-character left-edge alignment of the lower given-name line with the upper
// surname line: after the swap the given name occupies the second line, and
// without a shift a word that started to the left of the surname in the base
// plate would hang outside the block. Zero is a deliberate value for plates
// whose two lines are already flush or already inset. Texture, UVs, local
// bounds, scale/alpha ramps, clip data, node mapping, header and footer are
// never touched, so the edit is lossless apart from the intended presentation
// fields.
//
// One logical member can carry more than one name block: the story-introduction
// plate (with The Story of) uses one pair of keyframe groups, and the
// progressed/mode-select presentation of the same character uses a second pair
// in the same file. Apply walks every configured pair with the same verified
// operation; the pairs are required to be disjoint.
//
// The progressed pair can additionally receive a whole-block PairTXShift on
// all keyframes of both groups. That shifts the two lines together without
// changing their relative alignment, and is used to move the block out of the
// black Japanese calligraphy. The story pair is refused if it carries one.
//
// This package contains no game data: the caller supplies the user-supplied
// logical member, which is verified against the pinned base digest before any
// byte is written.
package plateorder

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"strings"
)

// FMBS container constants (verified against the FMBS container layout).
const (
	Magic          = "FMBS"
	HeaderSize     = 0x130
	SectionTable   = 0xB0
	SectionEntries = 11
	S4Entry        = 7
	S4Stride       = 36
	S4TXOffset     = 0x14
	S4TYOffset     = 0x18
)

// MaxGivenTXShift bounds the per-character horizontal offset. The largest
// evidence-derived plate value is 190.5 px; a descriptor far outside that range
// is a data error, not a composition refinement.
const MaxGivenTXShift = 300.0

// MaxPairTXShift bounds the whole-block horizontal offset. The largest value
// justified by the supplied capture geometry is about 351.5 px; anything far
// outside that is a data error. This offset moves both name groups together, so
// it is a different quantity from the lower-line alignment (MaxGivenTXShift).
const MaxPairTXShift = 400.0

// PairSpec is one two-line name block. All offsets refer to the logical
// (decompressed) member and to the S4 table; S4Offset lives on Spec because it
// is shared by every pair of the member.
type PairSpec struct {
	GivenBlock   int     // first S4 record of the given-name group (upper slot)
	SurnameBlock int     // first S4 record of the surname group (lower slot)
	Keyframes    int     // keyframes per group
	GivenTXShift float64 // constant horizontal offset for the given-name group
	// PairTXShift is a second, independent horizontal offset applied to BOTH
	// groups over every keyframe. It moves the whole two-line block without
	// changing the relative left-edge alignment produced by GivenTXShift.
	// It is only used on the progressed/mode-select pair; the story pair must
	// keep it at zero.
	PairTXShift  float64
	PairTXBasis  string // why the whole-block offset was chosen, for reports
	GivenTXBasis string // why that offset was chosen, for reports
	GivenLabel   string // display word, for reports
	SurnameLabel string // display word, for reports
	Slot         string // report label, e.g. "story_intro" or "progressed_plate"
}

// Spec is one character's order transform. S4Offset is the absolute file offset
// of the S4 table. PairSpec is the story-introduction pair; Second is the same
// character's second (progressed/mode-select) pair when the resource carries
// one.
type Spec struct {
	Member     string // archive member, e.g. "GUI/change_kurabe.mbs"
	BaseSize   int    // expected logical size of the user-supplied base
	BaseSHA256 string // expected logical digest of the user-supplied base
	S4Offset   int    // absolute file offset of the S4 section
	PairSpec
	Second *PairSpec
}

// Position is a name group's centre before or after the transform.
type Position struct {
	TX float64 `json:"tx"`
	TY float64 `json:"ty"`
}

// PairResult is the provable edit report for one name block.
type PairResult struct {
	Slot           string   `json:"slot,omitempty"`
	GivenBlock     int      `json:"given_block"`
	SurnameBlock   int      `json:"surname_block"`
	Keyframes      int      `json:"keyframes"`
	GivenLabel     string   `json:"given_label"`
	SurnameLabel   string   `json:"surname_label"`
	GivenTXShift   float64  `json:"given_tx_shift"`
	GivenTXBasis   string   `json:"given_tx_basis,omitempty"`
	PairTXShift    float64  `json:"pair_tx_shift,omitempty"`
	PairTXBasis    string   `json:"pair_tx_basis,omitempty"`
	GivenBefore    Position `json:"given_before"`
	GivenAfter     Position `json:"given_after"`
	SurnameBefore  Position `json:"surname_before"`
	SurnameAfter   Position `json:"surname_after"`
	ChangedBytes   int      `json:"changed_bytes"`
	ChangedOffsets []int    `json:"-"`
}

// Result is the transformed logical member plus the provable edit report.
type Result struct {
	Member         string      `json:"member"`
	BaseSHA256     string      `json:"base_sha256"`
	LogicalSHA256  string      `json:"logical_sha256"`
	LogicalSize    int         `json:"logical_size"`
	ChangedBytes   int         `json:"changed_bytes"`
	ChangedOffsets []int       `json:"changed_offsets"`
	S4Offset       int         `json:"s4_offset"`
	PairResult                 // the story-introduction pair
	Second         *PairResult `json:"second_pair,omitempty"`
	Logical        []byte      `json:"-"`
}

func sha(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func f32(b []byte, off int) float64 {
	return float64(math.Float32frombits(uint32(b[off]) | uint32(b[off+1])<<8 | uint32(b[off+2])<<16 | uint32(b[off+3])<<24))
}

func putF32(b []byte, off int, v float64) {
	u := math.Float32bits(float32(v))
	b[off] = byte(u)
	b[off+1] = byte(u >> 8)
	b[off+2] = byte(u >> 16)
	b[off+3] = byte(u >> 24)
}

func u32(b []byte, off int) uint32 {
	return uint32(b[off]) | uint32(b[off+1])<<8 | uint32(b[off+2])<<16 | uint32(b[off+3])<<24
}

func u64(b []byte, off int) uint64 {
	return uint64(u32(b, off)) | uint64(u32(b, off+4))<<32
}

// sectionEnd returns the absolute file offset at which the section starting at
// payload offset target ends, using the container's section offset table.
func sectionEnd(b []byte, target uint64) (int, error) {
	payload := uint64(u32(b, 0x04))
	offsets := make([]uint64, 0, SectionEntries)
	for i := 0; i < SectionEntries; i++ {
		v := u64(b, SectionTable+8*i)
		if v == 0 {
			continue
		}
		if v >= payload {
			return 0, fmt.Errorf("section offset %#x outside payload (%#x)", v, payload)
		}
		offsets = append(offsets, v)
	}
	sort.Slice(offsets, func(i, j int) bool { return offsets[i] < offsets[j] })
	for i, v := range offsets {
		if v != target {
			continue
		}
		next := payload
		if i+1 < len(offsets) {
			next = offsets[i+1]
		}
		return HeaderSize + int(next), nil
	}
	return 0, fmt.Errorf("section offset %#x not listed in the section table", target)
}

// position validates one name group at base and returns its constant centre.
// The two groups of a block animate independently (fade/scale curves differ per
// character and per presentation), so the invariant that matters for a slot
// exchange is that each group's centre is constant over its keyframes and that
// the settled keyframe is opaque. The base digest pins the exact asset.
func position(b []byte, base, keyframes int, label string) (Position, error) {
	if keyframes < 2 || keyframes > 7 {
		return Position{}, fmt.Errorf("%s: unsupported keyframe count %d", label, keyframes)
	}
	var pos Position
	for i := 0; i < keyframes; i++ {
		off := base + i*S4Stride
		if off+S4Stride > len(b) {
			return Position{}, fmt.Errorf("%s: keyframe %d outside the section", label, i)
		}
		tx := f32(b, off+S4TXOffset)
		ty := f32(b, off+S4TYOffset)
		if math.IsNaN(tx) || math.IsNaN(ty) || math.IsInf(tx, 0) || math.IsInf(ty, 0) {
			return Position{}, fmt.Errorf("%s: keyframe %d has a non-finite centre", label, i)
		}
		if i == 0 {
			pos = Position{TX: tx, TY: ty}
			continue
		}
		if math.Abs(tx-pos.TX) > 1e-3 || math.Abs(ty-pos.TY) > 1e-3 {
			return Position{}, fmt.Errorf("%s: keyframe %d position (%g,%g) differs from (%g,%g)", label, i, tx, ty, pos.TX, pos.TY)
		}
	}
	last := base + (keyframes-1)*S4Stride
	if u32(b, last+0x10) != 0xFFFFFFFF {
		return Position{}, fmt.Errorf("%s: settled keyframe alpha %08x is not opaque", label, u32(b, last+0x10))
	}
	return pos, nil
}

// validatePair checks the bounds and the shift of one pair against an S4 table
// of the given record count.
func validatePair(member string, records int, p PairSpec) error {
	if p.Keyframes < 2 || p.Keyframes > 7 {
		return fmt.Errorf("%s: keyframe count %d out of range", member, p.Keyframes)
	}
	if math.IsNaN(p.GivenTXShift) || math.IsInf(p.GivenTXShift, 0) || math.Abs(p.GivenTXShift) > MaxGivenTXShift {
		return fmt.Errorf("%s: given-name tx shift %g is outside the supported +/-%g px", member, p.GivenTXShift, MaxGivenTXShift)
	}
	if math.IsNaN(p.PairTXShift) || math.IsInf(p.PairTXShift, 0) || math.Abs(p.PairTXShift) > MaxPairTXShift {
		return fmt.Errorf("%s: whole-pair tx shift %g is outside the supported +/-%g px", member, p.PairTXShift, MaxPairTXShift)
	}
	need := p.SurnameBlock + p.Keyframes
	if p.GivenBlock < 0 || p.SurnameBlock <= p.GivenBlock || need > records {
		return fmt.Errorf("%s: name groups %d/%d (+%d) outside the %d S4 records", member, p.GivenBlock, p.SurnameBlock, p.Keyframes, records)
	}
	return nil
}

func label(p PairSpec) string {
	if p.Slot == "" {
		return "name pair"
	}
	return p.Slot
}

// applyPair performs the verified exchange on dst and returns the pair report
// and the absolute offsets it touched.
func applyPair(member string, dst []byte, s4, records int, p PairSpec) (PairResult, map[int]bool, error) {
	if err := validatePair(member, records, p); err != nil {
		return PairResult{}, nil, err
	}
	baseGiven := s4 + p.GivenBlock*S4Stride
	baseSurname := s4 + p.SurnameBlock*S4Stride
	givenBefore, err := position(dst, baseGiven, p.Keyframes, label(p)+" given name ("+p.GivenLabel+")")
	if err != nil {
		return PairResult{}, nil, err
	}
	surnameBefore, err := position(dst, baseSurname, p.Keyframes, label(p)+" surname ("+p.SurnameLabel+")")
	if err != nil {
		return PairResult{}, nil, err
	}
	if givenBefore.TY >= surnameBefore.TY {
		return PairResult{}, nil, fmt.Errorf("%s: given name ty %g is not above the surname ty %g", label(p), givenBefore.TY, surnameBefore.TY)
	}

	touched := make(map[int]bool)
	for i := 0; i < p.Keyframes; i++ {
		ga := baseGiven + i*S4Stride
		sa := baseSurname + i*S4Stride
		gty := f32(dst, ga+S4TYOffset)
		sty := f32(dst, sa+S4TYOffset)
		putF32(dst, ga+S4TYOffset, sty)
		putF32(dst, sa+S4TYOffset, gty)
		for j := 0; j < 4; j++ {
			touched[ga+S4TYOffset+j] = true
			touched[sa+S4TYOffset+j] = true
		}
	}
	if p.GivenTXShift != 0 || p.PairTXShift != 0 {
		givenShift := p.GivenTXShift + p.PairTXShift
		for i := 0; i < p.Keyframes; i++ {
			ga := baseGiven + i*S4Stride
			sa := baseSurname + i*S4Stride
			if givenShift != 0 {
				putF32(dst, ga+S4TXOffset, f32(dst, ga+S4TXOffset)+givenShift)
				for j := 0; j < 4; j++ {
					touched[ga+S4TXOffset+j] = true
				}
			}
			if p.PairTXShift != 0 {
				putF32(dst, sa+S4TXOffset, f32(dst, sa+S4TXOffset)+p.PairTXShift)
				for j := 0; j < 4; j++ {
					touched[sa+S4TXOffset+j] = true
				}
			}
		}
	}

	givenAfter, err := position(dst, baseGiven, p.Keyframes, label(p)+" given name after")
	if err != nil {
		return PairResult{}, nil, err
	}
	surnameAfter, err := position(dst, baseSurname, p.Keyframes, label(p)+" surname after")
	if err != nil {
		return PairResult{}, nil, err
	}
	if givenAfter.TY != surnameBefore.TY || surnameAfter.TY != givenBefore.TY {
		return PairResult{}, nil, fmt.Errorf("%s: transform did not exchange the vertical slots", label(p))
	}
	if math.Abs(givenAfter.TX-(givenBefore.TX+p.GivenTXShift+p.PairTXShift)) > 1e-3 {
		return PairResult{}, nil, fmt.Errorf("%s: given-name horizontal shift did not apply", label(p))
	}
	if math.Abs(surnameAfter.TX-(surnameBefore.TX+p.PairTXShift)) > 1e-3 {
		return PairResult{}, nil, fmt.Errorf("%s: surname horizontal shift did not apply", label(p))
	}

	// The caller computes the actual diff against the base; the touched set lets
	// it enforce field locality. Per-pair changed-byte accounting is filled in
	// Apply from the global diff.
	return PairResult{
		Slot:          p.Slot,
		GivenBlock:    p.GivenBlock,
		SurnameBlock:  p.SurnameBlock,
		Keyframes:     p.Keyframes,
		GivenLabel:    p.GivenLabel,
		SurnameLabel:  p.SurnameLabel,
		GivenTXShift:  p.GivenTXShift,
		GivenTXBasis:  p.GivenTXBasis,
		PairTXShift:   p.PairTXShift,
		PairTXBasis:   p.PairTXBasis,
		GivenBefore:   givenBefore,
		GivenAfter:    givenAfter,
		SurnameBefore: surnameBefore,
		SurnameAfter:  surnameAfter,
	}, touched, nil
}

// Apply verifies the user-supplied base member and returns the transformed
// logical member. It never modifies the input slice.
func Apply(spec Spec, base []byte) (*Result, error) {
	if len(base) == 0 {
		return nil, fmt.Errorf("%s: empty base member", spec.Member)
	}
	if spec.BaseSize != 0 && len(base) != spec.BaseSize {
		return nil, fmt.Errorf("%s: base size %d does not match the pinned %d", spec.Member, len(base), spec.BaseSize)
	}
	baseSHA := sha(base)
	if spec.BaseSHA256 != "" && !strings.EqualFold(baseSHA, spec.BaseSHA256) {
		return nil, fmt.Errorf("%s: base digest %s does not match the pinned %s", spec.Member, baseSHA, spec.BaseSHA256)
	}
	if len(base) < 4 || string(base[:4]) != Magic {
		return nil, fmt.Errorf("%s: not an FMBS container", spec.Member)
	}
	payload := int(u32(base, 0x04))
	if len(base) != HeaderSize+payload {
		return nil, fmt.Errorf("%s: size %d does not match header payload %d", spec.Member, len(base), payload)
	}
	tableOffset := u64(base, SectionTable+8*S4Entry)
	if tableOffset == 0 {
		return nil, fmt.Errorf("%s: section table has no S4 entry", spec.Member)
	}
	if HeaderSize+int(tableOffset) != spec.S4Offset {
		return nil, fmt.Errorf("%s: S4 offset %#x does not match the pinned %#x", spec.Member, HeaderSize+int(tableOffset), spec.S4Offset)
	}
	end, err := sectionEnd(base, tableOffset)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", spec.Member, err)
	}
	s4Len := end - spec.S4Offset
	if s4Len <= 0 || s4Len%S4Stride != 0 {
		return nil, fmt.Errorf("%s: S4 length %d is not a whole number of records", spec.Member, s4Len)
	}
	records := s4Len / S4Stride

	// The story-introduction pair is the reference layout and must never receive
	// the progressed-plate whole-block shift. Enforce that at the transform
	// boundary, not only in the descriptor validator.
	if spec.PairSpec.PairTXShift != 0 {
		return nil, fmt.Errorf("%s: story-introduction pair must not carry a whole-pair tx shift (got %g)", spec.Member, spec.PairSpec.PairTXShift)
	}
	pairs := []PairSpec{spec.PairSpec}
	if spec.Second != nil {
		pairs = append(pairs, *spec.Second)
	}
	// Every configured pair must be inside the table and disjoint from the
	// others: one pair's edit may never touch another pair's records.
	for i := range pairs {
		if err := validatePair(spec.Member, records, pairs[i]); err != nil {
			return nil, err
		}
		for j := 0; j < i; j++ {
			if rangesOverlap(pairs[i], pairs[j]) {
				return nil, fmt.Errorf("%s: name pairs %q and %q overlap", spec.Member, label(pairs[j]), label(pairs[i]))
			}
		}
	}

	out := append([]byte(nil), base...)
	touched := make(map[int]bool)
	results := make([]PairResult, len(pairs))
	for i := range pairs {
		pr, t, err := applyPair(spec.Member, out, spec.S4Offset, records, pairs[i])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", spec.Member, err)
		}
		for off := range t {
			touched[off] = true
		}
		results[i] = pr
	}

	// Proven invariant: no byte outside the touched fields differs.
	changed := make([]int, 0, len(touched))
	for i := range out {
		if out[i] == base[i] {
			continue
		}
		if !touched[i] {
			return nil, fmt.Errorf("%s: unexpected change at offset %#x", spec.Member, i)
		}
		changed = append(changed, i)
	}

	// Attribute the changed bytes to the pair that owns them.
	for i := range results {
		lo := spec.S4Offset + results[i].GivenBlock*S4Stride
		hi := spec.S4Offset + (results[i].SurnameBlock+results[i].Keyframes)*S4Stride
		for _, off := range changed {
			if off >= lo && off < hi {
				results[i].ChangedOffsets = append(results[i].ChangedOffsets, off)
			}
		}
		results[i].ChangedBytes = len(results[i].ChangedOffsets)
	}

	res := &Result{
		Member:         spec.Member,
		BaseSHA256:     baseSHA,
		LogicalSHA256:  sha(out),
		LogicalSize:    len(out),
		ChangedBytes:   len(changed),
		ChangedOffsets: changed,
		S4Offset:       spec.S4Offset,
		PairResult:     results[0],
		Logical:        out,
	}
	if len(results) > 1 {
		res.Second = &results[1]
	}
	return res, nil
}

func rangesOverlap(a, b PairSpec) bool {
	a0, a1 := a.GivenBlock, a.SurnameBlock+a.Keyframes
	b0, b1 := b.GivenBlock, b.SurnameBlock+b.Keyframes
	return a0 < b1 && b0 < a1
}

func (p PairResult) describe() string {
	out := fmt.Sprintf("%s above %s · %d keyframes · %d byte(s) changed · given (%g,%g)→(%g,%g) · surname (%g,%g)→(%g,%g)",
		p.SurnameLabel, p.GivenLabel, p.Keyframes, p.ChangedBytes,
		p.GivenBefore.TX, p.GivenBefore.TY, p.GivenAfter.TX, p.GivenAfter.TY,
		p.SurnameBefore.TX, p.SurnameBefore.TY, p.SurnameAfter.TX, p.SurnameAfter.TY)
	if p.GivenTXShift != 0 {
		out += fmt.Sprintf(" · lower line +%g px", p.GivenTXShift)
	}
	if p.PairTXShift != 0 {
		out += fmt.Sprintf(" · whole block %+g px", p.PairTXShift)
	}
	return out
}

// Describe renders a one-line summary of the applied transforms.
func (r *Result) Describe() string {
	out := r.PairResult.describe()
	if r.Second != nil {
		out += " · second pair " + r.Second.describe()
	}
	return out
}
