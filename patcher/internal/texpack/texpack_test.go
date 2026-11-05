package texpack

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"testing"

	"13s-patcher/internal/techdata"
)

// gradient makes a deterministic synthetic background; no game data is used.
func gradient(w, h int) image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			o := img.PixOffset(x, y)
			img.Pix[o] = uint8((x * 255) / w)
			img.Pix[o+1] = uint8((y * 255) / h)
			img.Pix[o+2] = uint8(((x + y) * 255) / (w + h))
			img.Pix[o+3] = 255
		}
	}
	return img
}

func TestComposeManualPageShape(t *testing.T) {
	raw, err := techdata.OverlayImage()
	if err != nil {
		t.Fatal(err)
	}
	overlay, err := DecodeImage(raw)
	if err != nil {
		t.Fatal(err)
	}
	page, err := ComposeManualPage(gradient(2560, 1440), overlay)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != PageSize {
		t.Fatalf("page size %d, want %d", len(page), PageSize)
	}
	if string(page[:4]) != "FTEX" || string(page[0x80:0x84]) != "FTX0" {
		t.Fatal("container markers missing")
	}
	if got := binary.LittleEndian.Uint32(page[0x0C:0x10]); got != 1 {
		t.Fatalf("sub-texture count %d", got)
	}
	if got := binary.LittleEndian.Uint32(page[0xCC:0xD0]); got != PageWidth {
		t.Fatalf("width field %d", got)
	}
	if got := binary.LittleEndian.Uint32(page[0xD0:0xD4]); got != PageHeight {
		t.Fatalf("height field %d", got)
	}
	if got := binary.LittleEndian.Uint32(page[0xE0:0xE4]); got != 0x2D0000 {
		t.Fatalf("payload length %d", got)
	}
	if string(page[PageSize-16:PageSize-12]) != "FEOC" {
		t.Fatal("trailer missing")
	}
	// every block must decode as BC7 mode 6
	pix := page[0x1C0 : 0x1C0+0x2D0000]
	for i := 0; i+16 <= len(pix); i += 16 {
		m := 0
		for m < 8 && (pix[i]>>uint(m))&1 == 0 {
			m++
		}
		if m != 6 {
			t.Fatalf("block %d has BC7 mode %d, want 6", i/16, m)
		}
	}
}

func TestComposeDeterministic(t *testing.T) {
	raw, _ := techdata.OverlayImage()
	overlay, _ := DecodeImage(raw)
	a, err := ComposeManualPage(gradient(1920, 1080), overlay)
	if err != nil {
		t.Fatal(err)
	}
	b, err := ComposeManualPage(gradient(1920, 1080), overlay)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("composition is not deterministic")
	}
}

func TestSwizzleRoundTripShape(t *testing.T) {
	in := make([]byte, PageWidth*BufferH)
	for i := range in {
		in[i] = byte(i * 7)
	}
	out, err := swizzleBlocks(in, PageWidth, BufferH, swizzleMode)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != len(in) {
		t.Fatalf("swizzle changed the size: %d -> %d", len(in), len(out))
	}
}

// decodeMode6 decodes one BC7 mode-6 block, used to prove the encoder produces
// decodable blocks.
func decodeMode6(blk []byte) [16][4]uint8 {
	bit := func(i int) int { return int(blk[i/8]>>uint(i%8)) & 1 }
	get := func(pos, n int) int {
		v := 0
		for i := 0; i < n; i++ {
			v |= bit(pos+i) << uint(i)
		}
		return v
	}
	var ep [2][4]int
	p0, p1 := bit(63), bit(64)
	order := [8][2]int{{0, 2}, {1, 2}, {0, 1}, {1, 1}, {0, 0}, {1, 0}, {0, 3}, {1, 3}}
	for slot, oc := range order {
		ep[oc[0]][oc[1]] = get(7+7*slot, 7)
	}
	for e := 0; e < 2; e++ {
		pb := p0
		if e == 1 {
			pb = p1
		}
		for k := 0; k < 4; k++ {
			ep[e][k] = (ep[e][k] << 1) | pb
		}
	}
	idx := [16]int{get(65, 3)}
	for tt := 1; tt < 16; tt++ {
		idx[tt] = get(68+(tt-1)*4, 4)
	}
	var out [16][4]uint8
	for tt := 0; tt < 16; tt++ {
		w := weights4[idx[tt]]
		for k := 0; k < 4; k++ {
			v := ((64-w)*ep[0][k] + w*ep[1][k] + 32) >> 6
			out[tt][k] = uint8(clamp8(v))
		}
	}
	return out
}

func TestMode6BlocksDecodeBack(t *testing.T) {
	// a horizontal ramp: the single-weight mode-6 fit represents it closely
	rgba := make([]byte, 8*8*4)
	for i := 0; i < 4; i++ {
		for j := 0; j < 4; j++ {
			o := (i*8 + j) * 4
			v := uint8(20 + i*30)
			rgba[o], rgba[o+1], rgba[o+2], rgba[o+3] = v, v, v, 255
		}
	}
	blocks := encodeMode6(rgba, 8, 8)
	if len(blocks) != 4*16 {
		t.Fatalf("block bytes %d", len(blocks))
	}
	got := decodeMode6(blocks[:16])
	// the top-left texel must round-trip within a small tolerance
	if d := int(got[0][0]) - 20; d > 4 || d < -4 {
		t.Fatalf("r round-trip off by %d", d)
	}
	if d := int(got[15][0]) - 110; d > 4 || d < -4 {
		t.Fatalf("last texel round-trip off by %d", d)
	}
	for tt := 0; tt < 16; tt++ {
		if got[tt][3] != 255 {
			t.Fatalf("alpha %d not opaque", got[tt][3])
		}
	}
	_ = color.NRGBA{}
}
