// Package fmsb reads and writes FMSB text banks (the game's localised string
// containers). The format is a simple forward string table:
//
//	0x00 "FMSB"
//	0x04 u32 declared_size (= total_len - 48)
//	0x08 u64 0x20
//	0x10 u32 0
//	0x14 u32 string_count
//	0x18 u64 3
//	0x20 string_count * 8 zero table
//	then string_count NUL-terminated UTF-8 strings (forward)
//	then zero padding to the next 16-byte boundary
//	then "FEOC" + u32 0 + u64 0x10
//
// No-op parse->rebuild is byte-identical. Structural anomalies fail closed;
// control tokens (inline markup used by the renderer for branches and emphasis)
// are copied verbatim and can be validated separately.
package fmsb

import (
	"encoding/binary"
	"errors"
	"fmt"
)

const headerLen = 0x20

var trailer = append([]byte("FEOC"), []byte{0, 0, 0, 0, 0x10, 0, 0, 0, 0, 0, 0, 0}...)

// Bank is a parsed text bank.
type Bank struct {
	Declared uint32
	Magic8   uint64
	U10      uint32
	Magic18  uint64
	Strings  [][]byte
}

// Parse parses a logical FMSB bank.
func Parse(data []byte) (*Bank, error) {
	if len(data) < headerLen+16 || string(data[:4]) != "FMSB" {
		return nil, errors.New("bad magic/size")
	}
	declared := binary.LittleEndian.Uint32(data[4:8])
	magic8 := binary.LittleEndian.Uint64(data[8:16])
	u10 := binary.LittleEndian.Uint32(data[0x10:0x14])
	count := int(binary.LittleEndian.Uint32(data[0x14:0x18]))
	magic18 := binary.LittleEndian.Uint64(data[0x18:0x20])
	if count < 0 || count > 5000000 || headerLen+count*8 > len(data) {
		return nil, errors.New("implausible string count")
	}
	f := &Bank{Declared: declared, Magic8: magic8, U10: u10, Magic18: magic18}
	p := headerLen + count*8
	for i := 0; i < count; i++ {
		e := -1
		for j := p; j < len(data); j++ {
			if data[j] == 0 {
				e = j
				break
			}
		}
		if e < 0 {
			return nil, fmt.Errorf("missing terminator for record %d", i)
		}
		f.Strings = append(f.Strings, append([]byte(nil), data[p:e]...))
		p = e + 1
	}
	tail := data[p:]
	fi := indexOf(tail, []byte("FEOC"))
	if fi < 0 || string(tail[fi:]) != string(trailer) {
		return nil, errors.New("bad FEOC trailer")
	}
	for _, b := range tail[:fi] {
		if b != 0 {
			return nil, errors.New("nonzero padding before trailer")
		}
	}
	return f, nil
}

func indexOf(hay, needle []byte) int {
	if len(needle) == 0 || len(hay) < len(needle) {
		return -1
	}
	for i := 0; i+len(needle) <= len(hay); i++ {
		ok := true
		for j := range needle {
			if hay[i+j] != needle[j] {
				ok = false
				break
			}
		}
		if ok {
			return i
		}
	}
	return -1
}

// Rebuild rebuilds a bank from a (possibly edited) record list; recomputes
// count/declared/padding. The zero table is regenerated as all-zero.
func (f *Bank) Rebuild(strings [][]byte) []byte {
	out := make([]byte, 0, 0x20+len(strings)*8)
	out = append(out, "FMSB"...)
	out = binary.LittleEndian.AppendUint32(out, 0)
	out = binary.LittleEndian.AppendUint64(out, f.Magic8)
	out = binary.LittleEndian.AppendUint32(out, f.U10)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(strings)))
	out = binary.LittleEndian.AppendUint64(out, f.Magic18)
	out = append(out, make([]byte, len(strings)*8)...)
	for _, s := range strings {
		out = append(out, s...)
		out = append(out, 0)
	}
	for len(out)%16 != 0 {
		out = append(out, 0)
	}
	out = append(out, trailer...)
	binary.LittleEndian.PutUint32(out[4:8], uint32(len(out)-48))
	return out
}

// ValidateControls performs a basic fail-closed check of known control-token
// families: @c(n) / @#(n) numeric tokens and ##(...) tokens must be terminated.
func ValidateControls(s []byte) error {
	i := 0
	for i < len(s) {
		if i+3 <= len(s) && (string(s[i:i+3]) == "@c(" || string(s[i:i+3]) == "@#(") {
			j := indexOf(s[i+3:], []byte(")"))
			if j < 0 {
				return errors.New("unterminated control token")
			}
			num := s[i+3 : i+3+j]
			if len(num) < 1 || len(num) > 4 {
				return errors.New("invalid control argument")
			}
			for _, c := range num {
				if c < '0' || c > '9' {
					return errors.New("invalid control argument")
				}
			}
			i = i + 3 + j + 1
		} else if i+3 <= len(s) && string(s[i:i+3]) == "##(" {
			j := indexOf(s[i+3:], []byte(")"))
			if j < 0 {
				return errors.New("unterminated ##() token")
			}
			i = i + 3 + j + 1
		} else {
			i++
		}
	}
	return nil
}
