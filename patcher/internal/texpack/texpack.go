// Package texpack builds the patch's added texture asset from patch-owned data
// and an image the user derives from their own game files.
//
// The container, swizzle and block encoder below follow the texture layout the
// game uses for manual pages:
//
//	FTEX | dataSize | headerSize 0x80 | count 1
//	entry0: name[32], meta[0]=0 meta[1]=payload meta[2]=0x1A0A07E5 meta[3]=id
//	0x80 "FTX0" | payloadSize | headerSize 0x40
//	0xC0 ".tex" | fmt 0x4D | mips 1 | width | height | 1 | 1 | dataStart 0x100 | dataLen
//	0x1C0 block payload, NSW mode-4 block-linear, BC7 mode 6
//	trailing "FEOC"
//
// Nothing here reads a game archive; the caller supplies the background image.
package texpack

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"math"

	_ "image/jpeg" // background inputs may be either format
	_ "image/png"
)

// Page geometry of an added manual page.
const (
	PageWidth     = 1920
	PageHeight    = 1080
	BufferH       = 1536
	swizzleMode   = 4
	bytesPerBlock = 16
	blockPx       = 4
)

// PageName is the texture entry name manual pages carry.
const PageName = "13Sentinels_Switch_Manual_EN_v04"

// PageMeta is the texture entry identifier manual pages carry.
const PageMeta uint32 = 0x042104

var weights4 = [16]int{0, 4, 9, 13, 17, 21, 26, 30, 34, 38, 43, 47, 51, 55, 60, 64}

// ---------------------------------------------------------------------------
// NSW block-linear permutation
// ---------------------------------------------------------------------------

type mat struct {
	rows, cols int
	d          []int
}

func (m mat) at(r, c int) int { return m.d[r*m.cols+c] }

func intraTileGrid(mode int) mat {
	n := 32 * (1 << mode)
	lst := make([]mat, n)
	for i := range lst {
		lst[i] = mat{1, 1, []int{i}}
	}
	concat := func(sec, axis int, in []mat) []mat {
		var out []mat
		for i := 0; i < len(in); i += sec {
			g := in[i : i+sec]
			if axis == 0 {
				rows := 0
				for _, m := range g {
					rows += m.rows
				}
				cols := g[0].cols
				d := make([]int, 0, rows*cols)
				for _, m := range g {
					d = append(d, m.d...)
				}
				out = append(out, mat{rows, cols, d})
			} else {
				rows := g[0].rows
				cols := 0
				for _, m := range g {
					cols += m.cols
				}
				d := make([]int, 0, rows*cols)
				for r := 0; r < rows; r++ {
					for _, m := range g {
						for c := 0; c < m.cols; c++ {
							d = append(d, m.at(r, c))
						}
					}
				}
				out = append(out, mat{rows, cols, d})
			}
		}
		return out
	}
	for _, op := range [][2]int{{2, 0}, {2, 1}, {4, 0}, {2, 1}, {1 << mode, 0}} {
		lst = concat(op[0], op[1], lst)
	}
	if len(lst) != 1 {
		panic("texpack: intra tile grid did not collapse")
	}
	return lst[0]
}

// swizzleBlocks maps raster-ordered 16-byte blocks to Tegra block-linear order.
func swizzleBlocks(linear []byte, width, height, mode int) ([]byte, error) {
	bw := width / blockPx
	bh := height / blockPx
	tileW := 64 / bytesPerBlock * blockPx
	tileH := 8 * blockPx * (1 << mode)
	tileBw := tileW / blockPx
	tileBh := tileH / blockPx
	if width%tileW != 0 || height%tileH != 0 {
		return nil, fmt.Errorf("texpack: %dx%d is not tile aligned", width, height)
	}
	grid := intraTileGrid(mode)
	if grid.rows != tileBh || grid.cols != tileBw {
		return nil, fmt.Errorf("texpack: tile grid shape mismatch")
	}
	tpr := width / tileW
	total := (bw / tileBw) * (bh / tileBh)
	out := make([]byte, len(linear))
	pos := 0
	words := make([][]byte, 32*(1<<mode))
	for t := 0; t < total; t++ {
		tr := t / tpr
		tc := t % tpr
		for r := 0; r < tileBh; r++ {
			for c := 0; c < tileBw; c++ {
				ob := tr*tileBh + r
				oc := tc*tileBw + c
				off := (ob*bw + oc) * bytesPerBlock
				words[grid.at(r, c)] = linear[off : off+bytesPerBlock]
			}
		}
		for u := 0; u < 32*(1<<mode); u++ {
			copy(out[pos:pos+bytesPerBlock], words[u])
			pos += bytesPerBlock
		}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// BC7 mode-6 block encoder (deterministic integer/float64 arithmetic)
// ---------------------------------------------------------------------------

func packMode6(c0, c1 [4]int, idx [16]int) []byte {
	buf := make([]int, 128)
	set := func(pos, val, n int) {
		for i := 0; i < n; i++ {
			buf[pos+i] = (val >> i) & 1
		}
	}
	buf[6] = 1
	order := [8][2]int{{0, 2}, {1, 2}, {0, 1}, {1, 1}, {0, 0}, {1, 0}, {0, 3}, {1, 3}}
	for slot, oc := range order {
		ep, comp := oc[0], oc[1]
		v := c0[comp]
		if ep == 1 {
			v = c1[comp]
		}
		set(7+7*slot, v>>1, 7)
	}
	buf[63] = c0[3] & 1
	buf[64] = c1[3] & 1
	for t := 0; t < 16; t++ {
		if t == 0 {
			set(65, idx[0], 3)
		} else {
			set(68+(t-1)*4, idx[t], 4)
		}
	}
	out := make([]byte, 16)
	for i := 0; i < 128; i++ {
		if buf[i] != 0 {
			out[i/8] |= 1 << uint(i%8)
		}
	}
	return out
}

func clamp8(v int) int {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return v
}

// encodeMode6 encodes a raster-ordered RGBA buffer into BC7 mode-6 blocks.
func encodeMode6(rgba []byte, width, height int) []byte {
	if width%blockPx != 0 || height%blockPx != 0 {
		panic("texpack: image is not block aligned")
	}
	bpr := width / 4
	nblocks := bpr * (height / 4)
	out := make([]byte, 0, nblocks*16)
	for bl := 0; bl < nblocks; bl++ {
		bx := bl % bpr
		by := bl / bpr
		out = append(out, encodeBlockMode6(rgba, width, bx, by)...)
	}
	return out
}

// encodeBlockMode6 encodes exactly one 4x4 block at raster block coordinates
// (bx, by) from a raster-ordered RGBA buffer of the given row width.
func encodeBlockMode6(rgba []byte, width, bx, by int) []byte {
	var p [16][4]int
	for t := 0; t < 16; t++ {
		r := t / 4
		c := t % 4
		o := ((by*4+r)*width + bx*4 + c) * 4
		p[t] = [4]int{int(rgba[o]), int(rgba[o+1]), int(rgba[o+2]), int(rgba[o+3])}
	}
	return encodeMode6Block(p)
}

// encodeBlockMode6Best is encodeBlockMode6 with both endpoint orders fitted.
// It is used by the title footer composition, where the historical single order
// leaves a hole at the first texel of mixed black/white blocks.
func encodeBlockMode6Best(rgba []byte, width, bx, by int) []byte {
	p := blockTexels(rgba, width, bx, by)
	return encodeMode6BlockBest(p)
}

// encodeBlockMode6AlphaBest is the footer's encoder: like encodeMode6BlockBest
// but the fit preserves the source's alpha channel. The title atlas is a
// white-on-transparent coverage texture, so an alpha-blind fit (which pins the
// alpha endpoints to 255) would paint an opaque slab where the artwork is
// meant to be transparent. The manual-page encoder keeps encodeMode6BlockBest,
// whose opaque endpoints and bytes are unchanged.
func encodeBlockMode6AlphaBest(rgba []byte, width, bx, by int) []byte {
	return encodeMode6BlockAlphaBest(blockTexels(rgba, width, bx, by))
}

// encodeMode6BlockAlphaBest is the block-level form of the footer encoder: both
// endpoint orders are fitted and the alpha channel is preserved.
func encodeMode6BlockAlphaBest(p [16][4]int) []byte {
	f := fitMode6BlockA(p, false, true)
	if s := fitMode6BlockA(p, true, true); s.sse < f.sse {
		return packMode6(s.c0, s.c1, s.idx)
	}
	return packMode6(f.c0, f.c1, f.idx)
}

// blockTexels reads the 4x4 texels of one block from a raster-ordered RGBA
// buffer of the given row width.
func blockTexels(rgba []byte, width, bx, by int) [16][4]int {
	var p [16][4]int
	for t := 0; t < 16; t++ {
		r := t / 4
		c := t % 4
		o := ((by*4+r)*width + bx*4 + c) * 4
		p[t] = [4]int{int(rgba[o]), int(rgba[o+1]), int(rgba[o+2]), int(rgba[o+3])}
	}
	return p
}

// encodeMode6Block fits one 4x4 RGBA block to BC7 mode 6 with the historical
// min->c0 / max->c1 endpoint order. Mode 6 stores the first texel's index in
// three bits, so under this order the texel at position 0 of a mixed block can
// never take the maximum endpoint (index 7 of 15 is only ~47% of the way to c1).
// The added manual pages keep this order so their bytes stay stable; the title
// footer composition uses encodeMode6BlockBest, which removes the artefact.
func encodeMode6Block(p [16][4]int) []byte {
	f := fitMode6Block(p, false)
	return packMode6(f.c0, f.c1, f.idx)
}

// encodeMode6BlockBest fits both endpoint orders and keeps the one with the
// lower measured error. For a block with black background and white ink the
// swapped order lets an ink texel at position 0 decode to white instead of grey.
// On an exact tie the historical order is kept, so a block the order choice
// cannot improve stays byte-identical to encodeMode6Block.
func encodeMode6BlockBest(p [16][4]int) []byte {
	f := fitMode6Block(p, false)
	if s := fitMode6Block(p, true); s.sse < f.sse {
		return packMode6(s.c0, s.c1, s.idx)
	}
	return packMode6(f.c0, f.c1, f.idx)
}

// mode6Fit is one fitted BC7 mode-6 block under a fixed endpoint order.
type mode6Fit struct {
	c0, c1 [4]int
	idx    [16]int
	sse    int
}

// sseMode6 is the total squared error of the decoded block against the source,
// evaluated with the same interpolation the format uses. c0/c1 carry the
// already-quantised endpoint values (p-bit applied), so it measures what a
// decoder actually produces.
func sseMode6(p [16][4]int, c0, c1 [4]int, idx [16]int) int {
	s := 0
	for t := 0; t < 16; t++ {
		ww := weights4[idx[t]]
		for k := 0; k < 4; k++ {
			v := ((64-ww)*c0[k] + ww*c1[k] + 32) >> 6
			d := v - p[t][k]
			s += d * d
		}
	}
	return s
}

// bestMode6Indices assigns, for a given endpoint pair, the per-texel index that
// minimises the block error. Position 0 is limited to the three bits the format
// reserves for it.
func bestMode6Indices(p [16][4]int, c0, c1 [4]int) [16]int {
	var idx [16]int
	for t := 0; t < 16; t++ {
		best, bestw := 1<<30, 0
		for wi := 0; wi < 16; wi++ {
			if t == 0 && wi > 7 {
				continue
			}
			ww := weights4[wi]
			e := 0
			for k := 0; k < 4; k++ {
				v := ((64-ww)*c0[k] + ww*c1[k] + 32) >> 6
				d := v - p[t][k]
				e += d * d
			}
			if e < best {
				best, bestw = e, wi
			}
		}
		idx[t] = bestw
	}
	return idx
}

// quantMode6Endpoint quantises one BC7 mode-6 endpoint to the wire format: a
// 7-bit value per channel plus the endpoint's single parity (p) bit. The
// parity is shared by all four channels, so the choice is made over the whole
// endpoint.
//
// With alphaAware=false this is exactly the historical rule: odd parity only,
// so a 255 channel stays 255, and the alpha channel is pinned to 255 whatever
// the source says. With alphaAware=true both parities are tried and the alpha
// channel is kept, so a fully transparent source endpoint stays 0 and a fully
// opaque white one stays 255.
func quantMode6Endpoint(c [4]int, alphaAware bool) [4]int {
	first := 1
	if alphaAware {
		first = 0
	}
	bestE := 1 << 30
	var best [4]int
	for par := first; par <= 1; par++ {
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
			if k == 3 && !alphaAware {
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

// fitMode6Block fits a block under one endpoint order with the opaque
// endpoints the manual pages use. swapped=false keeps the historical
// min->c0/max->c1 order; swapped=true seeds the fit with the endpoints
// exchanged.
func fitMode6Block(p [16][4]int, swapped bool) mode6Fit {
	return fitMode6BlockA(p, swapped, false)
}

// fitMode6BlockA is fitMode6Block with a switch for the alpha channel.
// alphaAware=false reproduces the historical fit exactly: the endpoint parity
// is fixed to odd and the alpha endpoints are pinned to 255, so an added manual
// page keeps its bytes. alphaAware=true lets the fit choose each endpoint's
// parity and keeps the alpha endpoints where the source puts them, which is
// what the title footer needs over its transparent artwork.
func fitMode6BlockA(p [16][4]int, swapped, alphaAware bool) mode6Fit {
	var lo, hi [4]int
	for k := 0; k < 4; k++ {
		mn, mx := 255, 0
		for t := 0; t < 16; t++ {
			v := p[t][k]
			if v < mn {
				mn = v
			}
			if v > mx {
				mx = v
			}
		}
		lo[k], hi[k] = mn, mx
	}
	c0, c1 := lo, hi
	if swapped {
		c0, c1 = hi, lo
	}

	quant := func(c [4]int) [4]int { return quantMode6Endpoint(c, alphaAware) }

	bestSSE := -1
	var best mode6Fit
	for iter := 0; iter < 8; iter++ {
		idx := bestMode6Indices(p, c0, c1)
		if s := sseMode6(p, c0, c1, idx); bestSSE < 0 || s < bestSSE {
			bestSSE = s
			best = mode6Fit{c0: c0, c1: c1, idx: idx, sse: s}
		}
		var sAA, sAB, sBB float64
		var sAT, sBT [4]float64
		for t := 0; t < 16; t++ {
			a := float64(64 - weights4[idx[t]])
			b := float64(weights4[idx[t]])
			sAA += a * a
			sAB += a * b
			sBB += b * b
			for k := 0; k < 4; k++ {
				tt := float64(64*p[t][k] - 32)
				sAT[k] += a * tt
				sBT[k] += b * tt
			}
		}
		det := sAA*sBB - sAB*sAB
		if det <= 1e-6*(sAA*sBB) || det <= 0 {
			continue
		}
		for k := 0; k < 4; k++ {
			nc0 := int((sBB*sAT[k]-sAB*sBT[k])/det + 0.5)
			nc1 := int((sAA*sBT[k]-sAB*sAT[k])/det + 0.5)
			if nc0 < lo[k] {
				nc0 = lo[k]
			}
			if nc0 > hi[k] {
				nc0 = hi[k]
			}
			if nc1 < lo[k] {
				nc1 = lo[k]
			}
			if nc1 > hi[k] {
				nc1 = hi[k]
			}
			c0[k], c1[k] = clamp8(nc0), clamp8(nc1)
		}
		c0, c1 = quant(c0), quant(c1)
	}
	if bestSSE < 0 {
		// Defensive: only reached if every iteration was degenerate, in which
		// case the initial endpoints are the best available.
		idx := bestMode6Indices(p, c0, c1)
		best = mode6Fit{c0: c0, c1: c1, idx: idx, sse: sseMode6(p, c0, c1, idx)}
	}
	return best
}

// ---------------------------------------------------------------------------
// container
// ---------------------------------------------------------------------------

func put32(b []byte, o int, v uint32) {
	b[o] = byte(v)
	b[o+1] = byte(v >> 8)
	b[o+2] = byte(v >> 16)
	b[o+3] = byte(v >> 24)
}

// buildFTXManualPage wraps a swizzled block payload in the manual-page
// container. The payload must be exactly PageWidth*BufferH bytes.
func buildFTXManualPage(pix []byte, name string, meta3 uint32) ([]byte, error) {
	const (
		dataSize = 0x2D0140
		hdrSize  = 0x80
		psize    = 0x2D0100
		dlen     = 0x2D0000
		texAt    = 0xC0
		pixAt    = texAt + 0x100
	)
	if len(pix) != dlen {
		return nil, fmt.Errorf("texpack: payload is %d bytes, want %d", len(pix), dlen)
	}
	out := make([]byte, pixAt+dlen+16)
	copy(out[0:4], "FTEX")
	put32(out, 4, dataSize)
	put32(out, 8, hdrSize)
	put32(out, 0x0C, 1)
	nb := []byte(name)
	if len(nb) > 32 {
		nb = nb[:32]
	}
	copy(out[0x20:0x40], nb)
	put32(out, 0x40, 0)
	put32(out, 0x44, psize)
	put32(out, 0x48, 0x1A0A07E5)
	put32(out, 0x4C, meta3)
	copy(out[0x80:0x84], "FTX0")
	put32(out, 0x84, psize)
	put32(out, 0x88, 0x40)
	put32(out, texAt+0x00, 0x7865742E)
	put32(out, texAt+0x04, 0x4D)
	put32(out, texAt+0x08, 1)
	put32(out, texAt+0x0C, PageWidth)
	put32(out, texAt+0x10, PageHeight)
	put32(out, texAt+0x14, 1)
	put32(out, texAt+0x18, 1)
	put32(out, texAt+0x1C, 0x100)
	put32(out, texAt+0x20, dlen)
	copy(out[pixAt:pixAt+dlen], pix)
	copy(out[pixAt+dlen:pixAt+dlen+4], "FEOC")
	put32(out, pixAt+dlen+4, 0)
	put32(out, pixAt+dlen+8, 0x10)
	put32(out, pixAt+dlen+12, 0)
	return out, nil
}

// PageSize is the exact logical size of a built manual page.
const PageSize = 448 + 0x2D0000 + 16

// ---------------------------------------------------------------------------
// composition
// ---------------------------------------------------------------------------

// ComposeManualPage builds one added manual page: the background (the project's
// shipped presentation plate, or an explicit user override) is cover-fitted to
// the page, toned, vignetted, the patch-owned overlay is composited on top, the
// buffer is padded to the tile height, encoded and swizzled.
//
// The result is deterministic for identical inputs. It is the patch's own
// composition; it is not a copy of any shipped asset.
func ComposeManualPage(base image.Image, overlay image.Image) ([]byte, error) {
	if base == nil {
		return nil, fmt.Errorf("texpack: no background image supplied")
	}
	if overlay == nil {
		return nil, fmt.Errorf("texpack: no overlay image supplied")
	}
	bg := coverFit(base, PageWidth, PageHeight)
	vignetteTone(bg, 0.5, 0.90, 0.88)
	if ob := overlay.Bounds(); ob.Dx() != PageWidth || ob.Dy() != PageHeight {
		return nil, fmt.Errorf("texpack: overlay is %dx%d, want %dx%d", ob.Dx(), ob.Dy(), PageWidth, PageHeight)
	}
	composite(bg, overlay)

	buf := make([]byte, PageWidth*BufferH*4)
	for y := 0; y < BufferH; y++ {
		sy := y
		if sy >= PageHeight {
			sy = PageHeight - 1
		}
		copy(buf[y*PageWidth*4:(y+1)*PageWidth*4], bg.Pix[sy*bg.Stride:sy*bg.Stride+PageWidth*4])
	}
	blocks := encodeMode6(buf, PageWidth, BufferH)
	sw, err := swizzleBlocks(blocks, PageWidth, BufferH, swizzleMode)
	if err != nil {
		return nil, err
	}
	return buildFTXManualPage(sw, PageName, PageMeta)
}

// DecodeImage decodes a PNG or JPEG background image.
func DecodeImage(raw []byte) (image.Image, error) {
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("texpack: cannot decode the image: %w", err)
	}
	return img, nil
}

// coverFit scales src to cover w x h and centre-crops it.
func coverFit(src image.Image, w, h int) *image.NRGBA {
	b := src.Bounds()
	r := math.Max(float64(w)/float64(b.Dx()), float64(h)/float64(b.Dy()))
	sw := int(float64(b.Dx())*r + 0.5)
	sh := int(float64(b.Dy())*r + 0.5)
	scaled := resizeBilinear(src, sw, sh)
	x := (sw - w) / 2
	y := (sh - h) / 2
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	for row := 0; row < h; row++ {
		for col := 0; col < w; col++ {
			si := scaled.PixOffset(x+col, y+row)
			di := out.PixOffset(col, row)
			copy(out.Pix[di:di+4], scaled.Pix[si:si+4])
			out.Pix[di+3] = 255
		}
	}
	return out
}

// resizeBilinear resamples src to w x h with bilinear interpolation.
func resizeBilinear(src image.Image, w, h int) *image.NRGBA {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	if w == sw && h == sh {
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				r, g, bb, a := src.At(b.Min.X+x, b.Min.Y+y).RGBA()
				o := out.PixOffset(x, y)
				out.Pix[o], out.Pix[o+1], out.Pix[o+2], out.Pix[o+3] = byte(r>>8), byte(g>>8), byte(bb>>8), byte(a>>8)
			}
		}
		return out
	}
	for y := 0; y < h; y++ {
		fy := (float64(y)+0.5)*float64(sh)/float64(h) - 0.5
		y0 := int(math.Floor(fy))
		ty := fy - float64(y0)
		if y0 < 0 {
			y0, ty = 0, 0
		}
		y1 := y0 + 1
		if y1 > sh-1 {
			y1 = sh - 1
		}
		for x := 0; x < w; x++ {
			fx := (float64(x)+0.5)*float64(sw)/float64(w) - 0.5
			x0 := int(math.Floor(fx))
			tx := fx - float64(x0)
			if x0 < 0 {
				x0, tx = 0, 0
			}
			x1 := x0 + 1
			if x1 > sw-1 {
				x1 = sw - 1
			}
			var acc [4]float64
			for k, wy := range []float64{1 - ty, ty} {
				yy := y0
				if k == 1 {
					yy = y1
				}
				for j, wx := range []float64{1 - tx, tx} {
					xx := x0
					if j == 1 {
						xx = x1
					}
					r, g, bb, a := src.At(b.Min.X+xx, b.Min.Y+yy).RGBA()
					wgt := wx * wy
					acc[0] += wgt * float64(r>>8)
					acc[1] += wgt * float64(g>>8)
					acc[2] += wgt * float64(bb>>8)
					acc[3] += wgt * float64(a>>8)
				}
			}
			o := out.PixOffset(x, y)
			for k := 0; k < 4; k++ {
				out.Pix[o+k] = uint8(clamp8(int(acc[k] + 0.5)))
			}
		}
	}
	return out
}

// vignetteTone applies a radial darkening followed by a brightness and a colour
// saturation adjustment, in place, on an opaque image.
func vignetteTone(img *image.NRGBA, strength, brightness, saturation float64) {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	cx, cy := float64(w)/2, float64(h)/2
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dx := (float64(x) - cx) / cx
			dy := (float64(y) - cy) / cy
			d := math.Sqrt(dx*dx + dy*dy)
			inner := math.Max(d-0.35, 0)
			if inner > 1.4 {
				inner = 1.4
			}
			m := 1 - strength*inner
			if m < 0 {
				m = 0
			}
			if m > 1 {
				m = 1
			}
			o := img.PixOffset(x, y)
			r := float64(img.Pix[o]) * m * brightness
			g := float64(img.Pix[o+1]) * m * brightness
			b := float64(img.Pix[o+2]) * m * brightness
			gray := 0.299*r + 0.587*g + 0.114*b
			r = gray + (r-gray)*saturation
			g = gray + (g-gray)*saturation
			b = gray + (b-gray)*saturation
			img.Pix[o] = uint8(clamp8(int(r + 0.5)))
			img.Pix[o+1] = uint8(clamp8(int(g + 0.5)))
			img.Pix[o+2] = uint8(clamp8(int(b + 0.5)))
			img.Pix[o+3] = 255
		}
	}
}

// composite alpha-blends overlay onto dst.
func composite(dst *image.NRGBA, overlay image.Image) {
	w, h := dst.Bounds().Dx(), dst.Bounds().Dy()
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.NRGBAModel.Convert(overlay.At(x, y)).(color.NRGBA)
			if c.A == 0 {
				continue
			}
			o := dst.PixOffset(x, y)
			a := float64(c.A) / 255
			for k := 0; k < 3; k++ {
				src := float64([3]uint8{c.R, c.G, c.B}[k])
				dstPix := float64(dst.Pix[o+k])
				dst.Pix[o+k] = uint8(clamp8(int(dstPix*(1-a) + src*a + 0.5)))
			}
			dst.Pix[o+3] = 255
		}
	}
}
