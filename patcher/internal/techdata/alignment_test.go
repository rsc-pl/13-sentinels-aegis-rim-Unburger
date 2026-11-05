package techdata

import "testing"

// TestOrderPlateAlignmentSpec pins the per-character horizontal refinement of
// the lower given-name line. These values are the patch spec, not game data:
// each is the left-edge alignment of the given-name line with the surname line
// after the order swap, derived from the FMBS S3 sprite bounds (calibrated to
// the measured Fuyusaka runtime frame) and, for the progressed pair, from the
// captured progressed-flow frame. Changing a value is a deliberate composition
// decision and must update this table and its documented rationale.
func TestOrderPlateAlignmentSpec(t *testing.T) {
	type pair struct {
		givenBlock   int
		surnameBlock int
		shift        float64
		pairShift    float64
	}
	want := map[string]struct {
		story      pair
		progressed pair
	}{
		"GUI/change_kurabe.mbs":     {pair{27, 34, 95.0, 0.0}, pair{74, 81, 75.0, -285.0}},
		"GUI/change_fuyusaka.mbs":   {pair{34, 41, 53.0, 0.0}, pair{90, 97, 34.5, -228.5}}, // story value pinned reference
		"GUI/change_yakushiji.mbs":  {pair{27, 34, 0.0, 0.0}, pair{74, 81, 70.0, -216.5}},
		"GUI/change_sekigahara.mbs": {pair{27, 34, 33.0, 0.0}, pair{77, 84, 3.5, -199.0}},
		"GUI/change_gouto.mbs":      {pair{27, 34, 133.5, 0.0}, pair{77, 84, 143.5, -223.5}},
		"GUI/change_minami.mbs":     {pair{27, 34, 184.5, 0.0}, pair{76, 83, 154.5, -351.0}},
		"GUI/change_miura.mbs":      {pair{34, 41, 128.0, 0.0}, pair{90, 97, 128.0, -254.5}},
		"GUI/change_hijiyama.mbs":   {pair{34, 41, 140.5, 0.0}, pair{90, 97, 190.5, -351.5}},
		"GUI/change_ogata.mbs":      {pair{28, 35, 143.5, 0.0}, pair{78, 85, 83.5, -306.5}},
		"GUI/change_kisaragi.mbs":   {pair{27, 34, 95.0, 0.0}, pair{77, 84, 95.5, -252.0}},
		"GUI/change_amiguchi.mbs":   {pair{20, 27, 50.5, 0.0}, pair{64, 71, 50.5, 0.0}},
		"GUI/change_takamiya.mbs":   {pair{27, 34, 0.0, 0.0}, pair{76, 83, 42.0, -202.0}},
		"GUI/change_shinonome.mbs":  {pair{27, 34, 0.0, 0.0}, pair{75, 82, 0.0, -180.0}},
	}
	seen := 0
	for _, r := range OrderPlateResources() {
		exp, ok := want[r.Member]
		if !ok {
			t.Fatalf("unexpected order plate %s", r.Member)
		}
		if r.Transform == nil {
			t.Fatalf("%s: missing transform", r.Member)
		}
		tf := r.Transform
		if tf.GivenBlock != exp.story.givenBlock || tf.SurnameBlock != exp.story.surnameBlock {
			t.Fatalf("%s: story blocks %d/%d, want %d/%d", r.Member, tf.GivenBlock, tf.SurnameBlock, exp.story.givenBlock, exp.story.surnameBlock)
		}
		if tf.Keyframes != 7 {
			t.Fatalf("%s: story keyframes %d, want 7", r.Member, tf.Keyframes)
		}
		if got := tf.GivenTXShift; got != exp.story.shift {
			t.Fatalf("%s: story given_tx_shift %g, want %g", r.Member, got, exp.story.shift)
		}
		if tf.GivenTXBasis == "" {
			t.Fatalf("%s: missing story given_tx_basis rationale", r.Member)
		}
		if tf.PairTXShift != 0 {
			t.Fatalf("%s: story pair must not carry a whole-pair shift (%g)", r.Member, tf.PairTXShift)
		}
		if tf.Slot != "story_intro" {
			t.Fatalf("%s: story slot %q, want story_intro", r.Member, tf.Slot)
		}

		sp := tf.SecondPair
		if sp == nil {
			t.Fatalf("%s: missing progressed second_pair", r.Member)
		}
		if sp.GivenBlock != exp.progressed.givenBlock || sp.SurnameBlock != exp.progressed.surnameBlock {
			t.Fatalf("%s: progressed blocks %d/%d, want %d/%d", r.Member, sp.GivenBlock, sp.SurnameBlock, exp.progressed.givenBlock, exp.progressed.surnameBlock)
		}
		if sp.Keyframes != 7 {
			t.Fatalf("%s: progressed keyframes %d, want 7", r.Member, sp.Keyframes)
		}
		if got := sp.GivenTXShift; got != exp.progressed.shift {
			t.Fatalf("%s: progressed given_tx_shift %g, want %g", r.Member, got, exp.progressed.shift)
		}
		if got := sp.PairTXShift; got != exp.progressed.pairShift {
			t.Fatalf("%s: progressed pair_tx_shift %g, want %g", r.Member, got, exp.progressed.pairShift)
		}
		if sp.GivenTXBasis == "" {
			t.Fatalf("%s: missing progressed given_tx_basis rationale", r.Member)
		}
		if sp.PairTXShift != 0 && sp.PairTXBasis == "" {
			t.Fatalf("%s: missing progressed pair_tx_basis rationale", r.Member)
		}
		if sp.Slot != "progressed_plate" {
			t.Fatalf("%s: progressed slot %q, want progressed_plate", r.Member, sp.Slot)
		}
		// The two blocks must be disjoint: one edit may never touch the other.
		if !(tf.SurnameBlock+tf.Keyframes <= sp.GivenBlock || sp.SurnameBlock+sp.Keyframes <= tf.GivenBlock) {
			t.Fatalf("%s: story and progressed pairs overlap", r.Member)
		}
		seen++
	}
	if seen != len(want) || seen != 13 {
		t.Fatalf("order plate descriptors %d, want 13", seen)
	}
}

// TestOrderPlateZeroShiftRationale proves the zero-shift plates are explicit
// composition decisions, not missing data.
func TestOrderPlateZeroShiftRationale(t *testing.T) {
	storyZero := map[string]bool{
		"GUI/change_yakushiji.mbs": false,
		"GUI/change_takamiya.mbs":  false,
		"GUI/change_shinonome.mbs": false,
	}
	progressedZero := map[string]bool{
		"GUI/change_shinonome.mbs": false,
	}
	for _, r := range OrderPlateResources() {
		if r.Transform == nil {
			continue
		}
		if r.Transform.GivenTXShift == 0 {
			if _, ok := storyZero[r.Member]; !ok {
				t.Fatalf("%s has an unexpected story zero shift", r.Member)
			}
			storyZero[r.Member] = true
		}
		if r.Transform.SecondPair == nil {
			continue
		}
		if r.Transform.SecondPair.GivenTXShift == 0 {
			if _, ok := progressedZero[r.Member]; !ok {
				t.Fatalf("%s has an unexpected progressed zero shift", r.Member)
			}
			progressedZero[r.Member] = true
		}
	}
	for member, found := range storyZero {
		if !found {
			t.Fatalf("%s: expected a documented story zero shift", member)
		}
	}
	for member, found := range progressedZero {
		if !found {
			t.Fatalf("%s: expected a documented progressed zero shift", member)
		}
	}
}
