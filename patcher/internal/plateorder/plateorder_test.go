package plateorder

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"math"
	"strings"
	"testing"
)

// storyRamp is the settle ramp of the story-introduction name groups. It is a
// test fixture value, not a transform input: the transform only touches the
// (tx,ty) fields and never reads or rewrites the scale/alpha curves.
var storyRamp = [7]float32{1.0, 1.0, 1.4, 1.3, 1.2, 1.1, 1.025}

// synthPlate builds a minimal FMBS container shaped like the game's plate
// scripts: header 0x130, a section offset table at 0xB0 with an S4 entry, and an
// S4 table of 36-byte transform records. It contains no game data.
func synthPlate(givenBlock, surnameBlock int, givenTX, givenTY, surnameTX, surnameTY float32) []byte {
	return synthPlatePairs([]synthPair{{givenBlock, surnameBlock, givenTX, givenTY, surnameTX, surnameTY}})
}

type synthPair struct {
	givenBlock, surnameBlock int
	givenTX, givenTY         float32
	surnameTX, surnameTY     float32
}

func synthPlatePairs(pairs []synthPair) []byte {
	const payload = 0x2000
	const s4Payload = 0x100
	const records = 120
	s4File := HeaderSize + s4Payload
	b := make([]byte, HeaderSize+payload)
	copy(b, Magic)
	binary.LittleEndian.PutUint32(b[4:], payload)
	// section table: three plausible non-zero entries, S4 at index 7
	binary.LittleEndian.PutUint64(b[SectionTable+8*0:], 0x40)
	binary.LittleEndian.PutUint64(b[SectionTable+8*1:], 0x80)
	binary.LittleEndian.PutUint64(b[SectionTable+8*S4Entry:], uint64(s4Payload))
	copy(b[0x80:], "change_test.mbs")
	put := func(off int, v float32) { binary.LittleEndian.PutUint32(b[off:], math.Float32bits(v)) }
	writeGroup := func(block int, tx, ty float32) {
		for i := 0; i < 7; i++ {
			off := s4File + (block+i)*S4Stride
			put(off+0x08, storyRamp[i])
			put(off+0x0C, storyRamp[i])
			alpha := uint32(0xFFFFFFFF)
			if i == 0 {
				alpha = 0x00FFFFFF
			}
			binary.LittleEndian.PutUint32(b[off+0x10:], alpha)
			put(off+S4TXOffset, tx)
			put(off+S4TYOffset, ty)
		}
	}
	for _, p := range pairs {
		writeGroup(p.givenBlock, p.givenTX, p.givenTY)
		writeGroup(p.surnameBlock, p.surnameTX, p.surnameTY)
	}
	// mark the end of S4 with a following section
	binary.LittleEndian.PutUint64(b[SectionTable+8*8:], uint64(s4Payload+records*S4Stride))
	return b
}

func baseSpec(b []byte) Spec {
	sum := sha256.Sum256(b)
	return Spec{
		Member:     "GUI/change_test.mbs",
		BaseSize:   len(b),
		BaseSHA256: hex.EncodeToString(sum[:]),
		S4Offset:   HeaderSize + 0x100,
		PairSpec: PairSpec{
			GivenBlock:   10,
			SurnameBlock: 17,
			Keyframes:    7,
			GivenLabel:   "GIVEN",
			SurnameLabel: "SURNAME",
		},
	}
}

func TestApplySwapsVerticalSlots(t *testing.T) {
	base := synthPlate(10, 17, -190, 82, -38, 129)
	spec := baseSpec(base)
	res, err := Apply(spec, base)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.GivenAfter.TY != 129 || res.SurnameAfter.TY != 82 {
		t.Fatalf("slots not exchanged: given %v surname %v", res.GivenAfter, res.SurnameAfter)
	}
	if res.GivenAfter.TX != -190 || res.SurnameAfter.TX != -38 {
		t.Fatalf("horizontal positions changed: %v %v", res.GivenAfter, res.SurnameAfter)
	}
	for _, off := range res.ChangedOffsets {
		if !inSlot(off, spec.S4Offset, spec.GivenBlock, spec.SurnameBlock) {
			t.Fatalf("unexpected changed offset %#x", off)
		}
	}
	if len(res.ChangedOffsets) == 0 || res.ChangedBytes != len(res.ChangedOffsets) {
		t.Fatalf("changed byte accounting: %d %d", res.ChangedBytes, len(res.ChangedOffsets))
	}
	// input must never be modified
	if got := math.Float32frombits(binary.LittleEndian.Uint32(base[spec.S4Offset+spec.GivenBlock*S4Stride+S4TYOffset:])); got != 82 {
		t.Fatalf("input mutated: %v", got)
	}
}

func inSlot(off, s4, given, surname int) bool {
	for i := 0; i < 7; i++ {
		ga := s4 + (given+i)*S4Stride
		sa := s4 + (surname+i)*S4Stride
		if (off >= ga+S4TXOffset && off < ga+S4TXOffset+8) || (off >= sa+S4TXOffset && off < sa+S4TXOffset+8) {
			return true
		}
	}
	return false
}

func TestApplyHorizontalShift(t *testing.T) {
	base := synthPlate(10, 17, -190, 82, -38, 129)
	spec := baseSpec(base)
	spec.GivenTXShift = 53
	res, err := Apply(spec, base)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.GivenAfter.TX != -137 {
		t.Fatalf("given tx %v, want -137", res.GivenAfter.TX)
	}
	if res.SurnameAfter.TX != -38 {
		t.Fatalf("surname tx moved to %v", res.SurnameAfter.TX)
	}
	// the shift is constant over every keyframe, so the animation curve is
	// unchanged; only the four tx bytes of each given-name record differ.
	for i := 0; i < 7; i++ {
		off := spec.S4Offset + (spec.GivenBlock+i)*S4Stride + S4TXOffset
		if got := math.Float32frombits(binary.LittleEndian.Uint32(res.Logical[off:])); got != -137 {
			t.Fatalf("keyframe %d tx %v, want -137", i, got)
		}
	}
}

// TestApplyLargeConfirmedShift covers the largest evidence-derived push
// (Hijiyama progressed plate, +190.5 px) and proves the surname envelope is
// untouched.
func TestApplyLargeConfirmedShift(t *testing.T) {
	base := synthPlate(10, 17, -535, -154, -373, -107)
	spec := baseSpec(base)
	spec.GivenTXShift = 190.5
	res, err := Apply(spec, base)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.GivenAfter.TX != -344.5 || res.GivenAfter.TY != -107 {
		t.Fatalf("given after %v, want (-344.5,-107)", res.GivenAfter)
	}
	if res.SurnameAfter.TX != -373 || res.SurnameAfter.TY != -154 {
		t.Fatalf("surname after %v, want (-373,-154)", res.SurnameAfter)
	}
}

// TestApplySecondPair proves the same verified exchange is applied to a second
// name block in the same member, with disjoint changed bytes and per-pair
// accounting.
func TestApplySecondPair(t *testing.T) {
	base := synthPlatePairs([]synthPair{
		{10, 17, -190, 82, -38, 129},
		{40, 47, -630, -248, -478, -201},
	})
	spec := baseSpec(base)
	spec.Second = &PairSpec{
		GivenBlock:   40,
		SurnameBlock: 47,
		Keyframes:    7,
		GivenTXShift: 34.5,
		Slot:         "progressed_plate",
		GivenLabel:   "GIVEN",
		SurnameLabel: "SURNAME",
	}
	res, err := Apply(spec, base)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.Second == nil {
		t.Fatal("no second-pair report")
	}
	if res.Second.GivenAfter.TY != -201 || res.Second.SurnameAfter.TY != -248 {
		t.Fatalf("second pair slots not exchanged: %v %v", res.Second.GivenAfter, res.Second.SurnameAfter)
	}
	if res.Second.GivenAfter.TX != -595.5 || res.Second.SurnameAfter.TX != -478 {
		t.Fatalf("second pair horizontal positions %v %v", res.Second.GivenAfter, res.Second.SurnameAfter)
	}
	if res.ChangedBytes == 0 || res.ChangedBytes != res.PairResult.ChangedBytes+res.Second.ChangedBytes {
		t.Fatalf("changed bytes not attributed: total %d primary %d second %d", res.ChangedBytes, res.PairResult.ChangedBytes, res.Second.ChangedBytes)
	}
	primary := 0
	for _, off := range res.ChangedOffsets {
		switch {
		case inSlot(off, spec.S4Offset, 10, 17):
			primary++
		case inSlot(off, spec.S4Offset, 40, 47):
		default:
			t.Fatalf("changed offset %#x outside both pairs", off)
		}
	}
	if primary != res.ChangedBytes-res.Second.ChangedBytes {
		t.Fatalf("primary accounting %d vs %d", primary, res.ChangedBytes-res.Second.ChangedBytes)
	}
	if got := res.Describe(); !strings.Contains(got, "second pair") {
		t.Fatalf("Describe does not mention the second pair: %q", got)
	}
}

// TestApplyWholePairShift proves a pair shift moves both groups by the same
// constant on every keyframe, on top of the existing lower-line alignment, and
// that the relative alignment is preserved.
func TestApplyWholePairShift(t *testing.T) {
	base := synthPlatePairs([]synthPair{
		{10, 17, -190, 82, -38, 129},
		{40, 47, -630, -248, -478, -201},
	})
	spec := baseSpec(base)
	spec.Second = &PairSpec{
		GivenBlock:   40,
		SurnameBlock: 47,
		Keyframes:    7,
		GivenTXShift: 34.5,
		PairTXShift:  -228.5,
		Slot:         "progressed_plate",
	}
	res, err := Apply(spec, base)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.Second == nil {
		t.Fatal("no second-pair report")
	}
	// given: -630 + 34.5 - 228.5 = -824.0; surname: -478 - 228.5 = -706.5.
	if res.Second.GivenAfter.TX != -824.0 || res.Second.SurnameAfter.TX != -706.5 {
		t.Fatalf("second pair horizontal positions %v %v", res.Second.GivenAfter, res.Second.SurnameAfter)
	}
	if res.Second.PairTXShift != -228.5 {
		t.Fatalf("pair shift report %v", res.Second.PairTXShift)
	}
	// The primary (story) pair must be untouched by the second pair's shift.
	if res.GivenAfter.TX != -190 || res.SurnameAfter.TX != -38 {
		t.Fatalf("primary pair moved: %v %v", res.GivenAfter, res.SurnameAfter)
	}
	for i := 0; i < 7; i++ {
		goff := spec.S4Offset + (40+i)*S4Stride + S4TXOffset
		soff := spec.S4Offset + (47+i)*S4Stride + S4TXOffset
		if got := math.Float32frombits(binary.LittleEndian.Uint32(res.Logical[goff:])); got != -824.0 {
			t.Fatalf("given keyframe %d tx %v, want -824", i, got)
		}
		if got := math.Float32frombits(binary.LittleEndian.Uint32(res.Logical[soff:])); got != -706.5 {
			t.Fatalf("surname keyframe %d tx %v, want -706.5", i, got)
		}
	}
	// Relative alignment after both shifts is the same as with the given shift
	// alone: -630 + 34.5 = -595.5 against surname -478 (117.5 px apart), and the
	// block shift adds -228.5 to both.
	if got := res.Second.GivenAfter.TX - res.Second.SurnameAfter.TX; got != -117.5 {
		t.Fatalf("relative alignment changed: %v", got)
	}
}

// TestApplyRejectsStoryPairWholeShift proves the whole-block offset cannot leak
// into the story-introduction pair.
func TestApplyRejectsStoryPairWholeShift(t *testing.T) {
	base := synthPlate(10, 17, -190, 82, -38, 129)
	spec := baseSpec(base)
	spec.PairTXShift = -100
	if _, err := Apply(spec, base); err == nil || !strings.Contains(err.Error(), "story-introduction pair") {
		t.Fatalf("story whole-pair shift must be refused, got %v", err)
	}
}

func TestApplyRejectsOverlappingPairs(t *testing.T) {
	base := synthPlatePairs([]synthPair{
		{10, 17, -190, 82, -38, 129},
		{15, 22, -630, -248, -478, -201},
	})
	spec := baseSpec(base)
	spec.Second = &PairSpec{GivenBlock: 15, SurnameBlock: 22, Keyframes: 7}
	if _, err := Apply(spec, base); err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("overlapping pairs must be refused, got %v", err)
	}
}

func TestApplyRejectsShiftOutsideRange(t *testing.T) {
	for _, shift := range []float64{301, -301, math.NaN(), math.Inf(1)} {
		base := synthPlate(10, 17, -190, 82, -38, 129)
		spec := baseSpec(base)
		spec.GivenTXShift = shift
		if _, err := Apply(spec, base); err == nil {
			t.Fatalf("primary shift %v: expected an error", shift)
		}
		spec = baseSpec(base)
		spec.Second = &PairSpec{GivenBlock: 40, SurnameBlock: 47, Keyframes: 7, GivenTXShift: shift}
		if _, err := Apply(spec, synthPlatePairs([]synthPair{{10, 17, -190, 82, -38, 129}, {40, 47, -630, -248, -478, -201}})); err == nil {
			t.Fatalf("second-pair shift %v: expected an error", shift)
		}
	}
	for _, shift := range []float64{401, -401, math.NaN(), math.Inf(-1)} {
		base := synthPlatePairs([]synthPair{{10, 17, -190, 82, -38, 129}, {40, 47, -630, -248, -478, -201}})
		spec := baseSpec(base)
		spec.Second = &PairSpec{GivenBlock: 40, SurnameBlock: 47, Keyframes: 7, PairTXShift: shift}
		if _, err := Apply(spec, base); err == nil {
			t.Fatalf("whole-pair shift %v: expected an error", shift)
		}
	}
}

func TestApplyRejects(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Spec)
		why  string
	}{
		{"digest", func(s *Spec) { s.BaseSHA256 = "00" + s.BaseSHA256[2:] }, "digest"},
		{"size", func(s *Spec) { s.BaseSize++ }, "size"},
		{"s4", func(s *Spec) { s.S4Offset += 4 }, "S4 offset"},
		{"blocks", func(s *Spec) { s.SurnameBlock = 119 }, "outside"},
		{"notabove", func(s *Spec) { s.GivenBlock = 17; s.SurnameBlock = 10 }, "away"},
	}
	for _, c := range cases {
		base := synthPlate(10, 17, -190, 82, -38, 129)
		spec := baseSpec(base)
		c.mut(&spec)
		if _, err := Apply(spec, base); err == nil {
			t.Fatalf("%s: expected an error", c.name)
		}
	}
}

func TestApplyRejectsBrokenGroup(t *testing.T) {
	// A name group whose centre moves between keyframes cannot be exchanged as
	// one presentation slot.
	base := synthPlate(10, 17, -190, 82, -38, 129)
	off := HeaderSize + 0x100 + (17+3)*S4Stride + S4TYOffset
	binary.LittleEndian.PutUint32(base[off:], math.Float32bits(7.5))
	sum := sha256.Sum256(base)
	spec := baseSpec(base)
	spec.BaseSHA256 = hex.EncodeToString(sum[:])
	if _, err := Apply(spec, base); err == nil || !strings.Contains(err.Error(), "differs") {
		t.Fatalf("moving group must be refused, got %v", err)
	}

	// A settled keyframe that is not opaque is refused as well.
	base = synthPlate(10, 17, -190, 82, -38, 129)
	off = HeaderSize + 0x100 + (17+6)*S4Stride + 0x10
	binary.LittleEndian.PutUint32(base[off:], 0x00FFFFFF)
	sum = sha256.Sum256(base)
	spec = baseSpec(base)
	spec.BaseSHA256 = hex.EncodeToString(sum[:])
	if _, err := Apply(spec, base); err == nil || !strings.Contains(err.Error(), "alpha") {
		t.Fatalf("invisible settled keyframe must be refused, got %v", err)
	}
}

func TestDescribe(t *testing.T) {
	base := synthPlate(10, 17, -190, 82, -38, 129)
	res, err := Apply(baseSpec(base), base)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	want := "SURNAME above GIVEN"
	if got := res.Describe(); len(got) < len(want) || got[:len(want)] != want {
		t.Fatalf("Describe = %q", got)
	}
	spec := baseSpec(base)
	spec.GivenTXShift = 53
	res, err = Apply(spec, base)
	if err != nil {
		t.Fatalf("Apply with shift: %v", err)
	}
	if got := res.Describe(); !strings.Contains(got, "lower line +53 px") {
		t.Fatalf("Describe does not report the lower-line shift: %q", got)
	}
}
