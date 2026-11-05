package texpack

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image"
)

// The title-screen footer branding is a small text strip baked into one
// sub-texture of the game's title texture atlas (GUI/title.ftx). The atlas is
// game-owned artwork, so the patch never ships it: the caller supplies the
// atlas extracted from their own copy of the game, and this package rewrites
// only the 4x4 BC7 blocks that carry the footer text. Every other byte of the
// base (the logo, the menu words, the other five sub-textures) is preserved
// verbatim.

// TitleMember is the archive member the title-screen branding replaces.
const TitleMember = "GUI/title.ftx"

// TitleLogicalSize is the exact logical size of the base title texture. It is
// pinned so a build fails closed against an unexpected revision rather than
// rewriting the wrong region.
const TitleLogicalSize = 15730896

// TitleSubIndex is the sub-texture that carries the title UI/text atlas.
const TitleSubIndex = 4

// Geometry of the footer sub-texture and the block rectangle that carries the
// shipped copyright line. The rectangle was measured from the decoded atlas
// (Tegra block-linear mode 4): the shipped line's ink spans pixels x 0..591,
// y 850..880, including the two top rows of the capitals (y 850..851) and the
// descender of "rights" (x 357..380, y 874..880). The block rectangle therefore
// starts at block row 212 (y 848) and runs to block row 220 (y 883) so the
// whole shipped line is replaced, with no stray row of it left behind and room
// for the composed label's outline and descender. Every pixel the rectangle
// touches is either part of that line or empty in the shipped atlas, so no
// other artwork is affected.
const (
	titleSubWidth  = 1024
	titleSubHeight = 1024
	titleSubBytes  = titleSubWidth * titleSubHeight // 1 byte per pixel (BC7)

	titleFooterBlockX0 = 0
	titleFooterBlockY0 = 848 / 4
	titleFooterBlockX1 = 592 / 4
	titleFooterBlockY1 = (884 + 3) / 4
)

// ftxSub is one sub-texture of an FTX container.
type ftxSub struct {
	Index  int
	Block  int // offset of the "FTX0" sub-block header
	Phdr   int // sub-block header size (0x40)
	Psize  int // sub-block payload size
	Width  uint32
	Height uint32
	DStart uint32
	Dlen   uint32
	PixOff int // absolute offset of the pixel payload
}

// ftxFile is a parsed FTX container.
type ftxFile struct {
	Count   int
	HdrSize int
	Subs    []ftxSub
}

// parseFTX parses the FTX container header and every sub-texture header. It
// reads only as much as the header declares and fails closed on any structural
// inconsistency.
func parseFTX(raw []byte) (*ftxFile, error) {
	if len(raw) < 0x20 || string(raw[:4]) != "FTEX" {
		return nil, errors.New("texpack: not an FTX container")
	}
	hdrSize := int(binary.LittleEndian.Uint32(raw[8:12]))
	count := int(binary.LittleEndian.Uint32(raw[0x0C:0x10]))
	if count <= 0 || count > 4096 {
		return nil, fmt.Errorf("texpack: implausible sub-texture count %d", count)
	}
	if hdrSize < 0x20+count*48 || hdrSize > len(raw) {
		return nil, fmt.Errorf("texpack: header size %d is out of range", hdrSize)
	}
	f := &ftxFile{Count: count, HdrSize: hdrSize}
	pos := hdrSize
	for i := 0; i < count; i++ {
		if pos+0x40 > len(raw) || string(raw[pos:pos+4]) != "FTX0" {
			return nil, fmt.Errorf("texpack: sub-texture %d has no FTX0 block", i)
		}
		psize := int(binary.LittleEndian.Uint32(raw[pos+4 : pos+8]))
		phdr := int(binary.LittleEndian.Uint32(raw[pos+8 : pos+12]))
		if phdr < 0x40 || psize < 0 {
			return nil, fmt.Errorf("texpack: sub-texture %d declares an invalid block (%d/%d)", i, psize, phdr)
		}
		tex := pos + phdr
		if tex+0x24 > len(raw) || string(raw[tex:tex+4]) != ".tex" {
			return nil, fmt.Errorf("texpack: sub-texture %d has no .tex header", i)
		}
		s := ftxSub{
			Index: i, Block: pos, Phdr: phdr, Psize: psize,
			Width:  binary.LittleEndian.Uint32(raw[tex+0x0C : tex+0x10]),
			Height: binary.LittleEndian.Uint32(raw[tex+0x10 : tex+0x14]),
			DStart: binary.LittleEndian.Uint32(raw[tex+0x1C : tex+0x20]),
			Dlen:   binary.LittleEndian.Uint32(raw[tex+0x20 : tex+0x24]),
		}
		s.PixOff = tex + int(s.DStart)
		if s.PixOff < 0 || s.PixOff+int(s.Dlen) > len(raw) {
			return nil, fmt.Errorf("texpack: sub-texture %d pixel payload is out of range", i)
		}
		f.Subs = append(f.Subs, s)
		pos = pos + phdr + psize
	}
	return f, nil
}

// swizzledBlockOffset returns the byte offset, inside a swizzled block-linear
// payload, of the 16-byte block at raster block coordinates (bx, by). It is the
// inverse of the mapping swizzleBlocks applies.
func swizzledBlockOffset(width, height, mode, bx, by int) (int, error) {
	bw := width / blockPx
	bh := height / blockPx
	tileW := 64 / bytesPerBlock * blockPx
	tileH := 8 * blockPx * (1 << mode)
	tileBw := tileW / blockPx
	tileBh := tileH / blockPx
	if bx < 0 || by < 0 || bx >= bw || by >= bh {
		return 0, fmt.Errorf("texpack: block (%d,%d) is outside %dx%d", bx, by, width, height)
	}
	if width%tileW != 0 || height%tileH != 0 {
		return 0, fmt.Errorf("texpack: %dx%d is not tile aligned", width, height)
	}
	grid := intraTileGrid(mode)
	if grid.rows != tileBh || grid.cols != tileBw {
		return 0, errors.New("texpack: tile grid shape mismatch")
	}
	tpr := width / tileW
	n := 32 * (1 << mode)
	tr, r := by/tileBh, by%tileBh
	tc, c := bx/tileBw, bx%tileBw
	t := tr*tpr + tc
	u := grid.at(r, c)
	return (t*n + u) * bytesPerBlock, nil
}

// ComposeTitleFooter returns a copy of the user-derived title texture with the
// shipped copyright footer replaced by label, rendered with the supplied display
// face and style over a canvas whose default backing is fully transparent:
// white glyphs with a subtle dark outline, as the shipped line is a
// white-on-transparent coverage strip.
//
// fontBytes are the display face's bytes, read from disk by the caller on every
// build (never embedded, never cached here). style is the parsed footer style.
// A font that cannot be parsed, a style whose label cannot fit the footer
// rectangle, or a glyph-less label all fail closed. The five other sub-textures
// and every unaffected block are byte-identical to the input; the result has
// exactly the same length, so the rebuilt member keeps the base's geometry.
func ComposeTitleFooter(base []byte, label string, fontBytes []byte, style TitleFooterStyle) ([]byte, error) {
	if len(base) != TitleLogicalSize {
		return nil, fmt.Errorf("texpack: title base is %d bytes, want %d (the base must be the logical GUI/title.ftx member from your own game files)", len(base), TitleLogicalSize)
	}
	f, err := parseFTX(base)
	if err != nil {
		return nil, err
	}
	if len(f.Subs) <= TitleSubIndex {
		return nil, fmt.Errorf("texpack: title base carries only %d sub-texture(s)", len(f.Subs))
	}
	s := f.Subs[TitleSubIndex]
	if s.Width != titleSubWidth || s.Height != titleSubHeight || int(s.Dlen) != titleSubBytes {
		return nil, fmt.Errorf("texpack: title sub-texture %d is %dx%d/%d bytes, want %dx%d/%d",
			TitleSubIndex, s.Width, s.Height, s.Dlen, titleSubWidth, titleSubHeight, titleSubBytes)
	}
	if s.PixOff+int(s.Dlen) > len(base) {
		return nil, errors.New("texpack: title sub-texture payload is out of range")
	}
	if len(fontBytes) == 0 {
		return nil, errors.New("texpack: no display face supplied for the title footer")
	}
	face, err := parseFont(fontBytes)
	if err != nil {
		return nil, err
	}

	canvas, err := composeTitleCanvas(label, face, style)
	if err != nil {
		return nil, err
	}

	out := append([]byte(nil), base...)
	for by := titleFooterBlockY0; by < titleFooterBlockY1; by++ {
		for bx := titleFooterBlockX0; bx < titleFooterBlockX1; bx++ {
			off, err := swizzledBlockOffset(titleSubWidth, titleSubHeight, swizzleMode, bx, by)
			if err != nil {
				return nil, err
			}
			blk := encodeBlockMode6AlphaBest(canvas.Pix, titleSubWidth, bx, by)
			copy(out[s.PixOff+off:s.PixOff+off+bytesPerBlock], blk)
		}
	}
	// Fail closed: a bug in the off-screen geometry or the encoder must never
	// produce an atlas that changed a byte outside the footer block rectangle
	// (or wrote a non-BC7-mode-6 block). VerifyTitleFooter re-derives the whole
	// claim from the two byte slices, independently of the loop above.
	if err := VerifyTitleFooter(base, out); err != nil {
		return nil, err
	}
	return out, nil
}

// FooterBlockCount is the number of 16-byte BC7 blocks the footer rectangle
// covers. A composed atlas must change at most this many blocks.
const FooterBlockCount = (titleFooterBlockX1 - titleFooterBlockX0) * (titleFooterBlockY1 - titleFooterBlockY0)

// VerifyTitleFooter checks that composed is a faithful, footer-only edit of the
// base atlas. It is the fail-closed guard for the whole title composition:
//
//   - same length and the same FTX container structure (header + sub headers);
//   - every changed 16-byte block lies inside sub-texture TitleSubIndex's pixel
//     payload AND inside the footer block rectangle (offsets re-derived from the
//     block-linear map, not reused from the write loop);
//   - every changed block is a BC7 mode-6 block (the only mode the encoder may
//     emit);
//   - at least one block changed (a silently empty edit is refused);
//   - no byte outside sub-texture TitleSubIndex changed at all.
//
// It validates the bytes only: it says nothing about what the game renders.
func VerifyTitleFooter(base, composed []byte) error {
	if len(base) != TitleLogicalSize || len(composed) != TitleLogicalSize {
		return fmt.Errorf("texpack: title atlas size %d/%d, want %d", len(base), len(composed), TitleLogicalSize)
	}
	fb, err := parseFTX(base)
	if err != nil {
		return err
	}
	fc, err := parseFTX(composed)
	if err != nil {
		return err
	}
	if fb.HdrSize != fc.HdrSize || fb.Count != fc.Count || len(fb.Subs) != len(fc.Subs) {
		return errors.New("texpack: composed atlas changed the FTX container shape")
	}
	if string(base[:fb.HdrSize]) != string(composed[:fc.HdrSize]) {
		return errors.New("texpack: composed atlas changed the FTX header bytes")
	}
	for i := range fb.Subs {
		a, b := fb.Subs[i], fc.Subs[i]
		if a.Block != b.Block || a.Phdr != b.Phdr || a.Psize != b.Psize ||
			a.Width != b.Width || a.Height != b.Height || a.DStart != b.DStart || a.Dlen != b.Dlen || a.PixOff != b.PixOff {
			return fmt.Errorf("texpack: composed atlas changed sub-texture %d geometry", i)
		}
	}
	if len(fb.Subs) <= TitleSubIndex {
		return fmt.Errorf("texpack: title base carries only %d sub-texture(s)", len(fb.Subs))
	}
	s := fc.Subs[TitleSubIndex]

	allowed := map[int]bool{}
	for by := titleFooterBlockY0; by < titleFooterBlockY1; by++ {
		for bx := titleFooterBlockX0; bx < titleFooterBlockX1; bx++ {
			off, err := swizzledBlockOffset(titleSubWidth, titleSubHeight, swizzleMode, bx, by)
			if err != nil {
				return err
			}
			allowed[s.PixOff+off] = true
		}
	}

	changed := 0
	for i := 0; i+16 <= len(base); i += 16 {
		if string(base[i:i+16]) == string(composed[i:i+16]) {
			continue
		}
		changed++
		if i < s.PixOff || i+16 > s.PixOff+int(s.Dlen) {
			return fmt.Errorf("texpack: composed atlas changed a block at %d outside sub-texture %d", i, TitleSubIndex)
		}
		if !allowed[i] {
			return fmt.Errorf("texpack: composed atlas changed a block at %d outside the footer rectangle", i)
		}
		if composed[i]&0x7F != 0x40 {
			return fmt.Errorf("texpack: composed atlas wrote a non-modes-6 block at %d (first byte %#02x)", i, composed[i])
		}
	}
	// any residual byte difference (not covered by whole blocks) is fatal
	if len(base)%16 != 0 {
		return errors.New("texpack: title atlas is not a whole number of 16-byte blocks")
	}
	if changed == 0 {
		return errors.New("texpack: composed atlas changed no blocks")
	}
	if changed > FooterBlockCount {
		return fmt.Errorf("texpack: composed atlas changed %d blocks, more than the %d footer blocks", changed, FooterBlockCount)
	}
	return nil
}

// titleFooterRect is the pixel rectangle (raster coordinates) that the footer
// block rectangle covers.
func titleFooterRect() image.Rectangle {
	return image.Rect(
		titleFooterBlockX0*4, titleFooterBlockY0*4,
		titleFooterBlockX1*4, titleFooterBlockY1*4,
	)
}

// composeTitleCanvas renders label in the footer rectangle over a canvas whose
// default backing is fully transparent, so the title artwork shows through
// everywhere the label has no ink. The style controls the glyph ink colour and
// opacity, the outline colour/opacity/width, an optional backing (only when
// background_opacity is greater than zero), the em (0 = auto-fit) and the pixel
// offsets.
//
// A label that does not fit the footer rectangle at the chosen placement is an
// error naming the limit: it is never silently clipped.
//
// The result is deterministic for a given label, face and style.
func composeTitleCanvas(label string, face *displayFace, style TitleFooterStyle) (*image.NRGBA, error) {
	canvas := image.NewNRGBA(image.Rect(0, 0, titleSubWidth, titleSubHeight))
	mask := renderTitleLabel(label, face, style)
	if mask == nil {
		return nil, fmt.Errorf("texpack: label %q has no renderable glyphs", label)
	}
	rect := titleFooterRect()
	if style.BackgroundOpacity > 0 {
		bg := alphaByte(1, style.BackgroundOpacity)
		for y := rect.Min.Y; y < rect.Max.Y; y++ {
			for x := rect.Min.X; x < rect.Max.X; x++ {
				o := canvas.PixOffset(x, y)
				canvas.Pix[o], canvas.Pix[o+1], canvas.Pix[o+2], canvas.Pix[o+3] =
					style.BackgroundColor[0], style.BackgroundColor[1], style.BackgroundColor[2], bg
			}
		}
	}
	ox := rect.Min.X + (rect.Dx()-mask.W)/2 + style.OffsetX
	oy := rect.Min.Y + (rect.Dy()-mask.H)/2 + style.OffsetY
	if ox < rect.Min.X || oy < rect.Min.Y || ox+mask.W > rect.Max.X || oy+mask.H > rect.Max.Y {
		return nil, fmt.Errorf(
			"texpack: the footer label (rendered %dx%d px, offset %d,%d) does not fit the footer rectangle %v; reduce font_size/letter_spacing or the offsets",
			mask.W, mask.H, style.OffsetX, style.OffsetY, rect)
	}
	halo := dilateCoverage(mask, style.OutlineWidth)
	for y := 0; y < mask.H; y++ {
		py := oy + y
		if py < rect.Min.Y || py >= rect.Max.Y {
			continue
		}
		for x := 0; x < mask.W; x++ {
			px := ox + x
			if px < rect.Min.X || px >= rect.Max.X {
				continue
			}
			o := canvas.PixOffset(px, py)
			if ink := mask.Cov[y*mask.W+x]; ink > 0 {
				canvas.Pix[o], canvas.Pix[o+1], canvas.Pix[o+2], canvas.Pix[o+3] =
					style.TextColor[0], style.TextColor[1], style.TextColor[2], alphaByte(ink, style.TextOpacity)
				continue
			}
			if h := halo[y*mask.W+x]; h > 0 {
				a := alphaByte(h, style.OutlineOpacity)
				if a == 0 {
					continue
				}
				canvas.Pix[o], canvas.Pix[o+1], canvas.Pix[o+2], canvas.Pix[o+3] =
					style.OutlineColor[0], style.OutlineColor[1], style.OutlineColor[2], a
			}
		}
	}
	return canvas, nil
}

// alphaByte converts a coverage value in 0..1 and an opacity in 0..1 to an 8-bit
// alpha. The opacity is first quantised to a byte (round-half-up), then applied
// to the coverage, so opacity 1 is exactly the coverage byte and the default
// outline opacity 0.5 is exactly 128 -- the values the shipped footer used.
func alphaByte(coverage, opacity float64) uint8 {
	op := clamp8(int(opacity*255 + 0.5))
	return uint8(clamp8(int(coverage*float64(op) + 0.5)))
}

// TitleBaseCandidateNames lists the file names accepted for the user-derived
// title texture base, relative to a resource or search root.
func TitleBaseCandidateNames() []string {
	return []string{"GUI__title.ftx", "title.ftx", "title_base.ftx"}
}
