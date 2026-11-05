package texpack

import (
	"encoding/binary"
	"strings"
	"testing"
)

// synthSFNT assembles a minimal sfnt container (a table directory plus the
// given tables) so the reader's fail-closed paths can be exercised without a
// real font file. 4-byte aligned.
func synthSFNT(flavour string, numGlyphs uint16, tables map[string][]byte) []byte {
	tags := make([]string, 0, len(tables))
	for tag := range tables {
		tags = append(tags, tag)
	}
	// deterministic order
	for i := 0; i < len(tags); i++ {
		for j := i + 1; j < len(tags); j++ {
			if tags[j] < tags[i] {
				tags[i], tags[j] = tags[j], tags[i]
			}
		}
	}
	dir := make([]byte, 12+16*len(tags))
	copy(dir[0:4], flavour)
	binary.BigEndian.PutUint16(dir[4:6], uint16(len(tags)))
	off := len(dir)
	body := []byte{}
	for i, tag := range tags {
		p := 12 + 16*i
		copy(dir[p:p+4], tag)
		data := tables[tag]
		binary.BigEndian.PutUint32(dir[p+8:p+12], uint32(off+len(body)))
		binary.BigEndian.PutUint32(dir[p+12:p+16], uint32(len(data)))
		body = append(body, data...)
		for len(body)%4 != 0 {
			body = append(body, 0)
		}
	}
	return append(dir, body...)
}

func headTable(unitsPerEm uint16, locFormat int16) []byte {
	h := make([]byte, 54)
	binary.BigEndian.PutUint32(h[0:4], 0x00010000)
	binary.BigEndian.PutUint16(h[18:20], unitsPerEm)
	binary.BigEndian.PutUint16(h[50:52], uint16(locFormat))
	return h
}

// TestParseFontRejectsUnsupportedFlavours proves the reader fails closed with an
// actionable message on everything it cannot draw, rather than guessing.
func TestParseFontRejectsUnsupportedFlavours(t *testing.T) {
	if _, err := parseFont([]byte("xx")); err == nil || !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("a truncated file must be refused as truncated, got %v", err)
	}

	// An OTTO face whose outlines live in CFF2 is named as such.
	cff2 := synthSFNT("OTTO", 1, map[string][]byte{
		"head": headTable(1000, 0),
		"maxp": {0, 0, 0x50, 0, 0, 1},
		"CFF2": make([]byte, 64),
	})
	if _, err := parseFont(cff2); err == nil || !strings.Contains(err.Error(), "CFF2") {
		t.Fatalf("a CFF2 face must be refused with a CFF2 message, got %v", err)
	}

	// An OTTO face with neither CFF table is refused.
	none := synthSFNT("OTTO", 1, map[string][]byte{
		"head": headTable(1000, 0),
		"maxp": {0, 0, 0x50, 0, 0, 1},
	})
	if _, err := parseFont(none); err == nil || !strings.Contains(err.Error(), "no CFF") {
		t.Fatalf("an OTTO face without CFF must be refused, got %v", err)
	}

	// An unknown sfnt flavour is refused.
	if _, err := parseFont(synthSFNT("wOFF", 1, map[string][]byte{"head": headTable(1000, 0)})); err == nil {
		t.Fatal("an unknown flavour must be refused")
	}

	// A TrueType flavour without glyf/loca is refused (not silently empty).
	tt := synthSFNT("\x00\x01\x00\x00", 1, map[string][]byte{
		"head": headTable(1000, 0),
		"maxp": {0, 0, 0x00, 0x01, 0, 1},
	})
	if _, err := parseFont(tt); err == nil {
		t.Fatal("a TrueType face without loca/glyf must be refused")
	}
}

// TestCFFIndexRejectsTruncation proves the CFF INDEX reader fails closed on a
// short table instead of reading out of bounds.
func TestCFFIndexRejectsTruncation(t *testing.T) {
	for _, b := range [][]byte{
		nil,
		{0x00},
		{0x00, 0x01},
		{0x00, 0x01, 0x04},
		{0x00, 0x01, 0x04, 0x00, 0x00, 0x00, 0x01, 0x00},
	} {
		if _, _, err := cffIndex(b, 0, len(b)); err == nil {
			t.Fatalf("a truncated INDEX %v must be refused", b)
		}
	}
}

// TestCFFCharstringRejectsUnknownOperator proves the Type 2 interpreter fails
// closed on an operator it does not implement.
func TestCFFCharstringRejectsUnknownOperator(t *testing.T) {
	r := &cffRun{cff: &cffFont{}}
	if err := r.charstring([]byte{32, 32, 2}, 0); err == nil {
		t.Fatal("an unsupported operator (2) must be refused")
	}
	// 12 45 is an unsupported escaped operator.
	r2 := &cffRun{cff: &cffFont{}}
	if err := r2.charstring([]byte{12, 45}, 0); err == nil {
		t.Fatal("an unsupported escaped operator must be refused")
	}
}
