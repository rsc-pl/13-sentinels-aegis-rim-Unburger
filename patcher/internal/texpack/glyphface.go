package texpack

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// ---------------------------------------------------------------------------
// Live display face
// ---------------------------------------------------------------------------
//
// The footer label is drawn with the display face whose bytes the caller
// supplies. In production those bytes are read from disk on every build
// (assets/fonts/<font_file>), so replacing the .otf changes the next build
// without recompiling; there is no embedded copy and no cached face. The reader
// below is a deliberately small OpenType subset: the sfnt table directory,
// head/maxp/hhea/hmtx metrics, cmap formats 4/6/12, and outlines from either
// flavour -- TrueType (glyf) or CFF (Type 2 charstrings, see cff.go). Composite
// glyphs and the unsupported CFF variants (CID-keyed, CFF2) are refused; a rune
// the face cannot supply is drawn with the patch's own bitmap face instead --
// that substitution is reported to the caller (TitleLabelRendering), never
// applied silently.

// fontPoint is one point of a glyph outline, in font units (y grows up). CFF
// cubic curves are flattened to on-curve points, so every point the renderer
// sees lies on the outline (TrueType off-curve control points stay off-curve).
type fontPoint struct {
	X, Y float64
	On   bool
}

// displayFace is the parsed subset of an OpenType face. Exactly one outline set
// is populated: glyf for a TrueType flavour, cff for an OTTO/CFF flavour.
type displayFace struct {
	data        []byte
	numGlyphs   int
	unitsPerEm  int
	hmtxAt      int
	numHMetrics int
	cmap        map[rune]int

	// TrueType (glyf) outlines.
	locFormat int
	loca      []uint32
	glyfAt    int

	// CFF (Type 2 charstring) outlines.
	cff *cffFont
}

// tableAt looks a table up in the sfnt directory.
func (f *displayFace) tableAt(tag string) (off, length int, ok bool) {
	n := int(binary.BigEndian.Uint16(f.data[4:6]))
	for i := 0; i < n; i++ {
		p := 12 + 16*i
		if p+16 > len(f.data) {
			return 0, 0, false
		}
		if string(f.data[p:p+4]) != tag {
			continue
		}
		off = int(binary.BigEndian.Uint32(f.data[p+8 : p+12]))
		length = int(binary.BigEndian.Uint32(f.data[p+12 : p+16]))
		if off < 0 || length < 0 || off+length > len(f.data) {
			return 0, 0, false
		}
		return off, length, true
	}
	return 0, 0, false
}

// parseFont parses an OpenType face from the given bytes. It reads the metrics
// every face shares (head/maxp/hhea/hmtx/cmap) and then the outline set for the face's
// flavour: glyf for a TrueType flavour, CFF (Type 2 charstrings) for OTTO. It
// fails closed on anything it does not understand rather than guessing.
func parseFont(data []byte) (*displayFace, error) {
	if len(data) < 12 {
		return nil, errors.New("texpack: font file is truncated")
	}
	sfnt := binary.BigEndian.Uint32(data[0:4])
	flavour := string(data[0:4])
	n := int(binary.BigEndian.Uint16(data[4:6]))
	if n <= 0 || 12+16*n > len(data) {
		return nil, errors.New("texpack: font table directory is out of range")
	}
	f := &displayFace{data: data}

	head, headLen, ok := f.tableAt("head")
	if !ok || headLen < 54 {
		return nil, errors.New("texpack: font has no usable head table")
	}
	f.unitsPerEm = int(binary.BigEndian.Uint16(data[head+18 : head+20]))
	f.locFormat = int(int16(binary.BigEndian.Uint16(data[head+50 : head+52])))
	if f.unitsPerEm <= 0 {
		return nil, errors.New("texpack: font declares unitsPerEm 0")
	}

	maxp, maxpLen, ok := f.tableAt("maxp")
	if !ok || maxpLen < 6 {
		return nil, errors.New("texpack: font has no usable maxp table")
	}
	f.numGlyphs = int(binary.BigEndian.Uint16(data[maxp+4 : maxp+6]))
	if f.numGlyphs <= 0 {
		return nil, errors.New("texpack: font declares no glyphs")
	}

	if hhea, hlen, ok := f.tableAt("hhea"); ok && hlen >= 36 {
		f.numHMetrics = int(binary.BigEndian.Uint16(data[hhea+34 : hhea+36]))
	}
	f.hmtxAt, _, _ = f.tableAt("hmtx")

	switch {
	case sfnt == 0x00010000 || flavour == "true":
		if err := f.parseGlyf(); err != nil {
			return nil, err
		}
	case flavour == "OTTO":
		cffOff, cffLen, ok := f.tableAt("CFF ")
		if !ok {
			if _, _, ok2 := f.tableAt("CFF2"); ok2 {
				return nil, errors.New("texpack: this face has CFF2 outlines, which are not supported; supply a CFF or TrueType face")
			}
			return nil, errors.New("texpack: OTTO face has no CFF table")
		}
		cff, err := parseCFF(data, cffOff, cffLen)
		if err != nil {
			return nil, err
		}
		if len(cff.charStrings) != f.numGlyphs {
			return nil, fmt.Errorf("texpack: CFF has %d charstrings but maxp declares %d glyphs", len(cff.charStrings), f.numGlyphs)
		}
		f.cff = cff
	default:
		return nil, fmt.Errorf("texpack: unsupported font flavour %q (need TrueType glyf or CFF/OTTO outlines)", flavour)
	}

	m, err := f.parseCMap()
	if err != nil {
		return nil, err
	}
	f.cmap = m
	return f, nil
}

// parseGlyf reads the loca index of a TrueType (glyf) face.
func (f *displayFace) parseGlyf() error {
	data := f.data
	loca, locaLen, ok := f.tableAt("loca")
	if !ok {
		return errors.New("texpack: font has no loca table")
	}
	if f.glyfAt, _, ok = f.tableAt("glyf"); !ok {
		return errors.New("texpack: font has no glyf table (CFF outlines are not supported)")
	}
	need := f.numGlyphs + 1
	f.loca = make([]uint32, need)
	switch f.locFormat {
	case 0:
		if locaLen < need*2 {
			return errors.New("texpack: font loca table is truncated")
		}
		for i := 0; i < need; i++ {
			f.loca[i] = uint32(binary.BigEndian.Uint16(data[loca+2*i:loca+2*i+2])) * 2
		}
	case 1:
		if locaLen < need*4 {
			return errors.New("texpack: font loca table is truncated")
		}
		for i := 0; i < need; i++ {
			f.loca[i] = binary.BigEndian.Uint32(data[loca+4*i : loca+4*i+4])
		}
	default:
		return fmt.Errorf("texpack: unknown loca format %d", f.locFormat)
	}
	return nil
}

// advance returns the horizontal advance of a glyph in font units.
func (f *displayFace) advance(gid int) float64 {
	if f.hmtxAt == 0 || f.numHMetrics <= 0 {
		return float64(f.unitsPerEm) / 2
	}
	i := gid
	if i >= f.numHMetrics {
		i = f.numHMetrics - 1
	}
	if i < 0 {
		i = 0
	}
	o := f.hmtxAt + 4*i
	if o+2 > len(f.data) {
		return 0
	}
	return float64(binary.BigEndian.Uint16(f.data[o : o+2]))
}

// parseCMap reads the best supported character-map subtable.
func (f *displayFace) parseCMap() (map[rune]int, error) {
	at, length, ok := f.tableAt("cmap")
	if !ok || length < 4 {
		return nil, errors.New("texpack: font has no usable cmap table")
	}
	end := at + length
	n := int(binary.BigEndian.Uint16(f.data[at+2 : at+4]))
	type sub struct{ score, off, format int }
	best := sub{score: -1}
	for i := 0; i < n; i++ {
		p := at + 4 + 8*i
		if p+8 > end {
			break
		}
		platform := int(binary.BigEndian.Uint16(f.data[p : p+2]))
		encoding := int(binary.BigEndian.Uint16(f.data[p+2 : p+4]))
		off := int(binary.BigEndian.Uint32(f.data[p+4 : p+8]))
		if off < 0 || at+off+2 > end {
			continue
		}
		format := int(binary.BigEndian.Uint16(f.data[at+off : at+off+2]))
		score := 0
		switch {
		case platform == 3 && encoding == 10 && format == 12:
			score = 6
		case platform == 0 && format == 12:
			score = 5
		case platform == 3 && encoding == 1 && format == 4:
			score = 4
		case platform == 0 && format == 4:
			score = 3
		case platform == 1 && encoding == 0 && format == 6:
			score = 2
		case format == 6 || format == 4:
			score = 1
		}
		if score > best.score {
			best = sub{score: score, off: off, format: format}
		}
	}
	if best.score < 0 {
		return nil, errors.New("texpack: font cmap has no supported subtable")
	}
	out := map[rune]int{}
	base := at + best.off
	var err error
	switch best.format {
	case 4:
		err = f.cmapFormat4(base, end, out)
	case 6:
		err = f.cmapFormat6(base, end, out)
	case 12:
		err = f.cmapFormat12(base, end, out)
	default:
		err = fmt.Errorf("texpack: unsupported cmap format %d", best.format)
	}
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (f *displayFace) cmapFormat4(base, end int, out map[rune]int) error {
	if base+14 > end {
		return errors.New("texpack: cmap format 4 header is truncated")
	}
	segX2 := int(binary.BigEndian.Uint16(f.data[base+6 : base+8]))
	seg := segX2 / 2
	if seg <= 0 {
		return nil
	}
	endCodes := base + 14
	startCodes := endCodes + segX2 + 2
	deltas := startCodes + segX2
	ranges := deltas + segX2
	if ranges+segX2 > end {
		return errors.New("texpack: cmap format 4 arrays are truncated")
	}
	for i := 0; i < seg; i++ {
		ec := int(binary.BigEndian.Uint16(f.data[endCodes+2*i : endCodes+2*i+2]))
		sc := int(binary.BigEndian.Uint16(f.data[startCodes+2*i : startCodes+2*i+2]))
		delta := int(int16(binary.BigEndian.Uint16(f.data[deltas+2*i : deltas+2*i+2])))
		ro := int(binary.BigEndian.Uint16(f.data[ranges+2*i : ranges+2*i+2]))
		if sc > ec {
			continue
		}
		for c := sc; c <= ec && c < 0xFFFF; c++ {
			var gid int
			if ro == 0 {
				gid = (c + delta) & 0xFFFF
			} else {
				gp := ranges + 2*i + ro + 2*(c-sc)
				if gp+2 > end {
					continue
				}
				gid = int(binary.BigEndian.Uint16(f.data[gp : gp+2]))
				if gid != 0 {
					gid = (gid + delta) & 0xFFFF
				}
			}
			if gid > 0 && gid < f.numGlyphs {
				if _, ok := out[rune(c)]; !ok {
					out[rune(c)] = gid
				}
			}
		}
	}
	return nil
}

func (f *displayFace) cmapFormat6(base, end int, out map[rune]int) error {
	if base+10 > end {
		return errors.New("texpack: cmap format 6 header is truncated")
	}
	first := int(binary.BigEndian.Uint16(f.data[base+6 : base+8]))
	count := int(binary.BigEndian.Uint16(f.data[base+8 : base+10]))
	p := base + 10
	if p+2*count > end {
		return errors.New("texpack: cmap format 6 array is truncated")
	}
	for i := 0; i < count; i++ {
		gid := int(binary.BigEndian.Uint16(f.data[p+2*i : p+2*i+2]))
		if gid > 0 && gid < f.numGlyphs {
			out[rune(first+i)] = gid
		}
	}
	return nil
}

func (f *displayFace) cmapFormat12(base, end int, out map[rune]int) error {
	if base+16 > end {
		return errors.New("texpack: cmap format 12 header is truncated")
	}
	groups := int(binary.BigEndian.Uint32(f.data[base+12 : base+16]))
	p := base + 16
	if groups < 0 || p+12*groups > end {
		return errors.New("texpack: cmap format 12 groups are truncated")
	}
	for i := 0; i < groups; i++ {
		start := binary.BigEndian.Uint32(f.data[p+12*i : p+12*i+4])
		stop := binary.BigEndian.Uint32(f.data[p+12*i+4 : p+12*i+8])
		gid := int(binary.BigEndian.Uint32(f.data[p+12*i+8 : p+12*i+12]))
		if stop < start {
			continue
		}
		for c := start; c <= stop; c++ {
			g := gid + int(c-start)
			if g > 0 && g < f.numGlyphs {
				if _, ok := out[rune(c)]; !ok {
					out[rune(c)] = g
				}
			}
		}
	}
	return nil
}

// glyphContours returns the outlines of one glyph. An empty slice means the
// glyph has no ink (for example a space). An error means the glyph cannot be
// drawn with this reader (a composite, say).
func (f *displayFace) glyphContours(gid int) ([][]fontPoint, error) {
	if gid < 0 || gid >= f.numGlyphs {
		return nil, fmt.Errorf("texpack: glyph %d is out of range", gid)
	}
	if f.cff != nil {
		return f.cff.glyphContours(gid)
	}
	start, stop := f.loca[gid], f.loca[gid+1]
	if start >= stop {
		return nil, nil
	}
	length := int(stop - start)
	base := f.glyfAt + int(start)
	if base < 0 || length < 10 || base+length > len(f.data) {
		return nil, errors.New("texpack: glyph data is out of range")
	}
	nc := int(int16(binary.BigEndian.Uint16(f.data[base : base+2])))
	if nc < 0 {
		return nil, fmt.Errorf("texpack: composite glyph %d is not supported", gid)
	}
	if nc == 0 {
		return nil, nil
	}
	if nc > 4096 {
		return nil, errors.New("texpack: implausible glyph contour count")
	}
	lim := base + length
	p := base + 10
	ends := make([]int, nc)
	for i := 0; i < nc; i++ {
		if p+2 > lim {
			return nil, errors.New("texpack: glyph contour table is truncated")
		}
		ends[i] = int(binary.BigEndian.Uint16(f.data[p : p+2]))
		p += 2
		if i > 0 && ends[i] < ends[i-1] {
			return nil, errors.New("texpack: glyph contour endpoints are not monotonic")
		}
	}
	nPts := ends[nc-1] + 1
	if nPts <= 0 || nPts > 1<<16 {
		return nil, errors.New("texpack: implausible glyph point count")
	}
	if p+2 > lim {
		return nil, errors.New("texpack: glyph instruction length is missing")
	}
	ins := int(binary.BigEndian.Uint16(f.data[p : p+2]))
	p += 2 + ins
	if p > lim {
		return nil, errors.New("texpack: glyph instructions run past the glyph")
	}
	flags := make([]byte, nPts)
	for i := 0; i < nPts; {
		if p >= lim {
			return nil, errors.New("texpack: glyph flags are truncated")
		}
		fl := f.data[p]
		p++
		flags[i] = fl
		i++
		if fl&8 != 0 {
			if p >= lim {
				return nil, errors.New("texpack: glyph flag repeat is truncated")
			}
			rep := int(f.data[p])
			p++
			for j := 0; j < rep && i < nPts; j++ {
				flags[i] = fl
				i++
			}
		}
	}
	xs := make([]int, nPts)
	x := 0
	for i := 0; i < nPts; i++ {
		fl := flags[i]
		switch {
		case fl&2 != 0:
			if p >= lim {
				return nil, errors.New("texpack: glyph x deltas are truncated")
			}
			d := int(f.data[p])
			p++
			if fl&16 == 0 {
				d = -d
			}
			x += d
		case fl&16 == 0:
			if p+2 > lim {
				return nil, errors.New("texpack: glyph x deltas are truncated")
			}
			x += int(int16(binary.BigEndian.Uint16(f.data[p : p+2])))
			p += 2
		}
		xs[i] = x
	}
	ys := make([]int, nPts)
	y := 0
	for i := 0; i < nPts; i++ {
		fl := flags[i]
		switch {
		case fl&4 != 0:
			if p >= lim {
				return nil, errors.New("texpack: glyph y deltas are truncated")
			}
			d := int(f.data[p])
			p++
			if fl&32 == 0 {
				d = -d
			}
			y += d
		case fl&32 == 0:
			if p+2 > lim {
				return nil, errors.New("texpack: glyph y deltas are truncated")
			}
			y += int(int16(binary.BigEndian.Uint16(f.data[p : p+2])))
			p += 2
		}
		ys[i] = y
	}

	var out [][]fontPoint
	from := 0
	for _, e := range ends {
		if e < from || e >= nPts {
			return nil, errors.New("texpack: glyph contour endpoints are malformed")
		}
		pts := make([]fontPoint, 0, e-from+1)
		for i := from; i <= e; i++ {
			pts = append(pts, fontPoint{X: float64(xs[i]), Y: float64(ys[i]), On: flags[i]&1 != 0})
		}
		out = append(out, pts)
		from = e + 1
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Label layout and rasterisation
// ---------------------------------------------------------------------------

// labelMask is an antialiased ink-coverage mask (0..1) for a rendered label.
type labelMask struct {
	W, H int
	Cov  []float64
}

// titleLabelMaskPad is the margin, in pixels, the mask keeps around the ink for
// the outline pass.
const titleLabelMaskPad = 1

// Bitmap-face metrics, in em units. The bundled face's cap height is 0.8 em and
// the built-in 8x8 face draws its cap in seven rows, so the fallback glyphs are
// scaled to the same cap height and baseline.
const (
	bitmapCapUnits   = 0.8    // cap height as a fraction of the em
	bitmapCapRows    = 7.0    // the visible cap rows of the 8x8 face
	bitmapGapUnits   = 0.06   // inter-glyph gap as a fraction of the em
	bitmapEmFallback = 1000.0 // em used when the bundled face cannot be read
	bitmapCells      = 8      // the built-in face is 8x8
)

// runeSource is the glyph chosen for one rune of the label.
type runeSource struct {
	r      rune
	bitmap bool
	gid    int
	cont   [][]fontPoint
	adv    float64 // font units
	top    float64
	bottom float64
}

// labelFaceChoice decides which face draws the whole label. The bundled face is
// used only when it can supply every rune; otherwise the patch's built-in
// bitmap face draws the whole label, so the footer never mixes two designs. The
// runes the bundled face cannot supply are returned so the choice is reported.
func labelFaceChoice(face *displayFace, label string) (bool, []rune) {
	var missing []rune
	for _, r := range label {
		if !runeAvailable(face, r) {
			missing = append(missing, r)
		}
	}
	return len(missing) == 0, missing
}

// runeSources lays the label out glyph by glyph with the chosen face.
func runeSources(label string, face *displayFace, units float64) []runeSource {
	useBundled, _ := labelFaceChoice(face, label)
	var out []runeSource
	for _, r := range label {
		src := runeSource{r: r}
		if useBundled {
			gid, ok := face.cmap[r]
			if !ok {
				continue
			}
			cont, err := face.glyphContours(gid)
			if err != nil {
				continue
			}
			src.gid = gid
			src.cont = cont
			src.adv = face.advance(gid)
			if len(cont) > 0 {
				src.top, src.bottom = contourExtent(cont)
			}
			out = append(out, src)
			continue
		}
		// Built-in bitmap face for this rune.
		g := bitmapGlyph(r)
		row := units * bitmapCapUnits / bitmapCapRows
		col := row
		minRow, maxRow := bitmapCells, -1
		for ry := 0; ry < bitmapCells; ry++ {
			if g[ry] == 0 {
				continue
			}
			if ry < minRow {
				minRow = ry
			}
			if ry > maxRow {
				maxRow = ry
			}
		}
		if maxRow < 0 {
			// An all-zero cell (a space) carries no ink.
			src.top, src.bottom = 0, 0
		} else {
			src.top = units*bitmapCapUnits - float64(minRow)*row
			src.bottom = units*bitmapCapUnits - float64(maxRow+1)*row
		}
		src.bitmap = true
		src.adv = float64(bitmapCells)*col + units*bitmapGapUnits
		out = append(out, src)
	}
	return out
}

// contourExtent returns the bounding box of a glyph's outline points.
func contourExtent(cont [][]fontPoint) (top, bottom float64) {
	top, bottom = -1e18, 1e18
	for _, c := range cont {
		for _, p := range c {
			if p.Y > top {
				top = p.Y
			}
			if p.Y < bottom {
				bottom = p.Y
			}
		}
	}
	return top, bottom
}

// bitmapGlyph returns the 8x8 rows of the built-in face for a rune.
func bitmapGlyph(r rune) [8]uint8 {
	if r >= 0 && r < 128 {
		return font8x8Basic[r]
	}
	return font8x8Basic['?']
}

// runeAvailable reports whether the bundled face can draw a rune.
func runeAvailable(face *displayFace, r rune) bool {
	if face == nil {
		return false
	}
	gid, ok := face.cmap[r]
	if !ok {
		return false
	}
	_, err := face.glyphContours(gid)
	return err == nil
}

// TitleLabelRendering reports which face the footer label is drawn with and,
// when the supplied face cannot cover the whole label, the runes it lacks. The
// footer never mixes faces: if the face cannot draw every rune the built-in
// bitmap face draws the whole label, and this function says so. An invalid face
// is reported, never swallowed.
func TitleLabelRendering(fontBytes []byte, label string) (face, fallback string) {
	f, err := parseFont(fontBytes)
	if err != nil {
		return "built-in bitmap font", "the supplied font could not be read: " + err.Error()
	}
	ok, missing := labelFaceChoice(f, label)
	if ok {
		return "supplied font", ""
	}
	return "built-in bitmap font", "the supplied font has no glyph for " + string(missing)
}

// renderTitleLabel rasterises the label, using the supplied face where it can
// and the built-in bitmap face for any rune it cannot supply.
//
// The em is chosen by the style: font_size 0 selects the largest em that fits
// the footer rectangle (the default auto-fit); a positive font_size pins the em
// directly, and the caller refuses a label that then does not fit rather than
// clipping it.
func renderTitleLabel(label string, f *displayFace, style TitleFooterStyle) *labelMask {
	units := bitmapEmFallback
	if f != nil {
		units = float64(f.unitsPerEm)
	}
	src := runeSources(label, f, units)
	if len(src) == 0 {
		return nil
	}
	spacingUnits := style.LetterSpacing * units
	totalAdv, top, bottom := 0.0, -1e18, 1e18
	for _, s := range src {
		totalAdv += s.adv + spacingUnits
		if s.top > top {
			top = s.top
		}
		if s.bottom < bottom {
			bottom = s.bottom
		}
	}
	if totalAdv <= 0 || top <= bottom {
		return nil
	}
	rect := titleFooterRect()
	var em float64
	if style.FontSize > 0 {
		em = style.FontSize
	} else {
		innerW := float64(rect.Dx() - 2*titleLabelMaskPad)
		innerH := float64(rect.Dy() - 2*titleLabelMaskPad)
		em = math.Min(innerH*units/(top-bottom), innerW*units/totalAdv)
	}
	if em <= 0 {
		return nil
	}
	scale := em / units

	w := int(math.Ceil(totalAdv*scale)) + 2*titleLabelMaskPad
	h := int(math.Ceil((top-bottom)*scale)) + 2*titleLabelMaskPad
	if w <= 0 || h <= 0 {
		return nil
	}
	baseline := float64(titleLabelMaskPad) + top*scale

	edges := make([]pixelEdge, 0, 1024)
	pen := float64(titleLabelMaskPad)
	row := units * bitmapCapUnits / bitmapCapRows
	col := row
	cellTop := units * bitmapCapUnits
	spacingPx := spacingUnits * scale
	for _, s := range src {
		if s.bitmap {
			g := bitmapGlyph(s.r)
			for ry := 0; ry < bitmapCells; ry++ {
				for cx := 0; cx < bitmapCells; cx++ {
					if g[ry]&(1<<uint(cx)) == 0 {
						continue
					}
					x0 := math.Floor(pen + float64(cx)*col*scale + 0.5)
					x1 := math.Floor(pen + float64(cx+1)*col*scale + 0.5)
					if x1 <= x0 {
						x1 = x0 + 1
					}
					y0 := math.Floor(baseline - cellTop*scale + float64(ry)*row*scale + 0.5)
					y1 := math.Floor(baseline - cellTop*scale + float64(ry+1)*row*scale + 0.5)
					if y1 <= y0 {
						y1 = y0 + 1
					}
					edges = append(edges,
						pixelEdge{x0, y0, x1, y0},
						pixelEdge{x1, y0, x1, y1},
						pixelEdge{x1, y1, x0, y1},
						pixelEdge{x0, y1, x0, y0},
					)
				}
			}
			pen += s.adv*scale + spacingPx
			continue
		}
		edges = appendGlyphEdges(edges, s.cont, scale, pen, baseline)
		pen += s.adv*scale + spacingPx
	}
	cov := fillEdges(edges, w, h, 4)
	for i, v := range cov {
		if v < 0 {
			cov[i] = 0
		} else if v > 1 {
			cov[i] = 1
		}
	}
	return &labelMask{W: w, H: h, Cov: cov}
}

// pixelEdge is one flattened outline edge in pixel space (y grows downward).
type pixelEdge struct{ X0, Y0, X1, Y1 float64 }

// appendGlyphEdges flattens a glyph's quadratic contours into pixel-space line
// edges. The transform is x' = ox + x*scale and y' = oy - y*scale.
func appendGlyphEdges(dst []pixelEdge, cont [][]fontPoint, scale, ox, oy float64) []pixelEdge {
	toPix := func(p fontPoint) (float64, float64) { return ox + p.X*scale, oy - p.Y*scale }
	for _, c := range cont {
		n := len(c)
		if n < 2 {
			continue
		}
		var cur fontPoint
		start := 0
		switch {
		case c[0].On:
			cur = c[0]
			start = 1
		case c[n-1].On:
			cur = c[n-1]
		default:
			cur = fontPoint{X: (c[0].X + c[1].X) / 2, Y: (c[0].Y + c[1].Y) / 2}
		}
		first := cur
		i := start
		lines := func(a, b fontPoint) {
			x0, y0 := toPix(a)
			x1, y1 := toPix(b)
			dst = append(dst, pixelEdge{x0, y0, x1, y1})
		}
		for k := 0; k < n+2; k++ {
			pt := c[i%n]
			if pt.On {
				lines(cur, pt)
				cur = pt
				i++
				if i-start >= n {
					break
				}
				continue
			}
			nxt := c[(i+1)%n]
			if nxt.On {
				appendQuad(&dst, cur, pt, nxt, scale, ox, oy)
				cur = nxt
				i += 2
				if i-start >= n {
					break
				}
				continue
			}
			mid := fontPoint{X: (pt.X + nxt.X) / 2, Y: (pt.Y + nxt.Y) / 2}
			appendQuad(&dst, cur, pt, mid, scale, ox, oy)
			cur = mid
			i++
			if i-start >= n {
				break
			}
		}
		lines(cur, first)
	}
	return dst
}

// appendQuad flattens one quadratic Bezier segment into line edges. The step
// count follows the segment length so glyph curves stay smooth at any size.
func appendQuad(dst *[]pixelEdge, a, c, b fontPoint, scale, ox, oy float64) {
	toPix := func(p fontPoint) (float64, float64) { return ox + p.X*scale, oy - p.Y*scale }
	ax, ay := toPix(a)
	cx, cy := toPix(c)
	bx, by := toPix(b)
	d := math.Hypot(cx-ax, cy-ay) + math.Hypot(bx-cx, by-cy)
	steps := int(d/2) + 2
	if steps < 2 {
		steps = 2
	}
	if steps > 32 {
		steps = 32
	}
	x0, y0 := ax, ay
	for i := 1; i <= steps; i++ {
		t := float64(i) / float64(steps)
		mt := 1 - t
		x := mt*mt*ax + 2*mt*t*cx + t*t*bx
		y := mt*mt*ay + 2*mt*t*cy + t*t*by
		*dst = append(*dst, pixelEdge{x0, y0, x, y})
		x0, y0 = x, y
	}
}

// fillEdges accumulates nonzero-winding coverage of the edges onto a w x h
// grid. Each scanline is sampled ss times vertically and spans are added with
// their exact horizontal overlap, so the mask is antialiased.
func fillEdges(edges []pixelEdge, w, h, ss int) []float64 {
	cov := make([]float64, w*h)
	if len(edges) == 0 || w <= 0 || h <= 0 {
		return cov
	}
	xs := make([]float64, 0, len(edges))
	dirs := make([]int8, 0, len(edges))
	for ry := 0; ry < h*ss; ry++ {
		y := (float64(ry) + 0.5) / float64(ss)
		xs = xs[:0]
		dirs = dirs[:0]
		for i := range edges {
			e := &edges[i]
			if !((e.Y0 <= y && e.Y1 > y) || (e.Y1 <= y && e.Y0 > y)) {
				continue
			}
			t := (y - e.Y0) / (e.Y1 - e.Y0)
			xs = append(xs, e.X0+t*(e.X1-e.X0))
			if e.Y1 > e.Y0 {
				dirs = append(dirs, 1)
			} else {
				dirs = append(dirs, -1)
			}
		}
		if len(xs) < 2 {
			continue
		}
		for i := 1; i < len(xs); i++ {
			for j := i; j > 0 && xs[j] < xs[j-1]; j-- {
				xs[j], xs[j-1] = xs[j-1], xs[j]
				dirs[j], dirs[j-1] = dirs[j-1], dirs[j]
			}
		}
		row := ry / ss
		weight := 1 / float64(ss)
		winding := 0
		for i := 0; i+1 < len(xs); i++ {
			winding += int(dirs[i])
			if winding == 0 {
				continue
			}
			addSpan(cov, w, row, xs[i], xs[i+1], weight)
		}
	}
	return cov
}

// addSpan adds a horizontal span to one pixel row with fractional endpoints.
func addSpan(cov []float64, w, row int, xa, xb, weight float64) {
	if xa > xb {
		xa, xb = xb, xa
	}
	if xa < 0 {
		xa = 0
	}
	if xb > float64(w) {
		xb = float64(w)
	}
	if xb <= xa {
		return
	}
	ia := int(xa)
	ib := int(xb)
	if ia == ib {
		cov[row*w+ia] += (xb - xa) * weight
		return
	}
	cov[row*w+ia] += (float64(ia+1) - xa) * weight
	for i := ia + 1; i < ib; i++ {
		cov[row*w+i] += weight
	}
	if ib < w {
		cov[row*w+ib] += (xb - float64(ib)) * weight
	}
}

// dilateCoverage returns the maximum ink coverage in a (2r+1)^2 neighbourhood of
// every mask pixel: the outline pass that surrounds the glyphs.
func dilateCoverage(m *labelMask, r int) []float64 {
	out := make([]float64, len(m.Cov))
	for y := 0; y < m.H; y++ {
		for x := 0; x < m.W; x++ {
			best := 0.0
			for dy := -r; dy <= r; dy++ {
				yy := y + dy
				if yy < 0 || yy >= m.H {
					continue
				}
				for dx := -r; dx <= r; dx++ {
					xx := x + dx
					if xx < 0 || xx >= m.W {
						continue
					}
					if v := m.Cov[yy*m.W+xx]; v > best {
						best = v
					}
				}
			}
			out[y*m.W+x] = best
		}
	}
	return out
}
