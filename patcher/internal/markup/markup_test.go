package markup_test

import (
	"testing"

	"13s-patcher/internal/markup"
)

func TestTokensAndEquivalence(t *testing.T) {
	orig := "@#(1)选择@/b and ##(a)"
	toks, err := markup.Tokens(orig)
	if err != nil {
		t.Fatal(err)
	}
	if len(toks) != 3 {
		t.Fatalf("tokens = %v", toks)
	}
	if ok, _ := markup.Equivalent(orig, "@#(1)選択@/b and ##(a)"); !ok {
		t.Fatal("text change that preserves tokens must be equivalent")
	}
	if ok, detail := markup.Equivalent(orig, "選択"); ok {
		t.Fatal("dropping tokens must not be equivalent")
	} else if detail == "" {
		t.Fatal("expected a detail message")
	}
	if ok, _ := markup.Equivalent("@ihello@/i", "@iworld@/i"); !ok {
		t.Fatal("emphasis inner text is opaque and may change")
	}
}

func TestTokensRejectsDangling(t *testing.T) {
	if _, err := markup.Tokens("hello @"); err == nil {
		t.Fatal("dangling @ accepted")
	}
	if _, err := markup.Tokens("@#(1"); err == nil {
		t.Fatal("unterminated token accepted")
	}
}
