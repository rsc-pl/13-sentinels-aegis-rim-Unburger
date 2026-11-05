package localsources

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
)

// PFS0 is the simple archive container used by the local game containers. The
// header is unencrypted, so entry names can be listed without any key material.
//
//	0x00 "PFS0"
//	0x04 u32 file count
//	0x08 u32 string-table size
//	0x0C u32 reserved
//	0x10 file count * { u64 offset, u64 size, u32 name offset, u32 reserved }
//	...  NUL-terminated name string table
//	...  entry data (offsets are relative to the data start, 0x20-aligned)
type PFS0 struct {
	FileCount int
	DataStart int64
	Files     []PFS0File
}

// PFS0File is one container entry.
type PFS0File struct {
	Name   string
	Offset uint64
	Size   uint64
}

// ParsePFS0 reads the container header and entry table from r.
func ParsePFS0(r io.ReaderAt) (*PFS0, error) {
	head := make([]byte, 0x10)
	if _, err := r.ReadAt(head, 0); err != nil {
		return nil, err
	}
	if string(head[:4]) != "PFS0" {
		return nil, errors.New("not a PFS0 container")
	}
	count := int(binary.LittleEndian.Uint32(head[4:8]))
	strSize := int(binary.LittleEndian.Uint32(head[8:12]))
	if count < 0 || count > 100000 || strSize < 0 || strSize > 64<<20 {
		return nil, fmt.Errorf("implausible PFS0 header (count=%d strings=%d)", count, strSize)
	}
	table := make([]byte, count*0x18)
	if count > 0 {
		if _, err := r.ReadAt(table, 0x10); err != nil {
			return nil, err
		}
	}
	stringsOff := int64(0x10 + count*0x18)
	strTab := make([]byte, strSize)
	if strSize > 0 {
		if _, err := r.ReadAt(strTab, stringsOff); err != nil {
			return nil, err
		}
	}
	dataStart := stringsOff + int64(strSize)
	if rem := dataStart % 0x20; rem != 0 {
		dataStart += 0x20 - rem
	}
	p := &PFS0{FileCount: count, DataStart: dataStart}
	for i := 0; i < count; i++ {
		e := table[i*0x18 : (i+1)*0x18]
		off := binary.LittleEndian.Uint64(e[0:8])
		size := binary.LittleEndian.Uint64(e[8:16])
		nameOff := int(binary.LittleEndian.Uint32(e[16:20]))
		if nameOff < 0 || nameOff > len(strTab) {
			return nil, fmt.Errorf("PFS0 entry %d: name offset out of range", i)
		}
		name := string(strTab[nameOff:])
		if idx := strings.IndexByte(name, 0); idx >= 0 {
			name = name[:idx]
		}
		p.Files = append(p.Files, PFS0File{Name: name, Offset: off, Size: size})
	}
	return p, nil
}

// Entry finds an entry by exact (case-sensitive) name.
func (p *PFS0) Entry(name string) *PFS0File {
	for i := range p.Files {
		if p.Files[i].Name == name {
			return &p.Files[i]
		}
	}
	return nil
}

// ReadEntry reads one entry's bytes.
func (p *PFS0) ReadEntry(r io.ReaderAt, f *PFS0File) ([]byte, error) {
	buf := make([]byte, f.Size)
	if _, err := r.ReadAt(buf, p.DataStart+int64(f.Offset)); err != nil {
		return nil, err
	}
	return buf, nil
}
