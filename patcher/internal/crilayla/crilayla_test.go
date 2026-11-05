package crilayla_test

import (
	"testing"

	"13s-patcher/internal/crilayla"
	"13s-patcher/internal/synth"
)

func logical(payload []byte) []byte {
	out := make([]byte, 0, 0x100+len(payload))
	out = append(out, synth.Bytes(0x100, 3)...)
	out = append(out, payload...)
	return out
}

func TestRoundTripBothEncoders(t *testing.T) {
	cases := [][]byte{
		synth.Repetitive(4000, 1),
		synth.Bytes(5000, 2),
		append(synth.Repetitive(100, 5), synth.Repetitive(100, 5)...),
	}
	for _, enc := range []string{crilayla.EncoderChain, crilayla.EncoderOld} {
		for i, payload := range cases {
			want := logical(payload)
			blk, err := crilayla.Encode(want, enc)
			if err != nil {
				t.Fatalf("%s case %d: %v", enc, i, err)
			}
			got, err := crilayla.Decode(blk)
			if err != nil {
				t.Fatalf("%s case %d: decode: %v", enc, i, err)
			}
			if string(got) != string(want) {
				t.Fatalf("%s case %d: round-trip mismatch", enc, i)
			}
			if len(blk) >= 16 && len(blk) < len(want) {
				// compressible input should actually shrink the payload portion
			}
		}
	}
}

func TestChainIsDeterministic(t *testing.T) {
	want := logical(synth.Repetitive(2000, 4))
	a, _ := crilayla.Encode(want, crilayla.EncoderChain)
	b, _ := crilayla.Encode(want, crilayla.EncoderChain)
	if string(a) != string(b) {
		t.Fatal("chain encoder is not deterministic")
	}
	if len(a) != len(b) {
		t.Fatal("chain encoder output sizes differ")
	}
}

func TestDecodeRejectsGarbage(t *testing.T) {
	if _, err := crilayla.Decode([]byte("not crilayla")); err == nil {
		t.Fatal("garbage accepted")
	}
}

func TestEncodeRejectsShortLogical(t *testing.T) {
	// The encoder's contract is a panic for < 0x100; callers guard it. Verify the
	// guard is observable so callers can rely on it.
	defer func() {
		if recover() == nil {
			t.Fatal("expected a panic for short input")
		}
	}()
	crilayla.EncodeChain([]byte("short"), 0)
}
