package build_test

import (
	"context"
	"path/filepath"
	"testing"

	"13s-patcher/internal/build"
	"13s-patcher/internal/cpk"
	"13s-patcher/internal/fixture"
	"13s-patcher/internal/synth"
)

// largeLogical builds a deterministic member that is much larger than every
// member of the fixture archive and larger than the default TOC slack: it
// exercises the large-member path (compression, alignment, TOC/ETOC growth).
func largeLogical(n int) []byte {
	out := make([]byte, n)
	// a compressible but non-trivial pattern (so it stores compressed and the
	// member is a genuine test of the CRILAYLA path)
	for i := range out {
		out[i] = byte((i*7 + i/251) & 0xFF)
	}
	return out
}

// TestAddedLargeMemberInvariants adds a multi-megabyte member to a small archive
// and checks the whole container contract: 2048 alignment, header bookkeeping
// (ContentSize / EtocOffset / EnabledDataSize / EnabledPackedSize / Files /
// TocSize / EtocSize), the ETOC sentinel, byte-identity of every unmodified
// member, the added member's decoded bytes, and determinism. It is the
// regression guard for the large base-only GUI member (GUI/title.ftx) the title
// branding adds.
func TestAddedLargeMemberInvariants(t *testing.T) {
	f, err := fixture.Write(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	logical := largeLogical(5 << 20) // 5 MiB
	cfg := cfgFrom(f, filepath.Join(f.OutputDir, "large.CPK"))
	cfg.Resources = []build.Resource{{Member: "GUI/title.ftx", Logical: logical, Note: "test large member"}}

	res, err := build.Build(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Report.Verified || !res.Report.UnmodifiedIntact {
		t.Fatalf("report not verified: %+v", res.Report)
	}

	arc, err := cpk.Parse(res.Bytes)
	if err != nil {
		t.Fatalf("the built archive must re-parse: %v", err)
	}
	if len(arc.Members) != 5 {
		t.Fatalf("member count %d, want 5", len(arc.Members))
	}

	// container invariants
	hdr := func(name string) uint64 {
		i := arc.Header.ColIndex(name)
		if i < 0 {
			t.Fatalf("missing header column %s", name)
		}
		return arc.Header.GetU(0, i)
	}
	if got, want := hdr("ContentSize"), hdr("EtocOffset")-hdr("ContentOffset"); got != want {
		t.Fatalf("ContentSize %d != EtocOffset-ContentOffset %d", got, want)
	}
	if got := hdr("EtocOffset") % cpk.Align; got != 0 {
		t.Fatalf("EtocOffset %d is not %d-aligned", hdr("EtocOffset"), cpk.Align)
	}
	if got, want := hdr("Files"), uint64(len(arc.Members)); got != want {
		t.Fatalf("Files %d != member rows %d", got, want)
	}
	if got, want := hdr("TocSize"), uint64(len(arc.TOCPkt.Data))+16; got != want {
		t.Fatalf("TocSize %d != TOC packet length+16 %d", got, want)
	}
	if got, want := hdr("EtocSize"), uint64(len(arc.EtocPkt.Data))+16; got != want {
		t.Fatalf("EtocSize %d != ETOC packet length+16 %d", got, want)
	}
	if arc.ETOC.RowCount() != len(arc.Members)+1 {
		t.Fatalf("ETOC rows %d != members+1 %d", arc.ETOC.RowCount(), len(arc.Members)+1)
	}

	var sumF, sumE uint64
	for _, m := range arc.Members {
		if m.FileOffset%cpk.Align != 0 {
			t.Fatalf("member %s/%s offset %d is not %d-aligned", m.Dir, m.FileName, m.FileOffset, cpk.Align)
		}
		sumF += uint64(m.FileSize)
		sumE += uint64(m.ExtractSize)
	}
	if sumF != hdr("EnabledDataSize") {
		t.Fatalf("EnabledDataSize %d != sum(FileSize) %d", hdr("EnabledDataSize"), sumF)
	}
	if sumE != hdr("EnabledPackedSize") {
		t.Fatalf("EnabledPackedSize %d != sum(ExtractSize) %d", hdr("EnabledPackedSize"), sumE)
	}
	// TocOffset .. ContentOffset must still hold the TOC packet
	if hdr("ContentOffset")-hdr("TocOffset") < hdr("TocSize") {
		t.Fatalf("TOC (%d bytes) does not fit before ContentOffset", hdr("TocSize"))
	}

	// the added member must decode to exactly the logical bytes supplied
	added := arc.MemberByPath("GUI/title.ftx")
	if added == nil {
		t.Fatal("the added member is missing")
	}
	got, err := arc.Logical(added)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(logical) || string(got) != string(logical) {
		t.Fatalf("the added member decoded to %d bytes, want %d (identical=%v)", len(got), len(logical), string(got) == string(logical))
	}
	if added.FileSize == added.ExtractSize {
		t.Fatalf("the large member was expected to be stored compressed (FileSize %d == ExtractSize %d)", added.FileSize, added.ExtractSize)
	}

	// determinism: a second build must be byte-identical
	cfg2 := cfgFrom(f, filepath.Join(f.OutputDir, "large2.CPK"))
	cfg2.Resources = cfg.Resources
	res2, err := build.Build(context.Background(), cfg2)
	if err != nil {
		t.Fatal(err)
	}
	if res2.OutputSHA256 != res.OutputSHA256 {
		t.Fatalf("large-member build is not deterministic: %s vs %s", res.OutputSHA256, res2.OutputSHA256)
	}
}

// TestAddedMemberTocGrowthKeepsOrder adds several members at once and proves the
// TOC stays path-sorted (the archive contract) and that each new member lands
// where its lower-cased path key belongs.
func TestAddedMemberTocGrowthKeepsOrder(t *testing.T) {
	f, err := fixture.Write(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := cfgFrom(f, filepath.Join(f.OutputDir, "multi.CPK"))
	cfg.Resources = []build.Resource{
		{Member: "GUI/title.ftx", Logical: synth.Bytes(4096, 3)},
		{Member: "GUI/Aaa.ftx", Logical: synth.Bytes(1024, 4)},
		{Member: "Manual/Manual25.ftx", Logical: synth.Bytes(8192, 5)},
	}
	res, err := build.Build(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	arc, err := cpk.Parse(res.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(arc.Members) != 7 {
		t.Fatalf("member count %d, want 7", len(arc.Members))
	}
	prev := ""
	for i, m := range arc.Members {
		k := cpk.PathKey(string(m.Dir), string(m.FileName))
		if i > 0 && k < prev {
			t.Fatalf("TOC is not path-sorted at row %d: %q < %q", i, k, prev)
		}
		prev = k
	}
	for _, want := range []string{"GUI/Aaa.ftx", "GUI/title.ftx", "Manual/Manual25.ftx"} {
		m := arc.MemberByPath(want)
		if m == nil {
			t.Fatalf("added member %s is missing", want)
		}
		if m.ID < 4 {
			t.Fatalf("added member %s reused an existing ID %d", want, m.ID)
		}
	}
}

// TestAddedRawResourceStoredVerbatim proves a Resource with Raw=true is stored
// verbatim: FileSize == ExtractSize, the stored bytes are exactly the supplied
// logical bytes (no CRILAYLA wrapper), the same content without Raw is
// compressed, and the report records the storage shape. This is the archive
// contract for the title atlas, whose base member is stored raw.
func TestAddedRawResourceStoredVerbatim(t *testing.T) {
	f, err := fixture.Write(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	logical := largeLogical(5 << 20)
	rawCfg := cfgFrom(f, filepath.Join(f.OutputDir, "raw.CPK"))
	rawCfg.Resources = []build.Resource{{Member: "GUI/title.ftx", Logical: logical, Raw: true, Note: "raw test"}}
	rawRes, err := build.Build(context.Background(), rawCfg)
	if err != nil {
		t.Fatal(err)
	}
	if !rawRes.Report.Verified || !rawRes.Report.UnmodifiedIntact {
		t.Fatalf("raw build report not verified: %+v", rawRes.Report)
	}
	arc, err := cpk.Parse(rawRes.Bytes)
	if err != nil {
		t.Fatalf("the raw built archive must re-parse: %v", err)
	}
	added := arc.MemberByPath("GUI/title.ftx")
	if added == nil {
		t.Fatal("the raw added member is missing")
	}
	if added.FileSize != added.ExtractSize {
		t.Fatalf("raw member must have FileSize == ExtractSize, got %d / %d", added.FileSize, added.ExtractSize)
	}
	if int(added.FileSize) != len(logical) {
		t.Fatalf("raw member FileSize %d != %d", added.FileSize, len(logical))
	}
	if string(added.Stored) != string(logical) {
		t.Fatal("the raw member's stored bytes are not the supplied logical bytes")
	}
	if got, err := arc.Logical(added); err != nil || string(got) != string(logical) {
		t.Fatalf("the raw member must decode to the supplied bytes (err=%v equal=%v)", err, string(got) == string(logical))
	}
	var rawStorage bool
	for _, m := range rawRes.Report.Members {
		if m.Member == "GUI/title.ftx" && m.Storage == "raw" {
			rawStorage = true
		}
	}
	if !rawStorage {
		t.Fatalf("the report must record the raw storage shape: %+v", rawRes.Report.Members)
	}

	// The identical content without Raw must still be compressed (no hidden
	// change to the historical resource behaviour).
	compCfg := cfgFrom(f, filepath.Join(f.OutputDir, "comp.CPK"))
	compCfg.Resources = []build.Resource{{Member: "GUI/title.ftx", Logical: logical, Note: "compressed test"}}
	compRes, err := build.Build(context.Background(), compCfg)
	if err != nil {
		t.Fatal(err)
	}
	compArc, err := cpk.Parse(compRes.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	compAdded := compArc.MemberByPath("GUI/title.ftx")
	if compAdded == nil || compAdded.FileSize == compAdded.ExtractSize {
		t.Fatalf("a resource without Raw must remain compressed: %+v", compAdded)
	}
	// The two shapes must produce different archives: the raw one is larger.
	if rawRes.OutputSHA256 == compRes.OutputSHA256 {
		t.Fatal("raw and compressed resources must not produce the same archive")
	}

	// determinism: a second raw build must be byte-identical
	raw2 := cfgFrom(f, filepath.Join(f.OutputDir, "raw2.CPK"))
	raw2.Resources = rawCfg.Resources
	rawRes2, err := build.Build(context.Background(), raw2)
	if err != nil {
		t.Fatal(err)
	}
	if rawRes2.OutputSHA256 != rawRes.OutputSHA256 {
		t.Fatalf("raw-member build is not deterministic: %s vs %s", rawRes.OutputSHA256, rawRes2.OutputSHA256)
	}
}
