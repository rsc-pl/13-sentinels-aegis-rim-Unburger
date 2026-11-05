package fmsb_test

import (
	"strings"
	"testing"

	"13s-patcher/internal/fmsb"
	"13s-patcher/internal/synth"
)

func TestRoundTripJapaneseAndControls(t *testing.T) {
	items := []string{
		"十三機兵防衛圏",
		"鞍部十郎@i…@/i",
		"line1\nline2\ttabbed",
		"@#(1)選択@/b",
		"##(a)",
		strings.Repeat("あ", 300),
		"",
		"emoji 🎮 mixed 名前",
	}
	raw := synth.Bank(items)
	f, err := fmsb.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Strings) != len(items) {
		t.Fatalf("count %d != %d", len(f.Strings), len(items))
	}
	for i := range items {
		if string(f.Strings[i]) != items[i] {
			t.Fatalf("record %d mismatch", i)
		}
	}
	if string(f.Rebuild(f.Strings)) != string(raw) {
		t.Fatal("rebuild is not byte-identical")
	}
}

func TestEmptyBank(t *testing.T) {
	raw := synth.Bank(nil)
	f, err := fmsb.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Strings) != 0 {
		t.Fatalf("want 0, got %d", len(f.Strings))
	}
	if string(f.Rebuild(f.Strings)) != string(raw) {
		t.Fatal("empty bank rebuild not byte-stable")
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	if _, err := fmsb.Parse([]byte("FMSB short")); err == nil {
		t.Fatal("garbage accepted")
	}
}

func TestValidateControlsDetectsUnterminated(t *testing.T) {
	if err := fmsb.ValidateControls([]byte("@#(12 broken")); err == nil {
		t.Fatal("unterminated control token accepted")
	}
	if err := fmsb.ValidateControls([]byte("@#(12) ok ##(x) done")); err != nil {
		t.Fatalf("valid tokens rejected: %v", err)
	}
}
