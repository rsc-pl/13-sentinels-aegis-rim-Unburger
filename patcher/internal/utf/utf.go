// Package utf implements the CRI "@UTF" table format used inside CPK containers
// (the header, TOC and ETOC packets of the archive).
//
// Layout (big-endian):
//
//	0x00 "@UTF"
//	0x04 u32 table_size              (= total_packet_len - 8)
//	0x08 u32 rows_offset  (+8)
//	0x0C u32 str_offset   (+8)
//	0x10 u32 data_offset  (+8)
//	0x14 u32 table_name_off (into string table)
//	0x18 u16 num_columns
//	0x1A u16 row_length
//	0x1C u32 num_rows
//	then per-column [flags:u8][name_off:u32][const value if storage==0x30]
//	then rows (row_length bytes each)
//	then string table ("<NULL>\0" + table name + column names + row strings)
//	then data area
//
// Storage mask 0xF0: 0x00 none, 0x10 zero, 0x30 const, 0x50 per-row.
// Type    mask 0x0F: 0/1 u8, 2/3 u16, 4/5 u32, 6/7 u64, 8 float, 0xA string,
// 0xB data.
//
// Serialisation de-duplicates the string table in first-use order and NUL-pads
// so the data area starts on an 8-byte boundary; the whole packet is padded to 8
// bytes. This reproduces the original tables byte-for-byte.
package utf

import (
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	stMask = 0xF0
	tyMask = 0x0F

	stNone  = 0x00
	stZero  = 0x10
	stConst = 0x30
	stRow   = 0x50

	// TyU8..TyData are the value type codes.
	TyU8   = 0x0
	TyU16  = 0x2
	TyU32  = 0x4
	TyU64  = 0x6
	TyF32  = 0x8
	TyStr  = 0xA
	TyData = 0xB
)

// Value holds a single cell. Only the fields relevant to the column type are used.
type Value struct {
	Type uint8
	U    uint64 // numeric (and the raw bits of an f32)
	S    []byte // string
	DO   uint32 // data offset
	DS   uint32 // data size
	Blob []byte // data payload
}

// Column is one column definition.
type Column struct {
	Flags byte
	Name  []byte
	Const *Value // set when storage == stConst
}

// Table is a parsed @UTF table.
type Table struct {
	Name []byte
	Cols []Column
	Rows [][]Value // Rows[r][c] valid when Cols[c] is per-row
}

func typeSize(typ uint8) int {
	switch typ {
	case TyU8:
		return 1
	case TyU16:
		return 2
	case TyU32:
		return 4
	case TyU64:
		return 8
	case TyF32:
		return 4
	case TyStr:
		return 4
	case TyData:
		return 8
	}
	return 0
}

func cstr(b []byte, off int) ([]byte, error) {
	if off < 0 || off >= len(b) {
		return nil, fmt.Errorf("cstring offset %d out of range (%d)", off, len(b))
	}
	for i := off; i < len(b); i++ {
		if b[i] == 0 {
			return b[off:i], nil
		}
	}
	return nil, errors.New("unterminated cstring")
}

// Parse parses a de-obfuscated @UTF packet.
func Parse(buf []byte) (*Table, error) {
	if len(buf) < 32 || string(buf[0:4]) != "@UTF" {
		return nil, errors.New("not @UTF")
	}
	tableSize := binary.BigEndian.Uint32(buf[4:8])
	if int(tableSize) > len(buf)-8 {
		return nil, fmt.Errorf("table_size %d exceeds packet %d", tableSize, len(buf))
	}
	rowsOff := int(binary.BigEndian.Uint32(buf[8:12])) + 8
	strOff := int(binary.BigEndian.Uint32(buf[12:16])) + 8
	dataOff := int(binary.BigEndian.Uint32(buf[16:20])) + 8
	tnameOff := int(binary.BigEndian.Uint32(buf[20:24]))
	ncol := int(binary.BigEndian.Uint16(buf[24:26]))
	rowlen := int(binary.BigEndian.Uint16(buf[26:28]))
	nrow := int(binary.BigEndian.Uint32(buf[28:32]))
	if ncol < 0 || nrow < 0 || ncol > 0xFFFF || nrow > 100000000 {
		return nil, errors.New("implausible table dims")
	}
	if strOff < 0 || strOff > len(buf) || dataOff < strOff || dataOff > len(buf) {
		return nil, errors.New("bad table offsets")
	}
	if rowlen < 0 || rowsOff < 32 || rowsOff > strOff {
		return nil, errors.New("bad rows/str offsets")
	}
	if nrow > 0 && rowsOff+nrow*rowlen > len(buf) {
		return nil, errors.New("rows exceed packet")
	}
	name, err := cstr(buf, strOff+tnameOff)
	if err != nil {
		return nil, err
	}
	t := &Table{Name: name}
	p := 32
	for i := 0; i < ncol; i++ {
		if p >= len(buf) {
			return nil, errors.New("column defs overrun")
		}
		flags := buf[p]
		p++
		if flags == 0 {
			if p+3 >= len(buf) {
				return nil, errors.New("column flags overrun")
			}
			p += 3
			flags = buf[p]
			p++
		}
		if p+4 > len(buf) {
			return nil, errors.New("column name overrun")
		}
		nameOff := int(binary.BigEndian.Uint32(buf[p : p+4]))
		p += 4
		nm, err := cstr(buf, strOff+nameOff)
		if err != nil {
			return nil, err
		}
		col := Column{Flags: flags, Name: nm}
		st := flags & stMask
		typ := flags & tyMask
		if st == stConst {
			v := &Value{Type: typ}
			sz := typeSize(typ)
			if p+sz > len(buf) {
				return nil, errors.New("const value overrun")
			}
			switch typ {
			case TyU8:
				v.U = uint64(buf[p])
			case TyU16:
				v.U = uint64(binary.BigEndian.Uint16(buf[p : p+2]))
			case TyU32:
				v.U = uint64(binary.BigEndian.Uint32(buf[p : p+4]))
			case TyU64:
				v.U = binary.BigEndian.Uint64(buf[p : p+8])
			case TyF32:
				v.U = uint64(binary.BigEndian.Uint32(buf[p : p+4]))
			case TyStr:
				so := int(binary.BigEndian.Uint32(buf[p : p+4]))
				s, err := cstr(buf, strOff+so)
				if err != nil {
					return nil, err
				}
				v.S = s
			case TyData:
				v.DO = binary.BigEndian.Uint32(buf[p : p+4])
				v.DS = binary.BigEndian.Uint32(buf[p+4 : p+8])
			default:
				return nil, fmt.Errorf("unsupported const type 0x%x", typ)
			}
			p += sz
			col.Const = v
		}
		t.Cols = append(t.Cols, col)
	}
	if p != rowsOff {
		return nil, fmt.Errorf("column defs ended at %d, rows at %d", p, rowsOff)
	}
	for r := 0; r < nrow; r++ {
		base := rowsOff + r*rowlen
		q := base
		row := make([]Value, ncol)
		for c := range t.Cols {
			col := t.Cols[c]
			st := col.Flags & stMask
			typ := col.Flags & tyMask
			switch st {
			case stNone, stZero:
				row[c] = Value{Type: typ}
				continue
			case stConst:
				row[c] = *col.Const
				continue
			}
			sz := typeSize(typ)
			if q+sz > len(buf) {
				return nil, errors.New("row value overrun")
			}
			v := Value{Type: typ}
			switch typ {
			case TyU8:
				v.U = uint64(buf[q])
			case TyU16:
				v.U = uint64(binary.BigEndian.Uint16(buf[q : q+2]))
			case TyU32:
				v.U = uint64(binary.BigEndian.Uint32(buf[q : q+4]))
			case TyU64:
				v.U = binary.BigEndian.Uint64(buf[q : q+8])
			case TyF32:
				v.U = uint64(binary.BigEndian.Uint32(buf[q : q+4]))
			case TyStr:
				so := int(binary.BigEndian.Uint32(buf[q : q+4]))
				s, err := cstr(buf, strOff+so)
				if err != nil {
					return nil, err
				}
				v.S = s
			case TyData:
				v.DO = binary.BigEndian.Uint32(buf[q : q+4])
				v.DS = binary.BigEndian.Uint32(buf[q+4 : q+8])
				if int(v.DO)+int(v.DS) > len(buf)-dataOff {
					return nil, errors.New("data blob out of range")
				}
				v.Blob = append([]byte(nil), buf[dataOff+int(v.DO):dataOff+int(v.DO)+int(v.DS)]...)
			default:
				return nil, fmt.Errorf("unsupported type 0x%x", typ)
			}
			row[c] = v
			q += sz
		}
		t.Rows = append(t.Rows, row)
	}
	return t, nil
}

func writeNum(dst []byte, typ uint8, u uint64) []byte {
	switch typ {
	case TyU8:
		return append(dst, byte(u))
	case TyU16:
		return binary.BigEndian.AppendUint16(dst, uint16(u))
	case TyU32:
		return binary.BigEndian.AppendUint32(dst, uint32(u))
	case TyU64:
		return binary.BigEndian.AppendUint64(dst, u)
	case TyF32:
		return binary.BigEndian.AppendUint32(dst, uint32(u))
	}
	panic("utf: writeNum bad type")
}

// Build serialises a table.
func Build(t *Table) ([]byte, error) {
	strtab := make([]byte, 0, 256)
	index := map[string]uint32{}
	add := func(s []byte) uint32 {
		key := string(s)
		if off, ok := index[key]; ok {
			return off
		}
		off := uint32(len(strtab))
		index[key] = off
		strtab = append(strtab, s...)
		strtab = append(strtab, 0)
		return off
	}
	add([]byte("<NULL>"))
	tnameIdx := add(t.Name)
	for _, c := range t.Cols {
		add(c.Name)
	}
	for _, c := range t.Cols {
		if c.Flags&stMask == stConst && c.Flags&tyMask == TyStr {
			add(c.Const.S)
		}
	}
	for _, row := range t.Rows {
		for c := range t.Cols {
			if t.Cols[c].Flags&stMask == stRow && t.Cols[c].Flags&tyMask == TyStr {
				add(row[c].S)
			}
		}
	}

	coldef := make([]byte, 0, 256)
	rowlen := 0
	for _, c := range t.Cols {
		coldef = append(coldef, c.Flags)
		coldef = binary.BigEndian.AppendUint32(coldef, index[string(c.Name)])
		st := c.Flags & stMask
		typ := c.Flags & tyMask
		if st == stRow {
			rowlen += typeSize(typ)
		}
		if st == stConst {
			switch typ {
			case TyStr:
				coldef = binary.BigEndian.AppendUint32(coldef, index[string(c.Const.S)])
			case TyData:
				coldef = binary.BigEndian.AppendUint32(coldef, c.Const.DO)
				coldef = binary.BigEndian.AppendUint32(coldef, c.Const.DS)
			default:
				coldef = writeNum(coldef, typ, c.Const.U)
			}
		}
	}

	rows := make([]byte, 0, rowlen*len(t.Rows))
	data := make([]byte, 0)
	for _, row := range t.Rows {
		for c := range t.Cols {
			col := t.Cols[c]
			st := col.Flags & stMask
			typ := col.Flags & tyMask
			if st != stRow {
				continue
			}
			v := row[c]
			switch typ {
			case TyStr:
				rows = binary.BigEndian.AppendUint32(rows, index[string(v.S)])
			case TyData:
				do := uint32(len(data))
				data = append(data, v.Blob...)
				rows = binary.BigEndian.AppendUint32(rows, do)
				rows = binary.BigEndian.AppendUint32(rows, uint32(len(v.Blob)))
			default:
				rows = writeNum(rows, typ, v.U)
			}
		}
	}

	rowsOff := 32 + len(coldef)
	strOff := rowsOff + len(rows)
	for (strOff+len(strtab))%8 != 0 {
		strtab = append(strtab, 0)
	}
	dataOff := strOff + len(strtab)

	out := make([]byte, 0, dataOff+len(data)+8)
	out = append(out, "@UTF"...)
	out = binary.BigEndian.AppendUint32(out, 0) // table_size placeholder
	out = binary.BigEndian.AppendUint32(out, uint32(rowsOff-8))
	out = binary.BigEndian.AppendUint32(out, uint32(strOff-8))
	out = binary.BigEndian.AppendUint32(out, uint32(dataOff-8))
	out = binary.BigEndian.AppendUint32(out, tnameIdx)
	out = binary.BigEndian.AppendUint16(out, uint16(len(t.Cols)))
	out = binary.BigEndian.AppendUint16(out, uint16(rowlen))
	out = binary.BigEndian.AppendUint32(out, uint32(len(t.Rows)))
	out = append(out, coldef...)
	out = append(out, rows...)
	out = append(out, strtab...)
	out = append(out, data...)
	for len(out)%8 != 0 {
		out = append(out, 0)
	}
	binary.BigEndian.PutUint32(out[4:8], uint32(len(out)-8))
	return out, nil
}

// RowCount returns the number of rows.
func (t *Table) RowCount() int { return len(t.Rows) }

// ColIndex returns the column index for a name, or -1.
func (t *Table) ColIndex(name string) int {
	for i := range t.Cols {
		if string(t.Cols[i].Name) == name {
			return i
		}
	}
	return -1
}

// GetU returns the numeric cell for column c in row r.
func (t *Table) GetU(r, c int) uint64 { return t.Rows[r][c].U }

// SetU sets the numeric cell for column c in row r.
func (t *Table) SetU(r, c int, v uint64) { t.Rows[r][c].U = v }
