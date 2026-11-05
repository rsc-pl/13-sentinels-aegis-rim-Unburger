package texpack

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// ---------------------------------------------------------------------------
// CFF (Type 2 charstring) outlines
// ---------------------------------------------------------------------------
//
// The bundled display face is an OpenType/CFF face, so the reader has to walk a
// CFF table: the header, the Name/Top DICT/String/Global Subr INDEXes, the
// CharStrings INDEX and the Private DICT with its Local Subr INDEX. Glyph
// outlines come from the Type 2 charstring interpreter below, which flattens
// every cubic Bezier to on-curve points so the existing scanline fill can draw
// them. It is deliberately small and fails closed on anything it does not
// understand: unknown operators, malformed INDEXes and CID-keyed or CFF2 faces
// (which need the FDArray/FDSelect machinery) are refused with a message that
// names the limitation.

// cffFont is the parsed subset of a CFF (Type 2 charstring) face that the
// footer renderer needs: one charstring per glyph id and the two subroutine
// INDEXes. Hints, charset, encoding and every other table are ignored: outlines
// are all the renderer consumes (advances come from hmtx).
type cffFont struct {
	charStrings [][]byte // one charstring per glyph id, in gid order
	gsubrs      [][]byte // global subroutines
	lsubrs      [][]byte // local subroutines
}

// cffIndex reads a CFF INDEX at the absolute offset p and returns its items
// plus the offset just past the INDEX. end is the absolute end of the CFF table.
func cffIndex(data []byte, p, end int) ([][]byte, int, error) {
	if p < 0 || p+2 > end {
		return nil, p, errors.New("texpack: CFF INDEX header is truncated")
	}
	count := int(binary.BigEndian.Uint16(data[p : p+2]))
	p += 2
	if count == 0 {
		return nil, p, nil
	}
	if p+1 > end {
		return nil, p, errors.New("texpack: CFF INDEX offSize is missing")
	}
	offSize := int(data[p])
	p++
	if offSize < 1 || offSize > 4 {
		return nil, p, fmt.Errorf("texpack: CFF INDEX offSize %d is invalid", offSize)
	}
	if count > 1<<20 {
		return nil, p, errors.New("texpack: CFF INDEX count is implausible")
	}
	if p+(count+1)*offSize > end {
		return nil, p, errors.New("texpack: CFF INDEX offset array is truncated")
	}
	offs := make([]int, count+1)
	for i := 0; i <= count; i++ {
		v := 0
		for k := 0; k < offSize; k++ {
			v = v<<8 | int(data[p])
			p++
		}
		offs[i] = v
	}
	base := p
	if offs[0] < 1 || offs[count] < 1 {
		return nil, p, errors.New("texpack: CFF INDEX offsets are malformed")
	}
	last := base + offs[count] - 1
	if last > end || last < base {
		return nil, p, errors.New("texpack: CFF INDEX data is out of range")
	}
	items := make([][]byte, count)
	for i := 0; i < count; i++ {
		a, b := base+offs[i]-1, base+offs[i+1]-1
		if a > b || b > end {
			return nil, p, errors.New("texpack: CFF INDEX offsets are malformed")
		}
		items[i] = data[a:b]
	}
	return items, last, nil
}

// cffDict is a decoded CFF DICT: operator -> operands. Operators above 0xFF are
// the two-byte escapes, encoded as 0x0c00|b1.
type cffDict map[int][]float64

// parseCFFDict decodes a CFF DICT. Reals (operator 30 operands) are parsed too,
// so a face that uses them still reads; the footer renderer itself only needs
// the integer operands.
func parseCFFDict(b []byte) (cffDict, error) {
	out := cffDict{}
	operands := make([]float64, 0, 8)
	i := 0
	for i < len(b) {
		b0 := b[i]
		switch {
		case b0 <= 21:
			var op int
			if b0 == 12 {
				if i+1 >= len(b) {
					return nil, errors.New("texpack: CFF DICT escape is truncated")
				}
				op = 0x0c00 | int(b[i+1])
				i += 2
			} else {
				op = int(b0)
				i++
			}
			out[op] = append([]float64(nil), operands...)
			operands = operands[:0]
		case b0 == 28:
			if i+3 > len(b) {
				return nil, errors.New("texpack: CFF DICT number is truncated")
			}
			operands = append(operands, float64(int16(binary.BigEndian.Uint16(b[i+1:i+3]))))
			i += 3
		case b0 == 29:
			if i+5 > len(b) {
				return nil, errors.New("texpack: CFF DICT number is truncated")
			}
			operands = append(operands, float64(int32(binary.BigEndian.Uint32(b[i+1:i+5]))))
			i += 5
		case b0 == 30:
			v, n, err := parseCFFReal(b[i+1:])
			if err != nil {
				return nil, err
			}
			operands = append(operands, v)
			i += 1 + n
		case b0 >= 32 && b0 <= 246:
			operands = append(operands, float64(int(b0)-139))
			i++
		case b0 >= 247 && b0 <= 250:
			if i+2 > len(b) {
				return nil, errors.New("texpack: CFF DICT number is truncated")
			}
			operands = append(operands, float64((int(b0)-247)*256+int(b[i+1])+108))
			i += 2
		case b0 >= 251 && b0 <= 254:
			if i+2 > len(b) {
				return nil, errors.New("texpack: CFF DICT number is truncated")
			}
			operands = append(operands, float64(-(int(b0)-251)*256-int(b[i+1])-108))
			i += 2
		default:
			return nil, fmt.Errorf("texpack: CFF DICT byte %#02x is invalid", b0)
		}
	}
	return out, nil
}

// parseCFFReal decodes a CFF DICT real (nibble-packed). It returns the value and
// the number of bytes consumed.
func parseCFFReal(b []byte) (float64, int, error) {
	var s []byte
	i := 0
	done := false
	for !done {
		if i >= len(b) {
			return 0, 0, errors.New("texpack: CFF DICT real is truncated")
		}
		v := b[i]
		i++
		for _, nib := range [2]byte{v >> 4, v & 0x0f} {
			switch {
			case nib <= 9:
				s = append(s, '0'+nib)
			case nib == 0x0a:
				s = append(s, '.')
			case nib == 0x0b:
				s = append(s, 'E')
			case nib == 0x0c:
				s = append(s, 'E', '-')
			case nib == 0x0e:
				s = append(s, '-')
			case nib == 0x0f:
				done = true
			default:
				return 0, 0, fmt.Errorf("texpack: CFF DICT real nibble %#x is reserved", nib)
			}
			if done {
				break
			}
		}
	}
	if len(s) == 0 {
		return 0, i, nil
	}
	var v float64
	if _, err := fmt.Sscanf(string(s), "%g", &v); err != nil {
		return 0, 0, fmt.Errorf("texpack: CFF DICT real %q is malformed", s)
	}
	return v, i, nil
}

// parseCFF parses the CFF table at (off, length) into the glyph charstrings and
// the subroutine INDEXes. It fails closed on every structure it does not
// support.
func parseCFF(data []byte, off, length int) (*cffFont, error) {
	if length < 4 || off < 0 || off+length > len(data) {
		return nil, errors.New("texpack: CFF table is truncated")
	}
	end := off + length
	if data[off] != 1 {
		return nil, fmt.Errorf("texpack: unsupported CFF version %d.%d (only CFF 1.x is supported)", data[off], data[off+1])
	}
	hdrSize := int(data[off+2])
	if hdrSize < 4 || off+hdrSize > end {
		return nil, errors.New("texpack: CFF header size is out of range")
	}
	p := off + hdrSize
	var err error
	if _, p, err = cffIndex(data, p, end); err != nil { // Name INDEX
		return nil, err
	}
	topDicts, p, err := cffIndex(data, p, end)
	if err != nil {
		return nil, err
	}
	if len(topDicts) != 1 {
		return nil, fmt.Errorf("texpack: CFF has %d Top DICTs (only a single non-CID face is supported)", len(topDicts))
	}
	if _, p, err = cffIndex(data, p, end); err != nil { // String INDEX
		return nil, err
	}
	gsubrs, _, err := cffIndex(data, p, end)
	if err != nil {
		return nil, err
	}
	top, err := parseCFFDict(topDicts[0])
	if err != nil {
		return nil, err
	}
	if _, ok := top[0x0c1e]; ok { // ROS: CID-keyed font
		return nil, errors.New("texpack: CID-keyed CFF faces are not supported")
	}
	csOff, ok := top[17]
	if !ok || len(csOff) < 1 || csOff[0] < 0 {
		return nil, errors.New("texpack: CFF face has no CharStrings INDEX")
	}
	charStrings, _, err := cffIndex(data, off+int(csOff[0]), end)
	if err != nil {
		return nil, err
	}
	face := &cffFont{charStrings: charStrings, gsubrs: gsubrs}
	if priv, ok := top[18]; ok && len(priv) >= 2 {
		privSize, privOff := int(priv[0]), off+int(priv[1])
		if privSize < 0 || privOff < off || privOff+privSize > end {
			return nil, errors.New("texpack: CFF Private DICT is out of range")
		}
		pd, err := parseCFFDict(data[privOff : privOff+privSize])
		if err != nil {
			return nil, err
		}
		if subrs, ok := pd[19]; ok && len(subrs) >= 1 {
			lsOff := privOff + int(subrs[0])
			face.lsubrs, _, err = cffIndex(data, lsOff, end)
			if err != nil {
				return nil, err
			}
		}
	}
	if len(face.charStrings) == 0 {
		return nil, errors.New("texpack: CFF face declares no glyphs")
	}
	return face, nil
}

// cffMaxSubrDepth bounds subroutine recursion, so a malformed face cannot make
// the parser loop or overflow the stack.
const cffMaxSubrDepth = 10

// cffRun is the mutable state of one Type 2 charstring interpretation.
type cffRun struct {
	cff      *cffFont
	stack    []float64
	x, y     float64
	cur      []fontPoint
	contours [][]fontPoint
	nStems   int
}

// glyphContours returns the flattened outlines of one CFF glyph. An empty slice
// means the glyph has no ink. An error means the glyph cannot be drawn.
func (c *cffFont) glyphContours(gid int) ([][]fontPoint, error) {
	if gid < 0 || gid >= len(c.charStrings) {
		return nil, fmt.Errorf("texpack: glyph %d is out of range", gid)
	}
	r := &cffRun{cff: c}
	if err := r.charstring(c.charStrings[gid], 0); err != nil {
		return nil, fmt.Errorf("texpack: CFF glyph %d: %w", gid, err)
	}
	r.closeContour()
	if len(r.contours) == 0 {
		return nil, nil
	}
	return r.contours, nil
}

func (r *cffRun) closeContour() {
	if len(r.cur) >= 2 {
		r.contours = append(r.contours, r.cur)
	}
	r.cur = nil
}

func (r *cffRun) moveTo(nx, ny float64) {
	r.closeContour()
	r.x, r.y = nx, ny
	r.cur = []fontPoint{{X: nx, Y: ny, On: true}}
}

func (r *cffRun) lineTo(nx, ny float64) error {
	if r.cur == nil {
		return errors.New("outline draws a line before any move")
	}
	r.cur = append(r.cur, fontPoint{X: nx, Y: ny, On: true})
	r.x, r.y = nx, ny
	return nil
}

func (r *cffRun) curveTo(c1x, c1y, c2x, c2y, ex, ey float64) error {
	if r.cur == nil {
		return errors.New("outline draws a curve before any move")
	}
	r.cur = appendCubicPoints(r.cur, r.x, r.y, c1x, c1y, c2x, c2y, ex, ey)
	r.x, r.y = ex, ey
	return nil
}

// appendCubicPoints flattens one cubic Bezier to on-curve points (excluding the
// start point, which is already the last point of pts). The step count follows
// the control-polygon length, so curves stay smooth at any rendered size.
func appendCubicPoints(pts []fontPoint, x0, y0, c1x, c1y, c2x, c2y, x1, y1 float64) []fontPoint {
	d := math.Hypot(c1x-x0, c1y-y0) + math.Hypot(c2x-c1x, c2y-c1y) + math.Hypot(x1-c2x, y1-c2y)
	steps := int(d/6) + 2
	if steps < 2 {
		steps = 2
	}
	if steps > 24 {
		steps = 24
	}
	for i := 1; i <= steps; i++ {
		t := float64(i) / float64(steps)
		mt := 1 - t
		x := mt*mt*mt*x0 + 3*mt*mt*t*c1x + 3*mt*t*t*c2x + t*t*t*x1
		y := mt*mt*mt*y0 + 3*mt*mt*t*c1y + 3*mt*t*t*c2y + t*t*t*y1
		pts = append(pts, fontPoint{X: x, Y: y, On: true})
	}
	return pts
}

// readCFFNumber decodes one charstring number at index i and returns the value
// and the index just past it.
func readCFFNumber(b []byte, i int) (float64, int, error) {
	b0 := b[i]
	switch {
	case b0 == 28:
		if i+3 > len(b) {
			return 0, 0, errors.New("number is truncated")
		}
		return float64(int16(binary.BigEndian.Uint16(b[i+1 : i+3]))), i + 3, nil
	case b0 == 255:
		if i+5 > len(b) {
			return 0, 0, errors.New("number is truncated")
		}
		return float64(int32(binary.BigEndian.Uint32(b[i+1:i+5]))) / 65536, i + 5, nil
	case b0 >= 32 && b0 <= 246:
		return float64(int(b0) - 139), i + 1, nil
	case b0 >= 247 && b0 <= 250:
		if i+2 > len(b) {
			return 0, 0, errors.New("number is truncated")
		}
		return float64((int(b0)-247)*256 + int(b[i+1]) + 108), i + 2, nil
	case b0 >= 251 && b0 <= 254:
		if i+2 > len(b) {
			return 0, 0, errors.New("number is truncated")
		}
		return float64(-(int(b0)-251)*256 - int(b[i+1]) - 108), i + 2, nil
	}
	return 0, 0, fmt.Errorf("byte %#02x is not a number", b0)
}

// cffSubrBias is the charstring subroutine index bias for a subroutine count.
func cffSubrBias(n int) int {
	switch {
	case n < 1240:
		return 107
	case n < 33900:
		return 1131
	default:
		return 32768
	}
}

// charstring interprets one Type 2 charstring, recursing through subroutine
// calls. Unknown operators and malformed stacks fail closed.
func (r *cffRun) charstring(cs []byte, depth int) error {
	if depth > cffMaxSubrDepth {
		return errors.New("subroutine recursion is too deep")
	}
	i := 0
	for i < len(cs) {
		b0 := cs[i]
		if b0 == 12 {
			if i+1 >= len(cs) {
				return errors.New("escaped operator is truncated")
			}
			if err := r.escaped(cs[i+1]); err != nil {
				return err
			}
			i += 2
			continue
		}
		if b0 == 28 || b0 == 255 || b0 >= 32 {
			v, n, err := readCFFNumber(cs, i)
			if err != nil {
				return err
			}
			r.stack = append(r.stack, v)
			i = n
			continue
		}
		switch b0 {
		case 1, 3, 18, 23: // hstem vstem hstemhm vstemhm
			if len(r.stack)%2 == 1 {
				r.stack = r.stack[1:] // a leading width
			}
			r.nStems += len(r.stack) / 2
			r.stack = r.stack[:0]
			i++
		case 19, 20: // hintmask cntrmask
			if len(r.stack) > 0 { // implicit vstem hints
				if len(r.stack)%2 == 1 {
					r.stack = r.stack[1:]
				}
				r.nStems += len(r.stack) / 2
				r.stack = r.stack[:0]
			}
			i++
			need := (r.nStems + 7) / 8
			if i+need > len(cs) {
				return errors.New("hintmask runs past the charstring")
			}
			i += need
		case 4: // vmoveto
			if len(r.stack) > 1 {
				r.stack = r.stack[len(r.stack)-1:]
			}
			if len(r.stack) != 1 {
				return errors.New("vmoveto has a bad argument count")
			}
			r.moveTo(r.x, r.y+r.stack[0])
			r.stack = r.stack[:0]
			i++
		case 21: // rmoveto
			if len(r.stack) > 2 {
				r.stack = r.stack[len(r.stack)-2:]
			}
			if len(r.stack) != 2 {
				return errors.New("rmoveto has a bad argument count")
			}
			r.moveTo(r.x+r.stack[0], r.y+r.stack[1])
			r.stack = r.stack[:0]
			i++
		case 22: // hmoveto
			if len(r.stack) > 1 {
				r.stack = r.stack[len(r.stack)-1:]
			}
			if len(r.stack) != 1 {
				return errors.New("hmoveto has a bad argument count")
			}
			r.moveTo(r.x+r.stack[0], r.y)
			r.stack = r.stack[:0]
			i++
		case 5: // rlineto
			if len(r.stack) == 0 || len(r.stack)%2 != 0 {
				return errors.New("rlineto has a bad argument count")
			}
			for k := 0; k+1 < len(r.stack); k += 2 {
				if err := r.lineTo(r.x+r.stack[k], r.y+r.stack[k+1]); err != nil {
					return err
				}
			}
			r.stack = r.stack[:0]
			i++
		case 6: // hlineto
			if err := r.alternateLines(true); err != nil {
				return err
			}
			i++
		case 7: // vlineto
			if err := r.alternateLines(false); err != nil {
				return err
			}
			i++
		case 8: // rrcurveto
			if len(r.stack) == 0 || len(r.stack)%6 != 0 {
				return errors.New("rrcurveto has a bad argument count")
			}
			for k := 0; k+5 < len(r.stack); k += 6 {
				if err := r.relCurve(r.stack[k:]...); err != nil {
					return err
				}
			}
			r.stack = r.stack[:0]
			i++
		case 24: // rcurveline
			if len(r.stack) < 8 || (len(r.stack)-2)%6 != 0 {
				return errors.New("rcurveline has a bad argument count")
			}
			n := len(r.stack) - 2
			for k := 0; k+5 < n; k += 6 {
				if err := r.relCurve(r.stack[k:]...); err != nil {
					return err
				}
			}
			if err := r.lineTo(r.x+r.stack[n], r.y+r.stack[n+1]); err != nil {
				return err
			}
			r.stack = r.stack[:0]
			i++
		case 25: // rlinecurve
			if len(r.stack) < 8 || (len(r.stack)-6)%2 != 0 {
				return errors.New("rlinecurve has a bad argument count")
			}
			n := len(r.stack) - 6
			for k := 0; k+1 < n; k += 2 {
				if err := r.lineTo(r.x+r.stack[k], r.y+r.stack[k+1]); err != nil {
					return err
				}
			}
			if err := r.relCurve(r.stack[n:]...); err != nil {
				return err
			}
			r.stack = r.stack[:0]
			i++
		case 26: // vvcurveto
			if err := r.vvCurve(); err != nil {
				return err
			}
			i++
		case 27: // hhcurveto
			if err := r.hhCurve(); err != nil {
				return err
			}
			i++
		case 30: // vhcurveto
			if err := r.altCurve(false); err != nil {
				return err
			}
			i++
		case 31: // hvcurveto
			if err := r.altCurve(true); err != nil {
				return err
			}
			i++
		case 10: // callsubr
			if len(r.stack) < 1 {
				return errors.New("callsubr underflow")
			}
			idx := int(r.stack[len(r.stack)-1]) + cffSubrBias(len(r.cff.lsubrs))
			r.stack = r.stack[:len(r.stack)-1]
			if idx < 0 || idx >= len(r.cff.lsubrs) {
				return fmt.Errorf("callsubr index %d is out of range", idx)
			}
			if err := r.charstring(r.cff.lsubrs[idx], depth+1); err != nil {
				return err
			}
			i++
		case 29: // callgsubr
			if len(r.stack) < 1 {
				return errors.New("callgsubr underflow")
			}
			idx := int(r.stack[len(r.stack)-1]) + cffSubrBias(len(r.cff.gsubrs))
			r.stack = r.stack[:len(r.stack)-1]
			if idx < 0 || idx >= len(r.cff.gsubrs) {
				return fmt.Errorf("callgsubr index %d is out of range", idx)
			}
			if err := r.charstring(r.cff.gsubrs[idx], depth+1); err != nil {
				return err
			}
			i++
		case 11: // return
			return nil
		case 14: // endchar
			switch len(r.stack) {
			case 0:
			case 1, 5: // leading width
				r.stack = r.stack[:0]
			case 4: // deprecated seac composition
				return errors.New("endchar accented-glyph composition is not supported")
			default:
				return errors.New("endchar has a bad argument count")
			}
			r.closeContour()
			return nil
		default:
			return fmt.Errorf("unsupported charstring operator %d", b0)
		}
	}
	return nil
}

// relCurve emits one relative cubic curve from six deltas (dxa dya dxb dyb dxc
// dyc).
func (r *cffRun) relCurve(a ...float64) error {
	return r.curveTo(
		r.x+a[0], r.y+a[1],
		r.x+a[0]+a[2], r.y+a[1]+a[3],
		r.x+a[0]+a[2]+a[4], r.y+a[1]+a[3]+a[5],
	)
}

// alternateLines implements hlineto/vlineto: single deltas alternating between
// horizontal and vertical moves, starting in the given direction.
func (r *cffRun) alternateLines(horiz bool) error {
	if len(r.stack) == 0 {
		return errors.New("hlineto/vlineto has no arguments")
	}
	for _, d := range r.stack {
		if horiz {
			if err := r.lineTo(r.x+d, r.y); err != nil {
				return err
			}
		} else {
			if err := r.lineTo(r.x, r.y+d); err != nil {
				return err
			}
		}
		horiz = !horiz
	}
	r.stack = r.stack[:0]
	return nil
}

// vvCurve implements vvcurveto: {dx1?} {dya dxb dyb dyc}+.
func (r *cffRun) vvCurve() error {
	args := r.stack
	dx1 := 0.0
	i := 0
	if len(args)%4 == 1 {
		dx1 = args[0]
		i = 1
	}
	if len(args) < 4 || (len(args)-i)%4 != 0 {
		return errors.New("vvcurveto has a bad argument count")
	}
	for ; i+3 < len(args); i += 4 {
		dya, dxb, dyb, dyc := args[i], args[i+1], args[i+2], args[i+3]
		c1x, c1y := r.x+dx1, r.y+dya
		c2x, c2y := c1x+dxb, c1y+dyb
		if err := r.curveTo(c1x, c1y, c2x, c2y, c2x, c2y+dyc); err != nil {
			return err
		}
		dx1 = 0
	}
	r.stack = r.stack[:0]
	return nil
}

// hhCurve implements hhcurveto: {dy1?} {dxa dxb dyb dxc}+.
func (r *cffRun) hhCurve() error {
	args := r.stack
	dy1 := 0.0
	i := 0
	if len(args)%4 == 1 {
		dy1 = args[0]
		i = 1
	}
	if len(args) < 4 || (len(args)-i)%4 != 0 {
		return errors.New("hhcurveto has a bad argument count")
	}
	for ; i+3 < len(args); i += 4 {
		dxa, dxb, dyb, dxc := args[i], args[i+1], args[i+2], args[i+3]
		c1x, c1y := r.x+dxa, r.y+dy1
		c2x, c2y := c1x+dxb, c1y+dyb
		if err := r.curveTo(c1x, c1y, c2x, c2y, c2x+dxc, c2y); err != nil {
			return err
		}
		dy1 = 0
	}
	r.stack = r.stack[:0]
	return nil
}

// altCurve implements hvcurveto/vhcurveto: curves whose start is alternately
// horizontal and vertical, with one optional extra final coordinate.
func (r *cffRun) altCurve(startHoriz bool) error {
	args := r.stack
	n := len(args)
	if n < 4 || (n-4)%4 != 0 && (n-5)%4 != 0 {
		return errors.New("hv/vhcurveto has a bad argument count")
	}
	horiz := startHoriz
	for i := 0; i+3 < n; {
		extra := 0.0
		hasExtra := n-i == 5
		if hasExtra {
			extra = args[i+4]
		}
		if horiz { // h->v: dx1 dx2 dy2 dy3
			c1x, c1y := r.x+args[i], r.y
			c2x, c2y := c1x+args[i+1], c1y+args[i+2]
			ey := c2y + args[i+3]
			if hasExtra {
				ey += extra
			}
			if err := r.curveTo(c1x, c1y, c2x, c2y, c2x, ey); err != nil {
				return err
			}
		} else { // v->h: dy1 dx2 dy2 dx3
			c1x, c1y := r.x, r.y+args[i]
			c2x, c2y := c1x+args[i+1], c1y+args[i+2]
			ex := c2x + args[i+3]
			if hasExtra {
				ex += extra
			}
			if err := r.curveTo(c1x, c1y, c2x, c2y, ex, c2y); err != nil {
				return err
			}
		}
		if hasExtra {
			break
		}
		i += 4
		horiz = !horiz
	}
	r.stack = r.stack[:0]
	return nil
}

// escaped implements the 12-prefixed operators: the arithmetic/storage subset
// and the flex family (each flex expands to the two relative curves the spec
// defines it to be equivalent to).
func (r *cffRun) escaped(b1 byte) error {
	switch b1 {
	case 34: // hflex: dx1 dx2 dy2 dx3 dx4 dx5 dx6
		a, err := r.take(7, "hflex")
		if err != nil {
			return err
		}
		if err := r.relCurve(a[0], 0, a[1], a[2], a[3], 0); err != nil {
			return err
		}
		return r.relCurve(a[4], 0, a[5], -a[2], a[6], 0)
	case 35: // flex: dx1 dy1 dx2 dy2 dx3 dy3 dx4 dy4 dx5 dy5 dx6 dy6 fd
		a, err := r.take(13, "flex")
		if err != nil {
			return err
		}
		if err := r.relCurve(a[0], a[1], a[2], a[3], a[4], a[5]); err != nil {
			return err
		}
		return r.relCurve(a[6], a[7], a[8], a[9], a[10], a[11])
	case 36: // hflex1: dx1 dy1 dx2 dy2 dx3 dx4 dx5 dy5 dx6
		a, err := r.take(9, "hflex1")
		if err != nil {
			return err
		}
		if err := r.relCurve(a[0], a[1], a[2], a[3], a[4], -(a[1] + a[3])); err != nil {
			return err
		}
		return r.relCurve(a[5], 0, a[6], a[7], a[8], a[1]+a[3]+a[7])
	case 37: // flex1: dx1 dy1 dx2 dy2 dx3 dy3 dx4 dy4 dx5 dy5 d6
		a, err := r.take(11, "flex1")
		if err != nil {
			return err
		}
		dx := a[0] + a[2] + a[4] + a[6] + a[8]
		dy := a[1] + a[3] + a[5] + a[7] + a[9]
		var d6x, d6y float64
		if math.Abs(dx) > math.Abs(dy) {
			d6x, d6y = a[10], -dy
		} else {
			d6x, d6y = -dx, a[10]
		}
		if err := r.relCurve(a[0], a[1], a[2], a[3], a[4], a[5]); err != nil {
			return err
		}
		return r.relCurve(a[6], a[7], a[8], a[9], d6x, d6y)
	case 3: // and
		return r.binary("and", func(a, b float64) float64 {
			if a != 0 && b != 0 {
				return 1
			}
			return 0
		})
	case 4: // or
		return r.binary("or", func(a, b float64) float64 {
			if a != 0 || b != 0 {
				return 1
			}
			return 0
		})
	case 5: // not
		return r.unary("not", func(a float64) float64 {
			if a == 0 {
				return 1
			}
			return 0
		})
	case 9: // abs
		return r.unary("abs", math.Abs)
	case 10: // add
		return r.binary("add", func(a, b float64) float64 { return a + b })
	case 11: // sub
		return r.binary("sub", func(a, b float64) float64 { return a - b })
	case 12: // div
		return r.binary("div", func(a, b float64) float64 {
			if b == 0 {
				return 0
			}
			return a / b
		})
	case 14: // neg
		return r.unary("neg", func(a float64) float64 { return -a })
	case 15: // eq
		return r.binary("eq", func(a, b float64) float64 {
			if a == b {
				return 1
			}
			return 0
		})
	case 18: // drop
		if len(r.stack) < 1 {
			return errors.New("drop underflow")
		}
		r.stack = r.stack[:len(r.stack)-1]
		return nil
	case 24: // mul
		return r.binary("mul", func(a, b float64) float64 { return a * b })
	case 26: // sqrt
		return r.unary("sqrt", math.Sqrt)
	case 27: // dup
		if len(r.stack) < 1 {
			return errors.New("dup underflow")
		}
		r.stack = append(r.stack, r.stack[len(r.stack)-1])
		return nil
	case 28: // exch
		if len(r.stack) < 2 {
			return errors.New("exch underflow")
		}
		n := len(r.stack)
		r.stack[n-1], r.stack[n-2] = r.stack[n-2], r.stack[n-1]
		return nil
	case 20, 21, 22, 23, 29, 30:
		// Storage/random operators: a valid Type 2 charstring cannot use the
		// transient array in a way that changes an outline, but guessing would
		// break the fail-closed rule, so refuse them.
		return fmt.Errorf("escaped operator %d (storage/random) is not supported", b1)
	default:
		return fmt.Errorf("unsupported escaped charstring operator %d", b1)
	}
}

func (r *cffRun) take(n int, name string) ([]float64, error) {
	if len(r.stack) < n {
		return nil, fmt.Errorf("%s underflow", name)
	}
	out := append([]float64(nil), r.stack[len(r.stack)-n:]...)
	r.stack = r.stack[:len(r.stack)-n]
	return out, nil
}

func (r *cffRun) unary(name string, fn func(float64) float64) error {
	if len(r.stack) < 1 {
		return fmt.Errorf("%s underflow", name)
	}
	r.stack[len(r.stack)-1] = fn(r.stack[len(r.stack)-1])
	return nil
}

func (r *cffRun) binary(name string, fn func(a, b float64) float64) error {
	if len(r.stack) < 2 {
		return fmt.Errorf("%s underflow", name)
	}
	n := len(r.stack)
	r.stack[n-2] = fn(r.stack[n-2], r.stack[n-1])
	r.stack = r.stack[:n-1]
	return nil
}
