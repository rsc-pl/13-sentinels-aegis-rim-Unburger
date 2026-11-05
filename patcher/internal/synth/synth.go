// Package synth builds small, structurally valid CPK archives and FMSB banks
// from scratch. It is used by the tests and by the offline demo generator so the
// whole pipeline can be exercised without any copyrighted game data.
package synth

import (
	"encoding/binary"
	"fmt"
	"sort"

	"13s-patcher/internal/cpk"
	"13s-patcher/internal/crilayla"
	"13s-patcher/internal/utf"
)

// Bank builds a valid logical FMSB bank (same layout as a rebuild).
func Bank(items []string) []byte {
	out := make([]byte, 0, 0x20+len(items)*8)
	out = append(out, "FMSB"...)
	out = binary.LittleEndian.AppendUint32(out, 0)
	out = binary.LittleEndian.AppendUint64(out, 0x20)
	out = binary.LittleEndian.AppendUint32(out, 0)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(items)))
	out = binary.LittleEndian.AppendUint64(out, 3)
	out = append(out, make([]byte, len(items)*8)...)
	for _, s := range items {
		out = append(out, []byte(s)...)
		out = append(out, 0)
	}
	for len(out)%16 != 0 {
		out = append(out, 0)
	}
	out = append(out, append([]byte("FEOC"), []byte{0, 0, 0, 0, 0x10, 0, 0, 0, 0, 0, 0, 0}...)...)
	binary.LittleEndian.PutUint32(out[4:8], uint32(len(out)-48))
	return out
}

// Member is one member of a synthetic archive.
type Member struct {
	Dir      string
	Name     string
	Logical  []byte
	Compress bool
	ID       uint32
}

func tableRow(name string, cols []string, typ []uint8) *utf.Table {
	t := &utf.Table{Name: []byte(name)}
	for i, c := range cols {
		t.Cols = append(t.Cols, utf.Column{Flags: 0x50 | typ[i], Name: []byte(c)})
	}
	return t
}

// Archive assembles a minimal but structurally valid CPK accepted by cpk.Parse.
func Archive(members []Member) ([]byte, error) {
	if len(members) == 0 {
		return nil, fmt.Errorf("synthetic archive needs at least one member")
	}
	ms := make([]Member, len(members))
	copy(ms, members)
	sort.Slice(ms, func(i, j int) bool { return cpk.PathKey(ms[i].Dir, ms[i].Name) < cpk.PathKey(ms[j].Dir, ms[j].Name) })
	seen := map[uint32]bool{}
	for i := range ms {
		if ms[i].Dir == "" || ms[i].Name == "" {
			return nil, fmt.Errorf("synthetic member %d: empty dir/name", i)
		}
		if seen[ms[i].ID] {
			return nil, fmt.Errorf("synthetic member %d: duplicate id %d", i, ms[i].ID)
		}
		seen[ms[i].ID] = true
	}

	const (
		tocOffset     = 2048
		contentOffset = 6144
	)
	toc := tableRow("CpkTocInfo", []string{"DirName", "FileName", "FileSize", "ExtractSize", "FileOffset", "ID"},
		[]uint8{utf.TyStr, utf.TyStr, utf.TyU32, utf.TyU32, utf.TyU64, utf.TyU32})
	etoc := tableRow("CpkEtocInfo", []string{"UpdateDateTime", "LocalDir"}, []uint8{utf.TyU64, utf.TyStr})

	payloads := make([][]byte, len(ms))
	offsets := make([]uint64, len(ms))
	abs := uint64(contentOffset)
	for i := range ms {
		var stored []byte
		if ms[i].Compress {
			if len(ms[i].Logical) < 0x100 {
				return nil, fmt.Errorf("synthetic member %s/%s: compress requires >= 0x100 bytes", ms[i].Dir, ms[i].Name)
			}
			stored = crilayla.EncodeChain(ms[i].Logical, crilayla.ChainCapDefault)
		} else {
			stored = ms[i].Logical
		}
		payloads[i] = stored
		abs = alignUp(abs, cpk.Align)
		offsets[i] = abs - cpk.AbsBias
		abs += uint64(len(stored))

		toc.Rows = append(toc.Rows, []utf.Value{
			{Type: utf.TyStr, S: []byte(ms[i].Dir)},
			{Type: utf.TyStr, S: []byte(ms[i].Name)},
			{Type: utf.TyU32, U: uint64(len(stored))},
			{Type: utf.TyU32, U: uint64(len(ms[i].Logical))},
			{Type: utf.TyU64, U: offsets[i]},
			{Type: utf.TyU32, U: uint64(ms[i].ID)},
		})
	}
	etocOffset := alignUp(abs, cpk.Align)

	var sumF, sumE uint64
	for i := range ms {
		sumF += uint64(len(payloads[i]))
		sumE += uint64(len(ms[i].Logical))
	}
	for i := range ms {
		etoc.Rows = append(etoc.Rows, []utf.Value{
			{Type: utf.TyU64, U: 0},
			{Type: utf.TyStr, S: []byte(ms[i].Dir)},
		})
	}
	etoc.Rows = append(etoc.Rows, []utf.Value{{Type: utf.TyU64, U: 0}, {Type: utf.TyStr, S: nil}})

	tocBytes, err := utf.Build(toc)
	if err != nil {
		return nil, err
	}
	etocBytes, err := utf.Build(etoc)
	if err != nil {
		return nil, err
	}
	if uint64(tocOffset)+uint64(len(tocBytes))+16 > contentOffset {
		return nil, fmt.Errorf("synthetic TOC does not fit before ContentOffset")
	}

	header := &utf.Table{Name: []byte("CpkHeader")}
	hn := []string{"Tvers", "Version", "Revision", "Align", "Codec", "EnableFileName", "Sorted", "CpkMode", "Files",
		"TocOffset", "TocSize", "EtocOffset", "EtocSize", "ContentOffset", "ContentSize", "EnabledDataSize", "EnabledPackedSize"}
	ht := []uint8{utf.TyStr, utf.TyU32, utf.TyU32, utf.TyU32, utf.TyU32, utf.TyU32, utf.TyU32, utf.TyU32, utf.TyU32, utf.TyU64, utf.TyU64, utf.TyU64, utf.TyU64, utf.TyU64, utf.TyU64, utf.TyU64, utf.TyU64}
	for i := range hn {
		header.Cols = append(header.Cols, utf.Column{Flags: 0x50 | ht[i], Name: []byte(hn[i])})
	}
	header.Rows = append(header.Rows, []utf.Value{
		{Type: utf.TyStr, S: []byte(cpk.TversExpected)},
		{Type: utf.TyU32, U: 7},
		{Type: utf.TyU32, U: 14},
		{Type: utf.TyU32, U: cpk.Align},
		{Type: utf.TyU32, U: 0},
		{Type: utf.TyU32, U: 1},
		{Type: utf.TyU32, U: 1},
		{Type: utf.TyU32, U: 1},
		{Type: utf.TyU32, U: uint64(len(ms))},
		{Type: utf.TyU64, U: tocOffset},
		{Type: utf.TyU64, U: uint64(len(tocBytes)) + 16},
		{Type: utf.TyU64, U: etocOffset},
		{Type: utf.TyU64, U: uint64(len(etocBytes)) + 16},
		{Type: utf.TyU64, U: contentOffset},
		{Type: utf.TyU64, U: etocOffset - contentOffset},
		{Type: utf.TyU64, U: sumF},
		{Type: utf.TyU64, U: sumE},
	})
	headerBytes, err := utf.Build(header)
	if err != nil {
		return nil, err
	}

	total := int(etocOffset) + len(etocBytes) + 16
	out := make([]byte, total)
	copy(out[0x7FA:0x800], "(c)CRI")
	copy(out[0:], marshal([4]byte{'C', 'P', 'K', ' '}, headerBytes))
	copy(out[tocOffset:], marshal([4]byte{'T', 'O', 'C', ' '}, tocBytes))
	for i := range ms {
		copy(out[int(offsets[i])+cpk.AbsBias:], payloads[i])
	}
	copy(out[int(etocOffset):], marshal([4]byte{'E', 'T', 'O', 'C'}, etocBytes))
	return out, nil
}

func marshal(tag [4]byte, payload []byte) []byte {
	out := make([]byte, 0, 16+len(payload))
	out = append(out, tag[:]...)
	out = binary.LittleEndian.AppendUint32(out, 0)
	out = binary.LittleEndian.AppendUint64(out, uint64(len(payload)))
	m := uint32(0x655F)
	for _, b := range payload {
		out = append(out, b^byte(m&0xFF))
		m *= 0x4115
	}
	return out
}

func alignUp(n, a uint64) uint64 {
	if a == 0 {
		return n
	}
	return (n + a - 1) / a * a
}

// Bytes produces reproducible pseudo-random bytes for fixtures.
func Bytes(n int, seed uint64) []byte {
	out := make([]byte, n)
	x := seed | 1
	for i := range out {
		x ^= x << 13
		x ^= x >> 7
		x ^= x << 17
		out[i] = byte(x)
	}
	return out
}

// Repetitive produces compressible bytes for fixtures.
func Repetitive(n int, seed int) []byte {
	out := make([]byte, n)
	words := []string{"Sentinel", "kaiju", "Aegis", "1985", "Juro", "Kurabe"}
	for i := range out {
		w := words[(i/16+seed)%len(words)]
		out[i] = w[i%len(w)]
	}
	return out
}
