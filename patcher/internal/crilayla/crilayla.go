// Package crilayla implements the CRILAYLA compression codec used for member
// payloads inside a CPK archive.
//
// Block layout (0x800-aligned inside a CPK):
//
//	0x00 "CRILAYLA"
//	0x08 u32le uncompressed_size (usz, payload bytes)
//	0x0C u32le compressed_size   (dsz, bitstream bytes)
//	0x10 .. 0x10+dsz          compressed bitstream
//	0x10+dsz .. +0x100        raw 0x100-byte output header
//	logical file = raw_0x100 + reverse(decode(bitstream)[:usz])
//
// Bits are read MSB-first from the END of the bitstream. Control bit 1 = back-ref
// (13-bit offset-3, then a 2/3/5/8/8... variable-length match length), 0 = literal
// byte.
//
// The decoder is stable and verified. The encoder guarantees
// decode(encode(x)) == x; it is not byte-identical to any particular original LZ
// stream, which is fine because a deterministic semantic rebuild is accepted.
package crilayla

import (
	"encoding/binary"
	"errors"
	"fmt"
)

type bitReader struct {
	buf   []byte // already reversed
	nbits int
	pos   int
}

func newBitReader(comp []byte) *bitReader {
	r := make([]byte, len(comp))
	for i := range comp {
		r[i] = comp[len(comp)-1-i]
	}
	return &bitReader{buf: r, nbits: len(r) * 8}
}

func (b *bitReader) read(n int) (uint32, error) {
	var out uint32
	for i := 0; i < n; i++ {
		if b.pos >= b.nbits {
			return 0, errors.New("bitstream exhausted")
		}
		byteVal := b.buf[b.pos>>3]
		bit := (byteVal >> (7 - (b.pos & 7))) & 1
		out = (out << 1) | uint32(bit)
		b.pos++
	}
	return out, nil
}

// Decode returns the logical bytes (raw 0x100 header + payload) of a CRILAYLA block.
func Decode(blob []byte) ([]byte, error) {
	if len(blob) < 0x10 || string(blob[0:8]) != "CRILAYLA" {
		return nil, errors.New("not a CRILAYLA block")
	}
	usz := int(binary.LittleEndian.Uint32(blob[8:12]))
	dsz := int(binary.LittleEndian.Uint32(blob[12:16]))
	if usz < 0 || dsz < 0 || 0x10+dsz+0x100 > len(blob) {
		return nil, fmt.Errorf("bad CRILAYLA sizes usz=%d dsz=%d len=%d", usz, dsz, len(blob))
	}
	comp := blob[0x10 : 0x10+dsz]
	raw := blob[0x10+dsz : 0x10+dsz+0x100]
	br := newBitReader(comp)
	out := make([]byte, 0, usz)
	for len(out) < usz {
		bit, err := br.read(1)
		if err != nil {
			break
		}
		if bit == 1 {
			o, err := br.read(13)
			if err != nil {
				break
			}
			off := int(o) + 3
			ln := 3
			lv := 2
			for {
				c, err := br.read(lv)
				if err != nil {
					return nil, err
				}
				ln += int(c)
				if int(c) != (1<<lv)-1 {
					break
				}
				switch lv {
				case 2:
					lv = 3
				case 3:
					lv = 5
				default:
					lv = 8
				}
			}
			r := len(out) - off
			if r < 0 {
				return nil, errors.New("backref before start")
			}
			for i := 0; i < ln; i++ {
				out = append(out, out[r])
				r++
			}
		} else {
			v, err := br.read(8)
			if err != nil {
				break
			}
			out = append(out, byte(v))
		}
	}
	if len(out) < usz {
		return nil, fmt.Errorf("decode short: %d < %d", len(out), usz)
	}
	payload := out[:usz]
	rev := make([]byte, len(payload))
	for i := range payload {
		rev[i] = payload[len(payload)-1-i]
	}
	res := make([]byte, 0, 0x100+usz)
	res = append(res, raw...)
	res = append(res, rev...)
	return res, nil
}

type bitWriter struct {
	buf []byte
	cur byte
	n   int
}

func (w *bitWriter) bit(b int) {
	w.cur = (w.cur << 1) | byte(b&1)
	w.n++
	if w.n == 8 {
		w.buf = append(w.buf, w.cur)
		w.cur = 0
		w.n = 0
	}
}

func (w *bitWriter) bits(v uint32, k int) {
	for i := k - 1; i >= 0; i-- {
		w.bit(int((v >> uint(i)) & 1))
	}
}

func (w *bitWriter) finish() []byte {
	if w.n > 0 {
		w.buf = append(w.buf, w.cur<<uint(8-w.n))
	}
	out := make([]byte, len(w.buf))
	for i := range w.buf {
		out[i] = w.buf[len(w.buf)-1-i]
	}
	return out
}

// finish4 is finish() followed by zero-padding the emission buffer up to a 4-byte
// multiple, so the stored stream length (dsz) is always a multiple of 4. The
// padding lands at the front of the reversed stream and is never read by the
// decoder (it stops as soon as it has produced usz bytes).
func (w *bitWriter) finish4() []byte {
	if w.n > 0 {
		w.buf = append(w.buf, w.cur<<uint(8-w.n))
	}
	for len(w.buf)%4 != 0 {
		w.buf = append(w.buf, 0)
	}
	out := make([]byte, len(w.buf))
	for i := range w.buf {
		out[i] = w.buf[len(w.buf)-1-i]
	}
	return out
}

const (
	minMatch = 3
	maxOff   = (1 << 13) + 2
	maxMatch = 8192
)

func emitLength(w *bitWriter, length int) {
	n := length - 3
	v := n
	if v > 3 {
		v = 3
	}
	w.bits(uint32(v), 2)
	if v < 3 {
		return
	}
	n -= 3
	v = n
	if v > 7 {
		v = 7
	}
	w.bits(uint32(v), 3)
	if v < 7 {
		return
	}
	n -= 7
	v = n
	if v > 31 {
		v = 31
	}
	w.bits(uint32(v), 5)
	if v < 31 {
		return
	}
	n -= 31
	for {
		v = n
		if v > 255 {
			v = 255
		}
		w.bits(uint32(v), 8)
		if v < 255 {
			return
		}
		n -= 255
	}
}

// Encoder names selectable through configuration.
const (
	// EncoderOld is the original greedy encoder (single most-recent 3-byte
	// candidate, match length capped, byte-only padding).
	EncoderOld = "old"
	// EncoderChain is the longest-match encoder: hash-chain candidate walk over the
	// full window, ties keep the smallest offset, 4-byte-aligned stream.
	EncoderChain = "chain"
	// ChainCapDefault bounds how many same-hash candidates are examined per position.
	ChainCapDefault = 256
)

func encodeGreedy(logical []byte) []byte {
	if len(logical) < 0x100 {
		panic("crilayla: logical shorter than 0x100 header")
	}
	raw := logical[:0x100]
	payload := logical[0x100:]
	usz := len(payload)
	R := make([]byte, len(payload))
	for i := range payload {
		R[i] = payload[len(payload)-1-i]
	}
	n := len(R)
	w := &bitWriter{}
	pos := map[string]int{}
	i := 0
	for i < n {
		bestLen, bestOff := 0, 0
		if i+minMatch <= n {
			key := string(R[i : i+minMatch])
			if j, ok := pos[key]; ok {
				off := i - j
				if off >= minMatch && off <= maxOff {
					l := 0
					for i+l < n && l < maxMatch && R[j+l] == R[i+l] {
						l++
					}
					if l >= minMatch {
						bestLen, bestOff = l, off
					}
				}
			}
		}
		if bestLen >= minMatch {
			w.bit(1)
			w.bits(uint32(bestOff-3), 13)
			emitLength(w, bestLen)
			hi := i + bestLen
			if n-minMatch+1 < hi {
				hi = n - minMatch + 1
			}
			for k := i; k < hi; k++ {
				pos[string(R[k:k+minMatch])] = k
			}
			i += bestLen
		} else {
			w.bit(0)
			w.bits(uint32(R[i]), 8)
			if i+minMatch <= n {
				pos[string(R[i:i+minMatch])] = i
			}
			i++
		}
	}
	comp := w.finish()
	out := make([]byte, 0, 0x10+len(comp)+0x100)
	out = append(out, "CRILAYLA"...)
	out = binary.LittleEndian.AppendUint32(out, uint32(usz))
	out = binary.LittleEndian.AppendUint32(out, uint32(len(comp)))
	out = append(out, comp...)
	out = append(out, raw...)
	return out
}

func hash3(b []byte, i int) uint32 {
	return uint32(b[i]) | uint32(b[i+1])<<8 | uint32(b[i+2])<<16
}

// EncodeChain builds a CRILAYLA block with a hash-chain longest-match search. At
// every position it walks the chain of previous occurrences of the same 3-byte
// hash within the window and keeps the longest match; ties keep the smallest
// offset. Deterministic.
func EncodeChain(logical []byte, chainCap int) []byte {
	if len(logical) < 0x100 {
		panic("crilayla: logical shorter than 0x100 header")
	}
	if chainCap <= 0 {
		chainCap = ChainCapDefault
	}
	raw := logical[:0x100]
	payload := logical[0x100:]
	N := len(payload)
	R := make([]byte, N)
	for i := range payload {
		R[i] = payload[N-1-i]
	}
	w := &bitWriter{}
	head := make(map[uint32]int32)
	next := make([]int32, N)
	for i := range next {
		next[i] = -1
	}
	insert := func(k int) {
		if k+minMatch > N {
			return
		}
		h := hash3(R, k)
		next[k] = head[h]
		head[h] = int32(k)
	}
	for j := 0; j < N; {
		bestLen, bestOff := 0, 0
		if j+minMatch <= N {
			h := hash3(R, j)
			p, ok := head[h]
			chain := 0
			for ok && chain < chainCap {
				off := j - int(p)
				if off > maxOff {
					break
				}
				l := 0
				for j+l < N && R[int(p)+l] == R[j+l] {
					l++
				}
				if off >= minMatch && l > bestLen {
					bestLen, bestOff = l, off
				}
				p = next[p]
				ok = p >= 0
				chain++
			}
		}
		if bestLen >= minMatch {
			w.bit(1)
			w.bits(uint32(bestOff-3), 13)
			emitLength(w, bestLen)
			end := j + bestLen
			for k := j; k < end; k++ {
				insert(k)
			}
			j = end
		} else {
			w.bit(0)
			w.bits(uint32(R[j]), 8)
			insert(j)
			j++
		}
	}
	comp := w.finish4()
	out := make([]byte, 0, 0x10+len(comp)+0x100)
	out = append(out, "CRILAYLA"...)
	out = binary.LittleEndian.AppendUint32(out, uint32(N))
	out = binary.LittleEndian.AppendUint32(out, uint32(len(comp)))
	out = append(out, comp...)
	out = append(out, raw...)
	return out
}

// Encode dispatches on the encoder name. "" or EncoderChain selects the
// longest-match encoder; EncoderOld selects the greedy encoder.
func Encode(logical []byte, version string) ([]byte, error) {
	switch version {
	case "", EncoderChain:
		return EncodeChain(logical, ChainCapDefault), nil
	case EncoderOld:
		return encodeGreedy(logical), nil
	default:
		return nil, fmt.Errorf("unknown encoder version %q (want %q or %q)", version, EncoderChain, EncoderOld)
	}
}
