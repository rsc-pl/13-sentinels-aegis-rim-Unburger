package texpack

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"13s-patcher/internal/fixture"
)

// testFontBytes reads the live display face from the module's assets directory.
// The tests exercise exactly the file the production path reads from disk; there
// is no embedded copy any more.
func testFontBytes(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "assets", "fonts", "unburger.otf"))
	if err != nil {
		t.Fatalf("the shipped display face must be readable: %v", err)
	}
	return b
}

// parseTestFace parses the shipped face for the layout tests.
func parseTestFace(t *testing.T) *displayFace {
	t.Helper()
	f, err := parseFont(testFontBytes(t))
	if err != nil {
		t.Fatalf("the shipped face must parse: %v", err)
	}
	return f
}

// composeShipped composes with the shipped face and the shipped
// title_footer.toml style (built-in defaults plus letter_spacing 0.2). This is
// the release composition; it is not the runtime-confirmed reference.
func composeShipped(t *testing.T, base []byte, label string) []byte {
	t.Helper()
	out, err := ComposeTitleFooter(base, label, testFontBytes(t), ShippedTitleFooterStyle())
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// --- footer geometry ---------------------------------------------------------

// TestTitleFooterGeometryPinned pins the footer block rectangle and the block
// budget. The rectangle is derived from the decoded atlas: the shipped
// copyright line occupies pixels x 0..591, y 850..880 (two top rows of
// capitals plus the descender of "rights"), so the block rectangle is rows
// 212..220 (y 848..883) by cols 0..147 (x 0..591). The pixels it adds around
// the line are empty in the shipped atlas.
func TestTitleFooterGeometryPinned(t *testing.T) {
	rect := titleFooterRect()
	if want := image.Rect(0, 848, 592, 884); rect != want {
		t.Fatalf("footer rectangle %v, want %v", rect, want)
	}
	if FooterBlockCount != 148*9 {
		t.Fatalf("footer block count %d, want %d", FooterBlockCount, 148*9)
	}
	if rect.Dx()%4 != 0 || rect.Dy()%4 != 0 {
		t.Fatalf("footer rectangle %v is not block aligned", rect)
	}
	if rect.Min.X < 0 || rect.Min.Y < 0 || rect.Max.X > titleSubWidth || rect.Max.Y > titleSubHeight {
		t.Fatalf("footer rectangle %v leaves the sub-texture", rect)
	}
	if rect.Dx()/4*(rect.Dy()/4) != FooterBlockCount {
		t.Fatal("footer rectangle and block count disagree")
	}
}

// --- composition shape -------------------------------------------------------

func TestComposeTitleFooterShape(t *testing.T) {
	base := fixture.SynthTitleFTX()
	if len(base) != TitleLogicalSize {
		t.Fatalf("synthetic base is %d bytes, want %d", len(base), TitleLogicalSize)
	}
	out := composeShipped(t, base, "Unburger Mod v0.1.0")
	if len(out) != len(base) {
		t.Fatalf("size changed: %d -> %d", len(base), len(out))
	}
	f, err := parseFTX(base)
	if err != nil {
		t.Fatal(err)
	}
	s := f.Subs[TitleSubIndex]
	rect := titleFooterRect()
	// every difference must be a whole 16-byte block inside the footer
	// rectangle; nothing else may change.
	diffs := 0
	for i := 0; i < len(base); i += 16 {
		if bytes.Equal(base[i:i+16], out[i:i+16]) {
			continue
		}
		diffs++
		if i%16 != 0 {
			t.Fatalf("change at %d is not block aligned", i)
		}
		if i < s.PixOff || i+16 > s.PixOff+int(s.Dlen) {
			t.Fatalf("change at %d is outside the title sub-texture", i)
		}
		allowed := false
		for by := titleFooterBlockY0; by < titleFooterBlockY1 && !allowed; by++ {
			for bx := titleFooterBlockX0; bx < titleFooterBlockX1; bx++ {
				off, err := swizzledBlockOffset(titleSubWidth, titleSubHeight, swizzleMode, bx, by)
				if err != nil {
					t.Fatal(err)
				}
				if s.PixOff+off == i {
					allowed = true
					break
				}
			}
		}
		if !allowed {
			t.Fatalf("change at %d is outside the footer rectangle %v", i, rect)
		}
	}
	if diffs > FooterBlockCount {
		t.Fatalf("changed %d blocks, want at most %d", diffs, FooterBlockCount)
	}
	if diffs == 0 {
		t.Fatal("composition changed nothing")
	}
	// determinism
	again := composeShipped(t, base, "Unburger Mod v0.1.0")
	if !bytes.Equal(out, again) {
		t.Fatal("composition is not deterministic")
	}
}

func TestComposeTitleFooterTracksVersion(t *testing.T) {
	base := fixture.SynthTitleFTX()
	a := composeShipped(t, base, "Unburger Mod v0.1.0")
	b := composeShipped(t, base, "Unburger Mod v0.2.0")
	if bytes.Equal(a, b) {
		t.Fatal("a different version label must change the composed texture")
	}
}

func TestComposeTitleFooterRejectsBadBase(t *testing.T) {
	font := testFontBytes(t)
	style := DefaultTitleFooterStyle()
	if _, err := ComposeTitleFooter([]byte("not an ftx"), "Unburger Mod v0.1.0", font, style); err == nil {
		t.Fatal("a non-FTX base must be refused")
	}
	base := fixture.SynthTitleFTX()
	// corrupt the sub-texture count so the geometry check fails closed
	base = append(base[:0x0C], append([]byte{1}, base[0x0D:]...)...)
	if _, err := ComposeTitleFooter(base, "Unburger Mod v0.1.0", font, style); err == nil {
		t.Fatal("a wrong sub-texture count must be refused")
	}
}

// TestComposeTitleFooterRejectsUnrenderableLabel proves a label that draws no
// ink at all fails closed instead of silently erasing the shipped footer.
func TestComposeTitleFooterRejectsUnrenderableLabel(t *testing.T) {
	if _, err := ComposeTitleFooter(fixture.SynthTitleFTX(), "", testFontBytes(t), DefaultTitleFooterStyle()); err == nil {
		t.Fatal("an empty label must be refused")
	}
}

// --- the rendered canvas -----------------------------------------------------

func TestTitleLabelStaysInsideFooterRect(t *testing.T) {
	labels := []string{
		"Unburger Mod v0.1.0",
		"Unburger Mod v2026.10.18+hotfix",
		strings.Repeat("9", 64),
		"Mod",
	}
	rect := titleFooterRect()
	face := parseTestFace(t)
	for _, label := range labels {
		canvas, err := composeTitleCanvas(label, face, DefaultTitleFooterStyle())
		if err != nil {
			t.Fatalf("label %q: %v", label, err)
		}
		if got := canvas.Bounds(); got.Dx() != titleSubWidth || got.Dy() != titleSubHeight {
			t.Fatalf("label %q: canvas %v", label, got)
		}
		for y := 0; y < canvas.Bounds().Dy(); y++ {
			for x := 0; x < canvas.Bounds().Dx(); x++ {
				if x >= rect.Min.X && x < rect.Max.X && y >= rect.Min.Y && y < rect.Max.Y {
					continue
				}
				o := canvas.PixOffset(x, y)
				if canvas.Pix[o] != 0 || canvas.Pix[o+1] != 0 || canvas.Pix[o+2] != 0 || canvas.Pix[o+3] != 0 {
					t.Fatalf("label %q drew outside the footer rectangle at (%d,%d)", label, x, y)
				}
			}
		}
	}
}

// TestTitleFooterCanvasIsTransparentNotASlab proves the composed footer is a
// white glyph pass with a partial dark outline over a fully transparent
// backing: the opaque black slab the earlier renderer produced is gone.
func TestTitleFooterCanvasIsTransparentNotASlab(t *testing.T) {
	canvas, err := composeTitleCanvas("Unburger Mod v0.1.0", parseTestFace(t), DefaultTitleFooterStyle())
	if err != nil {
		t.Fatal(err)
	}
	rect := titleFooterRect()
	var clear, ink, halo, opaqueBlack int
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			o := canvas.PixOffset(x, y)
			a := int(canvas.Pix[o+3])
			switch {
			case a == 0:
				clear++
			case a == 255 && canvas.Pix[o] == 255 && canvas.Pix[o+1] == 255 && canvas.Pix[o+2] == 255:
				ink++
			case a > 0 && canvas.Pix[o] == 0 && canvas.Pix[o+1] == 0 && canvas.Pix[o+2] == 0:
				halo++
			}
			if a == 255 && canvas.Pix[o] == 0 && canvas.Pix[o+1] == 0 && canvas.Pix[o+2] == 0 {
				opaqueBlack++
			}
		}
	}
	if clear*2 < rect.Dx()*rect.Dy() {
		t.Fatalf("only %d of %d footer pixels are fully transparent; the backing is still a slab", clear, rect.Dx()*rect.Dy())
	}
	if ink == 0 {
		t.Fatal("the label drew no opaque white ink")
	}
	if halo == 0 {
		t.Fatal("the label drew no dark outline")
	}
	if opaqueBlack != 0 {
		t.Fatalf("%d pixels are opaque black: the backing must never be opaque", opaqueBlack)
	}
}

// --- independent decode of the composed blocks -------------------------------

// decodeMode6Wire decodes one 16-byte BC7 mode-6 block to its 16 RGBA texels,
// reading the wire format directly. It is written from the format description,
// not by reusing the encoder, so it is an independent check of what the encoder
// actually wrote (endpoint parity, 7-bit endpoints, weights, index fields).
func decodeMode6Wire(blk []byte) ([16][4]uint8, bool) {
	var out [16][4]uint8
	if len(blk) != 16 || blk[0]&0x7F != 0x40 {
		return out, false
	}
	bit := func(i int) uint32 { return uint32(blk[i>>3]>>(uint(i)&7)) & 1 }
	// fields are stored least-significant-bit first at their bit position
	bits := func(pos, n int) uint32 {
		var v uint32
		for i := 0; i < n; i++ {
			v |= bit(pos+i) << uint(i)
		}
		return v
	}
	order := [8][2]int{{0, 2}, {1, 2}, {0, 1}, {1, 1}, {0, 0}, {1, 0}, {0, 3}, {1, 3}}
	var c [2][4]int
	for slot, oc := range order {
		c[oc[0]][oc[1]] = int(bits(7+7*slot, 7))
	}
	for k := 0; k < 4; k++ {
		c[0][k] = (c[0][k] << 1) | int(bit(63))
		c[1][k] = (c[1][k] << 1) | int(bit(64))
	}
	var idx [16]int
	idx[0] = int(bits(65, 3))
	for t := 1; t < 16; t++ {
		idx[t] = int(bits(68+(t-1)*4, 4))
	}
	for t := 0; t < 16; t++ {
		w := weights4[idx[t]]
		for k := 0; k < 4; k++ {
			v := ((64-w)*c[0][k] + w*c[1][k] + 32) >> 6
			if v < 0 {
				v = 0
			}
			if v > 255 {
				v = 255
			}
			out[t][k] = uint8(v)
		}
	}
	return out, true
}

// TestComposedFooterBlocksDecodeToTheCanvas decodes every rewritten block with
// the independent wire decoder and checks it against the canvas the renderer
// produced: transparent canvas texels must stay transparent (a bounded haze is
// tolerated where a 4x4 block mixes ink, outline and clear pixels, because the
// BC7 mode-6 index is shared by the colour and alpha channels), every ink texel
// must stay visible and bright, interior ink (all eight neighbours fully white)
// must stay solid at the same 80% bar the reference composition met, and nothing
// may decode as an opaque backing. It runs on the shipped face/style.
func TestComposedFooterBlocksDecodeToTheCanvas(t *testing.T) {
	base := fixture.SynthTitleFTX()
	label := "Unburger Mod v0.1.0"
	out := composeShipped(t, base, label)
	f, err := parseFTX(out)
	if err != nil {
		t.Fatal(err)
	}
	s := f.Subs[TitleSubIndex]

	want, err := composeTitleCanvas(label, parseTestFace(t), ShippedTitleFooterStyle())
	if err != nil {
		t.Fatal(err)
	}
	rect := titleFooterRect()

	const (
		inkRgbMin      = 200 // solid ink must stay bright white (never grey)
		inkAlphaMin    = 64  // any ink texel must stay visible even where it shares a mixed block
		inkSolidMinPct = 80  // percent of interior ink texels that must decode at >= inkRgbMin alpha
		hazeMax        = 96  // a clear texel may not decode as a visible backing
		hazeAllowed    = 3   // percent of clear texels allowed above hazeMax
	)
	// A white canvas texel is "interior" when all eight neighbours are also
	// fully opaque white. BC7 mode 6 shares one endpoint pair across a 4x4
	// block, so an ink texel in a block that also holds clear or antialiased
	// edge texels legitimately decodes below full alpha; only true interiors
	// must stay solid. This keeps the original 80% bar where the decoder can
	// meet it, instead of averaging interiors with mixed-block edges.
	isInterior := func(px, py int) bool {
		for dy := -1; dy <= 1; dy++ {
			for dx := -1; dx <= 1; dx++ {
				nx, ny := px+dx, py+dy
				if nx < 0 || ny < 0 || nx >= want.Bounds().Dx() || ny >= want.Bounds().Dy() {
					continue
				}
				no := want.PixOffset(nx, ny)
				if !(want.Pix[no] == 255 && want.Pix[no+1] == 255 && want.Pix[no+2] == 255 && want.Pix[no+3] == 255) {
					return false
				}
			}
		}
		return true
	}
	blocks, clear, hazy, opaque, ink, interior, interiorSolid := 0, 0, 0, 0, 0, 0, 0
	for by := titleFooterBlockY0; by < titleFooterBlockY1; by++ {
		for bx := titleFooterBlockX0; bx < titleFooterBlockX1; bx++ {
			off, err := swizzledBlockOffset(titleSubWidth, titleSubHeight, swizzleMode, bx, by)
			if err != nil {
				t.Fatal(err)
			}
			blk := out[s.PixOff+off : s.PixOff+off+bytesPerBlock]
			tex, ok := decodeMode6Wire(blk)
			if !ok {
				t.Fatalf("block (%d,%d) is not BC7 mode 6", bx, by)
			}
			blocks++
			for ty := 0; ty < 4; ty++ {
				for tx := 0; tx < 4; tx++ {
					px, py := bx*4+tx, by*4+ty
					o := want.PixOffset(px, py)
					wr, wg, wb, wa := want.Pix[o], want.Pix[o+1], want.Pix[o+2], want.Pix[o+3]
					got := tex[ty*4+tx]
					switch {
					case wa == 0:
						clear++
						if got[3] > hazeMax || got[0] > hazeMax {
							hazy++
						}
					case wr == 255 && wg == 255 && wb == 255 && wa == 255:
						ink++
						if got[3] < inkAlphaMin || got[0] < inkRgbMin {
							t.Fatalf("block (%d,%d) texel (%d,%d): white ink decoded %v, want bright white", bx, by, tx, ty, got)
						}
						if isInterior(px, py) {
							interior++
							if got[3] >= inkRgbMin {
								interiorSolid++
							}
						}
					}
					// No texel of the composed footer may decode as an opaque
					// black backing: the canvas never contains one.
					if got[3] == 255 && got[0] == 0 && got[1] == 0 && got[2] == 0 {
						opaque++
					}
				}
			}
		}
	}
	if blocks != FooterBlockCount {
		t.Fatalf("checked %d blocks, want %d", blocks, FooterBlockCount)
	}
	if clear == 0 || ink == 0 {
		t.Fatalf("no transparent (%d) or no ink (%d) texels in the footer", clear, ink)
	}
	if interior == 0 {
		t.Fatal("the shipped composition has no interior ink texels to check")
	}
	if opaque != 0 {
		t.Fatalf("%d texels decode as an opaque black backing", opaque)
	}
	if interiorSolid*100 < interior*inkSolidMinPct {
		t.Fatalf("only %d of %d interior ink texels decode at >= %d alpha; the glyph interior is washing out", interiorSolid, interior, inkRgbMin)
	}
	if hazy*100 > clear*hazeAllowed {
		t.Fatalf("%d of %d clear texels decode above %d (more than %d%%)", hazy, clear, hazeMax, hazeAllowed)
	}
	if rect.Dx()/4*rect.Dy()/4 != FooterBlockCount {
		t.Fatal("footer rectangle and block count disagree")
	}
}

// --- the encoder -------------------------------------------------------------

// TestEncodeMode6BlockFillsFirstIndexHole pins the corrected encoder: a mixed
// black/white block whose ink sits at texel 0 must decode to white. With the
// historical min->c0/max->c1 order the first index is three bits, so texel 0
// could only reach ~47% of white; encodeMode6BlockBest fits both endpoint orders
// and removes it, while the historical encodeMode6Block keeps the old shape.
func TestEncodeMode6BlockFillsFirstIndexHole(t *testing.T) {
	var p [16][4]int
	for i := range p {
		p[i] = [4]int{0, 0, 0, 255}
	}
	p[0] = [4]int{255, 255, 255, 255}
	tex, ok := decodeMode6Wire(encodeMode6BlockBest(p))
	if !ok {
		t.Fatal("encoded block is not BC7 mode 6")
	}
	if tex[0][0] < 248 {
		t.Fatalf("ink at texel 0 decoded to %d, want near white (the hole defect)", tex[0][0])
	}
	for i := 1; i < 16; i++ {
		if tex[i][0] > 8 {
			t.Fatalf("background texel %d decoded to %d, want near black", i, tex[i][0])
		}
	}
	// The two-order fit must never be worse than the historical single order.
	if got, old := fitMode6Block(p, true), fitMode6Block(p, false); got.sse > old.sse {
		t.Fatalf("swapped fit worse (%d) than the historical fit (%d)", got.sse, old.sse)
	}
	// The historical order must still exhibit the artefact (proving the test
	// discriminates rather than passing vacuously).
	if old := decodeMode6WireStrict(t, encodeMode6Block(p)); old[0][0] >= 248 {
		t.Fatalf("the historical order unexpectedly reached white at texel 0 (%d)", old[0][0])
	}
}

// decodeMode6WireStrict is decodeMode6Wire with a test failure on a bad block.
func decodeMode6WireStrict(t *testing.T, blk []byte) [16][4]uint8 {
	t.Helper()
	tex, ok := decodeMode6Wire(blk)
	if !ok {
		t.Fatal("block is not BC7 mode 6")
	}
	return tex
}

// historicalQuant is the alpha-blind endpoint quantiser exactly as the manual
// page encoder has always applied it, kept here so the refactor that added the
// alpha-aware path is checked against the historical behaviour rather than
// against itself.
func historicalQuant(c [4]int) [4]int {
	bestE := 1 << 30
	var best [4]int
	for par := 1; par < 2; par++ {
		var q [4]int
		e := 0
		for k := 0; k < 4; k++ {
			v7 := (c[k] - par + 1) / 2
			if v7 < 0 {
				v7 = 0
			}
			if v7 > 127 {
				v7 = 127
			}
			q[k] = 2*v7 + par
			if k == 3 {
				q[k] = 255
			}
			d := q[k] - c[k]
			e += d * d
		}
		if e < bestE {
			bestE, best = e, q
		}
	}
	return best
}

// TestOpaqueEndpointQuantiserUnchanged proves the manual-page path kept its
// exact endpoint behaviour, so the added manual page's bytes do not move.
func TestOpaqueEndpointQuantiserUnchanged(t *testing.T) {
	for r := 0; r < 256; r += 7 {
		for g := 0; g < 256; g += 13 {
			for b := 0; b < 256; b += 17 {
				for a := 0; a < 256; a += 29 {
					c := [4]int{r, g, b, a}
					if got, want := quantMode6Endpoint(c, false), historicalQuant(c); got != want {
						t.Fatalf("opaque quant of %v = %v, want %v", c, got, want)
					}
				}
			}
		}
	}
}

// TestOpaqueMode6BlockBytesPinned pins the exact bytes encodeMode6Block emits
// for a mixed block, so any accidental change to the manual-page encoder is
// caught here rather than in a rebuilt archive.
func TestOpaqueMode6BlockBytesPinned(t *testing.T) {
	var p [16][4]int
	for i := range p {
		p[i] = [4]int{0, 0, 0, 255}
	}
	p[5] = [4]int{255, 255, 255, 255}
	p[11] = [4]int{64, 64, 64, 255}
	// Pinned from the manual-page encoder that reproduced the recorded manual
	// pages; the manual pages must not move.
	const want = "40c01ff007fcffff0100f00000400000"
	got := hex.EncodeToString(encodeMode6Block(p))
	if got != want {
		t.Fatalf("encodeMode6Block bytes changed:\n got %s\nwant %s", got, want)
	}
}

// TestAlphaAwareEncoderKeepsTransparency proves the footer encoder carries the
// canvas alpha: a fully transparent background stays transparent and opaque
// white ink stays opaque white, and the endpoint quantiser keeps both extremes.
func TestAlphaAwareEncoderKeepsTransparency(t *testing.T) {
	var p [16][4]int
	for i := range p {
		p[i] = [4]int{0, 0, 0, 0}
	}
	p[10] = [4]int{255, 255, 255, 255}

	tex := decodeMode6WireStrict(t, encodeMode6BlockAlphaBest(p))
	if tex[10][3] < 248 || tex[10][0] < 248 {
		t.Fatalf("ink texel decoded %v, want opaque white", tex[10])
	}
	for i := range tex {
		if i == 10 {
			continue
		}
		if tex[i][3] > 8 {
			t.Fatalf("transparent texel %d decoded alpha %d, want 0", i, tex[i][3])
		}
	}

	// The endpoint quantiser is the switch: the opaque path pins the alpha
	// endpoint to 255 (and only tries odd parity), the alpha-aware path keeps
	// the source value.
	if got := quantMode6Endpoint([4]int{0, 0, 0, 0}, false); got[3] != 255 {
		t.Fatalf("the opaque quantiser must pin a transparent endpoint to 255, got %v", got)
	}
	if got := quantMode6Endpoint([4]int{0, 0, 0, 0}, true); got[3] != 0 {
		t.Fatalf("the alpha-aware quantiser must keep a transparent endpoint at 0, got %v", got)
	}
	if got := quantMode6Endpoint([4]int{255, 255, 255, 255}, true); got != [4]int{255, 255, 255, 255} {
		t.Fatalf("the alpha-aware quantiser must keep opaque white exact, got %v", got)
	}
}

// --- the bundled font --------------------------------------------------------

func TestBundledTitleFaceLoads(t *testing.T) {
	f, err := parseFont(testFontBytes(t))
	if err != nil {
		t.Fatalf("the shipped face must load from assets/fonts/unburger.otf: %v", err)
	}
	if f.unitsPerEm <= 0 || f.numGlyphs <= 0 || len(f.cmap) == 0 {
		t.Fatalf("bundled face has no usable metrics: upm=%d glyphs=%d cmap=%d", f.unitsPerEm, f.numGlyphs, len(f.cmap))
	}
	if f.cff == nil {
		t.Fatal("the bundled face must be parsed as an OpenType/CFF face")
	}
	for _, r := range []rune{'A', '0', '9'} {
		gid, ok := f.cmap[r]
		if !ok {
			t.Fatalf("the bundled face must map %q", r)
		}
		cont, err := f.glyphContours(gid)
		if err != nil || len(cont) == 0 {
			t.Fatalf("the bundled face must supply a drawable %q: %v (%d contours)", r, err, len(cont))
		}
	}
}

// TestTitleLabelRenderingReportsFontCoverage documents which runes the bundled
// face can supply. The bundled OpenType/CFF face covers the whole real label --
// every letter, the digits, the dot and the space -- so the footer is drawn with
// it alone. A rune outside its cmap switches the whole label to the patch's
// built-in bitmap face, and that substitution is reported, never silent.
func TestTitleLabelRenderingReportsFontCoverage(t *testing.T) {
	font := testFontBytes(t)
	for _, label := range []string{
		"Unburger Mod v0.1.0",
		"Unburger Mod v0.2.0",
		"Unburger Mod v2026.10.18+hotfix",
		strings.Repeat("9", 64),
	} {
		if face, fallback := TitleLabelRendering(font, label); face != "supplied font" || fallback != "" {
			t.Fatalf("the supplied face covers %q, want it alone, got %q/%q", label, face, fallback)
		}
	}
	face, fallback := TitleLabelRendering(font, "Unburger Mod v0.1.0 \u2603")
	if face != "built-in bitmap font" {
		t.Fatalf("a label with an uncovered rune must use the built-in face, got %q", face)
	}
	if !strings.Contains(fallback, "\u2603") {
		t.Fatalf("the runes the face lacks must be reported, got %q", fallback)
	}
}

// TestRuneSourcesUsesOneFaceForTheWholeLabel checks the face choice: never a
// mixture of the two designs.
func TestRuneSourcesUsesOneFaceForTheWholeLabel(t *testing.T) {
	f := parseTestFace(t)
	for _, label := range []string{"Mod", "U01.", "Unburger Mod v0.1.0"} {
		for _, s := range runeSources(label, f, float64(f.unitsPerEm)) {
			if s.bitmap {
				t.Fatalf("rune %q must use the bundled face", s.r)
			}
			if s.r != ' ' && (len(s.cont) == 0 || s.adv <= 0) {
				t.Fatalf("rune %q has no outline", s.r)
			}
		}
	}
	// one rune the bundled face lacks switches the whole label.
	for _, s := range runeSources("Mod\u2603", f, float64(f.unitsPerEm)) {
		if !s.bitmap {
			t.Fatalf("rune %q: a single missing glyph must switch the whole label", s.r)
		}
	}
}

// TestTitleLabelMaskCoverage checks the mask is a real coverage mask. The
// bundled OpenType/CFF face is rasterised with antialiased edges, so it must
// carry partial coverage; the built-in bitmap face is deliberately snapped to
// the pixel grid, so its coverage is crisp. Both must be deterministic.
func TestTitleLabelMaskCoverage(t *testing.T) {
	face := parseTestFace(t)
	style := DefaultTitleFooterStyle()
	bundled := renderTitleLabel("Unburger Mod v0.1.0", face, style)
	if bundled == nil {
		t.Fatal("no mask for a face-drawn label")
	}
	partial, full := 0, 0
	for _, v := range bundled.Cov {
		if v > 0 && v < 1 {
			partial++
		}
		if v >= 1 {
			full++
		}
	}
	if full == 0 {
		t.Fatal("mask has no fully covered pixels")
	}
	if partial == 0 {
		t.Fatal("the bundled face mask has no partial coverage: its glyph edges are not antialiased")
	}

	crisp := renderTitleLabel("Mod\u2603", face, style)
	if crisp == nil {
		t.Fatal("no mask for a bitmap-face label")
	}
	for i, v := range crisp.Cov {
		if v != 0 && v != 1 {
			t.Fatalf("bitmap-face coverage at %d is %v, want a crisp 0 or 1", i, v)
		}
	}

	again := renderTitleLabel("Mod\u2603", face, style)
	if again.W != crisp.W || again.H != crisp.H {
		t.Fatal("mask geometry is not deterministic")
	}
	for i := range crisp.Cov {
		if crisp.Cov[i] != again.Cov[i] {
			t.Fatalf("mask coverage differs at %d", i)
		}
	}
}

// TestComposedFooterBytesPinned pins the exact bytes the renderer and the
// encoder compose over the synthetic base for a fixed label, using the shipped
// face and the shipped title_footer.toml style. It is the deliberate change
// detector for the release composition: the value moves only when the face, the
// shipped style, the renderer, the alpha-aware encoder or the fixture changes,
// and it must then be re-derived on purpose. This pin is a release-identity
// value, not a runtime claim: the shipped face/style has not been run in the
// game, so the build reports the composition as runtime-unverified.
func TestComposedFooterBytesPinned(t *testing.T) {
	out := composeShipped(t, fixture.SynthTitleFTX(), "Unburger Mod v0.1.0")
	const want = "728ea7237005b33916094f649ac7793290e61f52b9ffe37cd91da7303d62fb89"
	if got := hashBytes(out); got != want {
		t.Fatalf("composed footer bytes changed:\n got %s\nwant %s", got, want)
	}
}

// hashBytes is a small local helper so the footer tests can pin whole-atlas
// digests without importing the service hash helper.
func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestVerifyTitleFooterRejectsOutOfRegionAndNonMode6(t *testing.T) {
	base := fixture.SynthTitleFTX()
	out := composeShipped(t, base, "Unburger Mod v0.1.0")
	if err := VerifyTitleFooter(base, out); err != nil {
		t.Fatalf("the composed atlas must verify: %v", err)
	}

	f, err := parseFTX(base)
	if err != nil {
		t.Fatal(err)
	}
	// a single flipped byte inside another sub-texture
	other := f.Subs[0]
	bad := append([]byte(nil), out...)
	bad[other.PixOff] ^= 0xFF
	if err := VerifyTitleFooter(base, bad); err == nil {
		t.Fatal("a change outside the title sub-texture must be refused")
	}

	// a changed block that is not BC7 mode 6
	off, err := swizzledBlockOffset(titleSubWidth, titleSubHeight, swizzleMode, 0, titleFooterBlockY0)
	if err != nil {
		t.Fatal(err)
	}
	s := f.Subs[TitleSubIndex]
	bad2 := append([]byte(nil), out...)
	bad2[s.PixOff+off] = 0x00 // clears the mode-6 signature
	if err := VerifyTitleFooter(base, bad2); err == nil {
		t.Fatal("a non-mode-6 rewritten block must be refused")
	}

	// a valid-mode block outside the footer rectangle (but inside sub-texture 4)
	bad3 := append([]byte(nil), out...)
	outside, err := swizzledBlockOffset(titleSubWidth, titleSubHeight, swizzleMode, 10, 100)
	if err != nil {
		t.Fatal(err)
	}
	bad3[s.PixOff+outside] = 0x7F
	if err := VerifyTitleFooter(base, bad3); err == nil {
		t.Fatal("a block outside the footer rectangle must be refused")
	}

	// no change at all
	if err := VerifyTitleFooter(out, out); err == nil {
		t.Fatal("an atlas with no changed block must be refused")
	}
}
