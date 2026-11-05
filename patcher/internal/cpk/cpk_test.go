package cpk_test

import (
	"testing"

	"13s-patcher/internal/cpk"
	"13s-patcher/internal/synth"
)

func sample() []synth.Member {
	return []synth.Member{
		{Dir: "Fms", Name: "A.fms", ID: 0, Logical: fmsbSample()},
		{Dir: "Fms", Name: "B.fms", ID: 1, Logical: fmsbSample()},
		{Dir: "z", Name: "c.bin", ID: 2, Logical: synth.Bytes(4000, 5), Compress: true},
	}
}

func fmsbSample() []byte { return synth.Bank([]string{"alpha", "beta @iis@/i gamma"}) }

func TestSynthParseNoop(t *testing.T) {
	raw, err := synth.Archive(sample())
	if err != nil {
		t.Fatal(err)
	}
	c, err := cpk.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Members) != 3 {
		t.Fatalf("members = %d", len(c.Members))
	}
	noop, err := c.Build(nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(noop) != string(raw) {
		t.Fatal("no-op rebuild is not byte-identical")
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	if _, err := cpk.Parse([]byte("not a cpk at all, definitely not")); err == nil {
		t.Fatal("garbage accepted")
	}
	if _, err := cpk.Parse(nil); err == nil {
		t.Fatal("nil accepted")
	}
}

func TestEditPreservesOtherMembers(t *testing.T) {
	raw, err := synth.Archive(sample())
	if err != nil {
		t.Fatal(err)
	}
	c, err := cpk.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	target := c.MemberByPath("Fms/A.fms")
	if target == nil {
		t.Fatal("member not found")
	}
	newLogical := synth.Bank([]string{"ALPHA", "beta @iis@/i gamma"})
	payload := newLogical // store raw (small)
	out, err := c.BuildLayout([]cpk.MemberEdit{{RowIndex: target.RowIndex, Payload: payload, ExtractSize: uint32(len(newLogical))}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	vc, err := cpk.Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	for i := range c.Members {
		old := c.Members[i]
		nm := vc.MemberByPath(string(old.Dir) + "/" + string(old.FileName))
		if nm == nil {
			t.Fatalf("lost %s", old.FileName)
		}
		if string(old.FileName) == string(target.FileName) {
			continue
		}
		if string(nm.Stored) != string(old.Stored) {
			t.Fatalf("unmodified member %s changed", old.FileName)
		}
	}
	got, err := vc.Logical(vc.MemberByPath("Fms/A.fms"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(newLogical) {
		t.Fatal("edited content mismatch")
	}
}

func TestBuildWithAddPreservesOffsets(t *testing.T) {
	raw, err := synth.Archive([]synth.Member{{Dir: "Fms", Name: "A.fms", ID: 0, Logical: fmsbSample()}})
	if err != nil {
		t.Fatal(err)
	}
	c, err := cpk.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	before := map[string]uint64{}
	for i := range c.Members {
		before[string(c.Members[i].FileName)] = c.Members[i].FileOffset
	}
	add := cpk.MemberAdd{Dir: "Fms", Name: "Z.fms", ID: 9, Stored: fmsbSample(), ExtractSize: uint32(len(fmsbSample()))}
	out, err := c.BuildWithAdd(nil, []cpk.MemberAdd{add})
	if err != nil {
		t.Fatal(err)
	}
	vc, err := cpk.Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(vc.Members) != 2 {
		t.Fatalf("members = %d, want 2", len(vc.Members))
	}
	if vc.MemberByPath("Fms/A.fms").FileOffset != before["A.fms"] {
		t.Fatal("existing offset not preserved")
	}
	if vc.MemberByPath("Fms/Z.fms") == nil {
		t.Fatal("added member missing")
	}
}

// TestBuildWithAddRawMember pins the RAW added-member storage shape that the
// title-black-screen diagnostic relies on: an added member stored with
// FileSize == ExtractSize (no CRILAYLA header) is accepted, re-parsed and
// returned byte-identical, with the whole container contract intact. It is the
// archive-level guard for a raw overlay shadow of the base GUI/title.ftx, which
// the base pack itself stores raw.
func TestBuildWithAddRawMember(t *testing.T) {
	raw, err := synth.Archive(sample())
	if err != nil {
		t.Fatal(err)
	}
	c, err := cpk.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	// larger than the 0x100 CRILAYLA minimum, and not a structured stream
	payload := make([]byte, 1<<20)
	for i := range payload {
		payload[i] = byte((i*131 + i/7) & 0xFF)
	}
	add := cpk.MemberAdd{Dir: "GUI", Name: "title.ftx", ID: 99, Stored: payload, ExtractSize: uint32(len(payload))}
	out, err := c.BuildWithAdd(nil, []cpk.MemberAdd{add})
	if err != nil {
		t.Fatal(err)
	}
	vc, err := cpk.Parse(out)
	if err != nil {
		t.Fatalf("a raw added member must re-parse: %v", err)
	}
	m := vc.MemberByPath("GUI/title.ftx")
	if m == nil {
		t.Fatal("the raw added member is missing")
	}
	if m.FileSize != uint32(len(payload)) || m.ExtractSize != uint32(len(payload)) {
		t.Fatalf("raw added member is %d/%d, want %d/%d (FileSize == ExtractSize)",
			m.FileSize, m.ExtractSize, len(payload), len(payload))
	}
	if string(m.Stored) != string(payload) {
		t.Fatal("raw added member stored bytes are not byte-identical")
	}
	got, err := vc.Logical(m)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatal("raw added member logical bytes are not byte-identical")
	}
	if len(m.Stored) >= 8 && string(m.Stored[:8]) == "CRILAYLA" {
		t.Fatal("raw added payload must not carry a CRILAYLA header")
	}
}
