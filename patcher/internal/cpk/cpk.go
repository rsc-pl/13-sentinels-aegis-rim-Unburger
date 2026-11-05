// Package cpk reads and writes CRI CPK resource archives (the game's
// patch-archive layout).
//
// Proven layout contracts:
//   - file: "CPK " @0x0, "(c)CRI" @0x7FA, "TOC " @TocOffset (2048).
//   - the CPK header, TOC and ETOC are @UTF packets, each XOR-obfuscated with the
//     CRI rolling mask (m=0x655F, t=0x4115; out[i]=in[i]^(m&0xFF), m*=t).
//   - member abs offset = FileOffset + 2048; members are 2048-aligned and laid out
//     sequentially in ascending FileOffset order, starting at ContentOffset.
//   - stored member FileSize = dsz + 0x110; logical ExtractSize = usz + 0x100.
//     FileSize == ExtractSize == raw size for uncompressed members.
//   - header: EnabledDataSize = sum(FileSize); EnabledPackedSize = sum(ExtractSize);
//     ContentSize = EtocOffset - ContentOffset.
package cpk

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"13s-patcher/internal/crilayla"
	"13s-patcher/internal/utf"
)

// Archive layout constants.
const (
	Align         = 2048
	AbsBias       = 2048
	TversExpected = "CPKMC2.49.36, DLL3.28.00"
)

// Packet is an outer CPK packet (tag + u32 + u64 + @UTF payload).
type Packet struct {
	Tag  [4]byte
	Unk  uint32
	Data []byte // de-obfuscated @UTF payload
}

func keystream(n int) []byte {
	ks := make([]byte, n)
	m := uint32(0x655F)
	for i := 0; i < n; i++ {
		ks[i] = byte(m & 0xFF)
		m *= 0x4115
	}
	return ks
}

func xorMask(p []byte) []byte {
	ks := keystream(len(p))
	out := make([]byte, len(p))
	for i := range p {
		out[i] = p[i] ^ ks[i]
	}
	return out
}

func readPacket(data []byte, off int) (*Packet, error) {
	if off < 0 || off+16 > len(data) {
		return nil, errors.New("packet offset out of range")
	}
	p := &Packet{}
	copy(p.Tag[:], data[off:off+4])
	p.Unk = binary.LittleEndian.Uint32(data[off+4 : off+8])
	size := int(binary.LittleEndian.Uint64(data[off+8 : off+16]))
	if size < 0 || off+16+size > len(data) {
		return nil, fmt.Errorf("packet size %d out of range", size)
	}
	raw := data[off+16 : off+16+size]
	if len(raw) >= 4 && string(raw[:4]) == "@UTF" {
		p.Data = append([]byte(nil), raw...)
	} else {
		p.Data = xorMask(raw)
	}
	if len(p.Data) < 4 || string(p.Data[:4]) != "@UTF" {
		return nil, errors.New("packet is not @UTF after de-obfuscation")
	}
	return p, nil
}

func marshalPacket(tag [4]byte, unk uint32, payload []byte) []byte {
	out := make([]byte, 0, 16+len(payload))
	out = append(out, tag[:]...)
	out = binary.LittleEndian.AppendUint32(out, unk)
	out = binary.LittleEndian.AppendUint64(out, uint64(len(payload)))
	out = append(out, xorMask(payload)...)
	return out
}

// Member is one TOC row.
type Member struct {
	RowIndex    int
	Dir         []byte
	FileName    []byte
	FileSize    uint32
	ExtractSize uint32
	FileOffset  uint64
	ID          uint32
	Stored      []byte // stored bytes (compressed block or raw)
}

// Archive is a parsed container plus the field indices needed to rebuild it.
type Archive struct {
	Raw       []byte
	HeaderPkt *Packet
	TOCPkt    *Packet
	EtocPkt   *Packet
	Header    *utf.Table
	TOC       *utf.Table
	ETOC      *utf.Table

	TocOffset     uint64
	TocSize       uint64
	ContentOffset uint64
	EtocOffset    uint64
	EtocSize      uint64

	iDir, iFile, iFSize, iESize, iFOff, iID int

	hContentSizeC, hEtocOffsetC, hEnabledDataC, hEnabledPackedC int
	hContentOffset                                              int
	hFilesC, hTocSizeC, hEtocSizeC                              int

	Members []Member

	// file and fileSize are set only by OpenFile, which parses a CPK from
	// disk without holding its member payloads in memory.
	file     *os.File
	fileSize int64
}

// Parse parses a CPK byte slice with fail-closed checks.
func Parse(data []byte) (*Archive, error) {
	if len(data) < 0x800 || string(data[:4]) != "CPK " {
		return nil, errors.New("bad CPK magic")
	}
	if string(data[0x7FA:0x800]) != "(c)CRI" {
		return nil, errors.New("missing (c)CRI signature")
	}
	hp, err := readPacket(data, 0)
	if err != nil {
		return nil, fmt.Errorf("cpk header: %w", err)
	}
	c, err := newArchive(hp)
	if err != nil {
		return nil, err
	}
	c.Raw = data
	tp, err := readPacket(data, int(c.TocOffset))
	if err != nil {
		return nil, fmt.Errorf("toc: %w", err)
	}
	ep, err := readPacket(data, int(c.EtocOffset))
	if err != nil {
		return nil, fmt.Errorf("etoc: %w", err)
	}
	if err := c.loadTables(tp, ep); err != nil {
		return nil, err
	}
	if err := c.loadMembers(int64(len(data)), func(abs int64, n uint32) ([]byte, error) {
		return append([]byte(nil), data[abs:abs+int64(n)]...), nil
	}, true); err != nil {
		return nil, err
	}
	return c, nil
}

func utfIndex(t *utf.Table, name string) (int, error) {
	i := t.ColIndex(name)
	if i < 0 {
		return 0, fmt.Errorf("missing column %s", name)
	}
	return i, nil
}

// newArchive validates the CPK header packet and the accepted revision.
func newArchive(hp *Packet) (*Archive, error) {
	header, err := utf.Parse(hp.Data)
	if err != nil {
		return nil, fmt.Errorf("cpk header parse: %w", err)
	}
	c := &Archive{HeaderPkt: hp, Header: header}
	idx := utfIndex
	var ciContentSize, ciEtocOffset, ciEnabledData, ciEnabledPacked, ciContentOffset, ciTocOffset, ciTocSize, ciEtocSize, ciTvers, ciVersion, ciRevision, ciAlign, ciCodec, ciEnableFileName, ciSorted, ciCpkMode, ciFiles int
	if ciContentSize, err = idx(header, "ContentSize"); err != nil {
		return nil, err
	}
	if ciEtocOffset, err = idx(header, "EtocOffset"); err != nil {
		return nil, err
	}
	if ciEnabledData, err = idx(header, "EnabledDataSize"); err != nil {
		return nil, err
	}
	if ciEnabledPacked, err = idx(header, "EnabledPackedSize"); err != nil {
		return nil, err
	}
	if ciContentOffset, err = idx(header, "ContentOffset"); err != nil {
		return nil, err
	}
	if ciTocOffset, err = idx(header, "TocOffset"); err != nil {
		return nil, err
	}
	if ciTocSize, err = idx(header, "TocSize"); err != nil {
		return nil, err
	}
	if ciEtocSize, err = idx(header, "EtocSize"); err != nil {
		return nil, err
	}
	if ciTvers, err = idx(header, "Tvers"); err != nil {
		return nil, err
	}
	if ciVersion, err = idx(header, "Version"); err != nil {
		return nil, err
	}
	if ciRevision, err = idx(header, "Revision"); err != nil {
		return nil, err
	}
	if ciAlign, err = idx(header, "Align"); err != nil {
		return nil, err
	}
	if ciCodec, err = idx(header, "Codec"); err != nil {
		return nil, err
	}
	if ciEnableFileName, err = idx(header, "EnableFileName"); err != nil {
		return nil, err
	}
	if ciSorted, err = idx(header, "Sorted"); err != nil {
		return nil, err
	}
	if ciCpkMode, err = idx(header, "CpkMode"); err != nil {
		return nil, err
	}
	if ciFiles, err = idx(header, "Files"); err != nil {
		return nil, err
	}

	version := header.GetU(0, ciVersion)
	revision := header.GetU(0, ciRevision)
	align := header.GetU(0, ciAlign)
	codec := header.GetU(0, ciCodec)
	enableFileName := header.GetU(0, ciEnableFileName)
	sorted := header.GetU(0, ciSorted)
	cpkMode := header.GetU(0, ciCpkMode)
	tvers := string(header.Rows[0][ciTvers].S)
	if version != 7 || revision != 14 || align != Align {
		return nil, fmt.Errorf("unsupported CPK version/rev/align %d/%d/%d", version, revision, align)
	}
	if codec != 0 || enableFileName != 1 || sorted != 1 || cpkMode != 1 {
		return nil, fmt.Errorf("unsupported CPK meta codec=%d fn=%d sorted=%d mode=%d", codec, enableFileName, sorted, cpkMode)
	}
	if tvers != TversExpected {
		return nil, fmt.Errorf("unexpected Tvers %q", tvers)
	}
	c.hContentSizeC, c.hEtocOffsetC, c.hEnabledDataC, c.hEnabledPackedC = ciContentSize, ciEtocOffset, ciEnabledData, ciEnabledPacked
	c.hContentOffset = ciContentOffset
	c.hFilesC, c.hTocSizeC, c.hEtocSizeC = ciFiles, ciTocSize, ciEtocSize

	c.TocOffset = header.GetU(0, ciTocOffset)
	c.TocSize = header.GetU(0, ciTocSize)
	c.ContentOffset = header.GetU(0, ciContentOffset)
	c.EtocOffset = header.GetU(0, ciEtocOffset)
	c.EtocSize = header.GetU(0, ciEtocSize)
	if c.TocOffset == 0 || c.ContentOffset < c.TocOffset || c.EtocOffset <= c.ContentOffset {
		return nil, errors.New("bad region offsets")
	}
	return c, nil
}

// loadTables parses the TOC and ETOC packets and validates their columns.
func (c *Archive) loadTables(tp, ep *Packet) error {
	idx := utfIndex
	c.TOCPkt = tp
	var err error
	if c.TOC, err = utf.Parse(tp.Data); err != nil {
		return fmt.Errorf("toc parse: %w", err)
	}
	if c.iDir, err = idx(c.TOC, "DirName"); err != nil {
		return err
	}
	if c.iFile, err = idx(c.TOC, "FileName"); err != nil {
		return err
	}
	if c.iFSize, err = idx(c.TOC, "FileSize"); err != nil {
		return err
	}
	if c.iESize, err = idx(c.TOC, "ExtractSize"); err != nil {
		return err
	}
	if c.iFOff, err = idx(c.TOC, "FileOffset"); err != nil {
		return err
	}
	if c.iID, err = idx(c.TOC, "ID"); err != nil {
		return err
	}
	files := c.Header.GetU(0, c.hFilesC)
	if uint64(c.TOC.RowCount()) != files {
		return fmt.Errorf("Files=%d but TOC rows=%d", files, c.TOC.RowCount())
	}
	c.EtocPkt = ep
	if c.ETOC, err = utf.Parse(ep.Data); err != nil {
		return fmt.Errorf("etoc parse: %w", err)
	}
	return nil
}

// loadMembers validates every TOC row and either keeps each stored payload
// (keep=true, the in-memory parser) or only its identity (keep=false, the
// on-disk reader that loads payloads on demand).
func (c *Archive) loadMembers(size int64, read func(abs int64, n uint32) ([]byte, error), keep bool) error {
	seen := map[string]bool{}
	type span struct{ a, b uint64 }
	spans := make([]span, 0, c.TOC.RowCount())
	for r := 0; r < c.TOC.RowCount(); r++ {
		dir := c.TOC.Rows[r][c.iDir].S
		file := c.TOC.Rows[r][c.iFile].S
		key := string(dir) + "/" + string(file)
		if seen[key] {
			return fmt.Errorf("duplicate member %s", key)
		}
		seen[key] = true
		fs := uint32(c.TOC.GetU(r, c.iFSize))
		es := uint32(c.TOC.GetU(r, c.iESize))
		fo := c.TOC.GetU(r, c.iFOff)
		id := uint32(c.TOC.GetU(r, c.iID))
		abs := fo + AbsBias
		if abs+uint64(fs) > uint64(size) {
			return fmt.Errorf("member %s out of range", key)
		}
		var stored []byte
		if keep {
			b, err := read(int64(abs), fs)
			if err != nil {
				return fmt.Errorf("member %s: %w", key, err)
			}
			stored = b
		}
		if fs != es {
			var head []byte
			if keep {
				head = stored
			} else {
				b, err := read(int64(abs), 16)
				if err != nil {
					return fmt.Errorf("member %s: %w", key, err)
				}
				head = b
			}
			if len(head) < 16 || string(head[:8]) != "CRILAYLA" {
				return fmt.Errorf("member %s: FileSize!=ExtractSize but not CRILAYLA", key)
			}
			usz := binary.LittleEndian.Uint32(head[8:12])
			dsz := binary.LittleEndian.Uint32(head[12:16])
			if uint64(dsz)+0x110 != uint64(fs) || uint64(usz)+0x100 != uint64(es) {
				return fmt.Errorf("member %s: size relation broken (usz=%d dsz=%d fs=%d es=%d)", key, usz, dsz, fs, es)
			}
		} else if keep && uint64(len(stored)) != uint64(es) {
			return fmt.Errorf("member %s: raw size mismatch", key)
		}
		c.Members = append(c.Members, Member{RowIndex: r, Dir: dir, FileName: file, FileSize: fs, ExtractSize: es, FileOffset: fo, ID: id, Stored: stored})
		spans = append(spans, span{abs, abs + uint64(fs)})
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].a < spans[j].a })
	for i := 1; i < len(spans); i++ {
		if spans[i].a < spans[i-1].b {
			return errors.New("overlapping members")
		}
	}
	return nil
}

// RowCount returns the number of TOC rows.
func (c *Archive) RowCount() int { return len(c.TOC.Rows) }

// MemberByName returns the *Member for "dir/name" (exact match).
func (c *Archive) MemberByName(dir, name string) *Member {
	for i := range c.Members {
		if string(c.Members[i].Dir) == dir && string(c.Members[i].FileName) == name {
			return &c.Members[i]
		}
	}
	return nil
}

// MemberByPath returns the *Member for "dir/name" case-insensitively.
func (c *Archive) MemberByPath(path string) *Member {
	want := strings.ToLower(path)
	for i := range c.Members {
		if PathKey(string(c.Members[i].Dir), string(c.Members[i].FileName)) == want {
			return &c.Members[i]
		}
	}
	return nil
}

// PathKey returns the case-insensitive lookup key for a member path.
func PathKey(dir, name string) string {
	return strings.ToLower(dir) + "/" + strings.ToLower(name)
}

// Logical returns the logical (decompressed) bytes of a member. For an archive
// opened with OpenFile the stored bytes are read on demand, so a multi-gigabyte
// container is never held in memory just to read one member.
func (c *Archive) Logical(m *Member) ([]byte, error) {
	stored, err := c.storedBytes(m)
	if err != nil {
		return nil, err
	}
	if m.FileSize == m.ExtractSize {
		return append([]byte(nil), stored...), nil
	}
	return crilayla.Decode(stored)
}

// storedBytes returns a member's stored payload, from memory for Parse archives
// and from disk for OpenFile archives.
func (c *Archive) storedBytes(m *Member) ([]byte, error) {
	if m.Stored != nil || m.FileSize == 0 {
		return m.Stored, nil
	}
	if c.file == nil {
		return nil, fmt.Errorf("cpk: member %s/%s has no payload", m.Dir, m.FileName)
	}
	buf := make([]byte, m.FileSize)
	if _, err := c.file.ReadAt(buf, int64(m.FileOffset)+AbsBias); err != nil {
		return nil, fmt.Errorf("cpk: read %s/%s: %w", m.Dir, m.FileName, err)
	}
	return buf, nil
}

// MemberEdit replaces one member's stored payload.
type MemberEdit struct {
	RowIndex    int
	Payload     []byte // stored bytes (CRILAYLA block or raw)
	ExtractSize uint32
}

func alignUp(n, a uint64) uint64 {
	if a == 0 {
		return n
	}
	return (n + a - 1) / a * a
}

// Build rebuilds the container. With no edits this reproduces the input
// byte-for-byte. Member payloads are copied byte-identically unless explicitly
// edited.
func (c *Archive) Build(edits []MemberEdit) ([]byte, error) {
	return c.BuildLayout(edits, nil)
}

// BuildLayout is Build plus an optional pure-gap layout displacement. gapAfter maps
// a TOC row index to extra alignment padding inserted immediately after that member.
// Every existing member payload stays byte-identical; only the offsets of later
// members and the four derived header fields move. Each gap must be a multiple of
// Align (diagnostic use only, never with edits).
func (c *Archive) BuildLayout(edits []MemberEdit, gapAfter map[int]uint64) ([]byte, error) {
	if c.Raw == nil {
		return nil, errors.New("cpk: cannot rebuild an archive opened from disk; parse its bytes first")
	}
	editByRow := map[int]MemberEdit{}
	for _, e := range edits {
		if e.RowIndex < 0 || e.RowIndex >= c.RowCount() {
			return nil, fmt.Errorf("edit row %d out of range", e.RowIndex)
		}
		if _, dup := editByRow[e.RowIndex]; dup {
			return nil, fmt.Errorf("duplicate edit for row %d", e.RowIndex)
		}
		editByRow[e.RowIndex] = e
	}

	order := make([]int, len(c.Members))
	for i := range c.Members {
		order[i] = i
	}
	sort.Slice(order, func(a, b int) bool {
		ma, mb := c.Members[order[a]], c.Members[order[b]]
		if ma.FileOffset != mb.FileOffset {
			return ma.FileOffset < mb.FileOffset
		}
		return ma.ID < mb.ID
	})

	payloads := make([][]byte, len(c.Members))
	fsizes := make([]uint32, len(c.Members))
	esizes := make([]uint32, len(c.Members))
	newOffset := make([]uint64, len(c.Members))
	for i := range c.Members {
		payloads[i] = c.Members[i].Stored
		fsizes[i] = c.Members[i].FileSize
		esizes[i] = c.Members[i].ExtractSize
	}
	for row, e := range editByRow {
		mi := -1
		for i := range c.Members {
			if c.Members[i].RowIndex == row {
				mi = i
				break
			}
		}
		if mi < 0 {
			return nil, fmt.Errorf("edit row %d has no member", row)
		}
		payloads[mi] = e.Payload
		fsizes[mi] = uint32(len(e.Payload))
		esizes[mi] = e.ExtractSize
	}

	abs := c.ContentOffset
	for _, mi := range order {
		abs = alignUp(abs, Align)
		newOffset[mi] = abs - AbsBias
		abs += uint64(len(payloads[mi]))
		if gapAfter != nil {
			if g, ok := gapAfter[c.Members[mi].RowIndex]; ok {
				if g == 0 || g%Align != 0 {
					return nil, fmt.Errorf("layout gap %d for row %d is not a positive multiple of %d", g, c.Members[mi].RowIndex, Align)
				}
				abs += g
			}
		}
	}
	etocOffset := alignUp(abs, Align)

	var sumF, sumE uint64
	for i := range c.Members {
		r := c.Members[i].RowIndex
		c.TOC.Rows[r][c.iFSize].U = uint64(fsizes[i])
		c.TOC.Rows[r][c.iESize].U = uint64(esizes[i])
		c.TOC.Rows[r][c.iFOff].U = newOffset[i]
		sumF += uint64(fsizes[i])
		sumE += uint64(esizes[i])
	}
	contentSize := etocOffset - c.ContentOffset
	c.Header.Rows[0][c.hContentSizeC].U = contentSize
	c.Header.Rows[0][c.hEtocOffsetC].U = etocOffset
	c.Header.Rows[0][c.hEnabledDataC].U = sumF
	c.Header.Rows[0][c.hEnabledPackedC].U = sumE
	if c.Header.Rows[0][c.hContentOffset].U != c.ContentOffset {
		return nil, errors.New("ContentOffset drifted")
	}

	headerBytes, err := utf.Build(c.Header)
	if err != nil {
		return nil, err
	}
	tocBytes, err := utf.Build(c.TOC)
	if err != nil {
		return nil, err
	}
	etocBytes, err := utf.Build(c.ETOC)
	if err != nil {
		return nil, err
	}
	if uint64(len(tocBytes))+16 != c.TocSize {
		return nil, fmt.Errorf("TOC size changed: %d+16 != TocSize %d", len(tocBytes), c.TocSize)
	}
	if uint64(len(etocBytes))+16 != c.EtocSize {
		return nil, fmt.Errorf("ETOC size changed: %d+16 != EtocSize %d", len(etocBytes), c.EtocSize)
	}

	total := etocOffset + uint64(len(etocBytes)) + 16
	out := make([]byte, total)
	copy(out[0:], c.Raw[:c.TocOffset]) // header region template (preserves (c)CRI, zeros)
	hp := marshalPacket(c.HeaderPkt.Tag, c.HeaderPkt.Unk, headerBytes)
	copy(out[0:], hp)
	tp := marshalPacket(c.TOCPkt.Tag, c.TOCPkt.Unk, tocBytes)
	copy(out[c.TocOffset:], tp)
	for i := range c.Members {
		off := newOffset[i] + AbsBias
		copy(out[off:], payloads[i])
	}
	ep := marshalPacket(c.EtocPkt.Tag, c.EtocPkt.Unk, etocBytes)
	copy(out[etocOffset:], ep)
	return out, nil
}

// MemberAdd is a NEW member appended by BuildWithAdd.
type MemberAdd struct {
	Dir         string
	Name        string
	ID          uint32
	Stored      []byte // CRILAYLA block (FileSize != ExtractSize) or raw bytes
	ExtractSize uint32
}

func cloneUTFValue(v utf.Value) utf.Value {
	c := v
	if v.S != nil {
		c.S = append([]byte(nil), v.S...)
	}
	if v.Blob != nil {
		c.Blob = append([]byte(nil), v.Blob...)
	}
	return c
}

// BuildWithAdd rebuilds the container with edits applied to existing members and
// one or more NEW members inserted.
//
// Contracts:
//   - every existing member keeps its stored payload; with no existing member
//     edited, every existing FileOffset is preserved exactly (fail-closed);
//   - new members are appended after the last existing payload (2048-aligned);
//   - the TOC is kept in its path-sorted order and the per-file ETOC mirrors the
//     TOC row order (the trailing ETOC sentinel row stays last); new rows are
//     inserted at the correct sorted position;
//   - TocSize / EtocSize / EtocOffset / ContentSize / Files / EnabledDataSize /
//     EnabledPackedSize are recomputed; version / revision / align / Tvers are
//     untouched.
//
// Deterministic and fail-closed.
func (c *Archive) BuildWithAdd(edits []MemberEdit, adds []MemberAdd) ([]byte, error) {
	if c.Raw == nil {
		return nil, errors.New("cpk: cannot rebuild an archive opened from disk; parse its bytes first")
	}
	if len(adds) == 0 {
		return nil, errors.New("BuildWithAdd called with no additions")
	}

	editByRow := map[int]MemberEdit{}
	for _, e := range edits {
		if e.RowIndex < 0 || e.RowIndex >= c.RowCount() {
			return nil, fmt.Errorf("edit row %d out of range", e.RowIndex)
		}
		if _, dup := editByRow[e.RowIndex]; dup {
			return nil, fmt.Errorf("duplicate edit for row %d", e.RowIndex)
		}
		editByRow[e.RowIndex] = e
	}

	seenPath := map[string]bool{}
	seenID := map[uint32]bool{}
	for i := range c.Members {
		seenPath[PathKey(string(c.Members[i].Dir), string(c.Members[i].FileName))] = true
		seenID[c.Members[i].ID] = true
	}
	for i := range adds {
		a := &adds[i]
		if a.Dir == "" || a.Name == "" {
			return nil, errors.New("add member: empty dir or name")
		}
		key := PathKey(a.Dir, a.Name)
		if seenPath[key] {
			return nil, fmt.Errorf("add member %s already exists in the archive", key)
		}
		seenPath[key] = true
		if seenID[a.ID] {
			return nil, fmt.Errorf("add member %s: ID %d already used", key, a.ID)
		}
		seenID[a.ID] = true
		if len(a.Stored) >= 0x10 && string(a.Stored[:8]) == "CRILAYLA" {
			usz := binary.LittleEndian.Uint32(a.Stored[8:12])
			dsz := binary.LittleEndian.Uint32(a.Stored[12:16])
			if uint64(dsz)+0x110 != uint64(len(a.Stored)) {
				return nil, fmt.Errorf("add member %s: dsz+0x110 != stored len (%d+272 != %d)", key, dsz, len(a.Stored))
			}
			if uint64(usz)+0x100 != uint64(a.ExtractSize) {
				return nil, fmt.Errorf("add member %s: usz+0x100 != ExtractSize (%d+256 != %d)", key, usz, a.ExtractSize)
			}
		} else if uint64(len(a.Stored)) != uint64(a.ExtractSize) {
			return nil, fmt.Errorf("add member %s: raw stored len %d != ExtractSize %d", key, len(a.Stored), a.ExtractSize)
		}
	}

	var prev string
	for r := 0; r < c.TOC.RowCount(); r++ {
		k := PathKey(string(c.TOC.Rows[r][c.iDir].S), string(c.TOC.Rows[r][c.iFile].S))
		if r > 0 && k < prev {
			return nil, fmt.Errorf("TOC is not path-sorted (row %d: %q < %q)", r, k, prev)
		}
		prev = k
	}
	if c.ETOC.RowCount() != c.TOC.RowCount()+1 {
		return nil, fmt.Errorf("ETOC rows %d != TOC rows %d + 1 (sentinel)", c.ETOC.RowCount(), c.TOC.RowCount())
	}
	if len(c.ETOC.Cols) < 2 || string(c.ETOC.Cols[1].Name) != "LocalDir" {
		return nil, errors.New("ETOC column 1 is not LocalDir; add-member mirroring unsupported")
	}

	order := make([]int, len(c.Members))
	for i := range c.Members {
		order[i] = i
	}
	sort.Slice(order, func(x, y int) bool {
		ma, mb := c.Members[order[x]], c.Members[order[y]]
		if ma.FileOffset != mb.FileOffset {
			return ma.FileOffset < mb.FileOffset
		}
		return ma.ID < mb.ID
	})
	payloads := make([][]byte, len(c.Members))
	fsizes := make([]uint32, len(c.Members))
	esizes := make([]uint32, len(c.Members))
	newOffset := make([]uint64, len(c.Members))
	for i := range c.Members {
		payloads[i] = c.Members[i].Stored
		fsizes[i] = c.Members[i].FileSize
		esizes[i] = c.Members[i].ExtractSize
	}
	for row, e := range editByRow {
		mi := -1
		for i := range c.Members {
			if c.Members[i].RowIndex == row {
				mi = i
				break
			}
		}
		if mi < 0 {
			return nil, fmt.Errorf("edit row %d has no member", row)
		}
		payloads[mi] = e.Payload
		fsizes[mi] = uint32(len(e.Payload))
		esizes[mi] = e.ExtractSize
	}
	abs := c.ContentOffset
	for _, mi := range order {
		abs = alignUp(abs, Align)
		newOffset[mi] = abs - AbsBias
		abs += uint64(len(payloads[mi]))
	}
	if len(editByRow) == 0 {
		for i := range c.Members {
			if newOffset[i] != c.Members[i].FileOffset {
				return nil, fmt.Errorf("offset preservation violated for %s/%s: %d -> %d",
					c.Members[i].Dir, c.Members[i].FileName, c.Members[i].FileOffset, newOffset[i])
			}
		}
	}
	addOffset := make([]uint64, len(adds))
	for i := range adds {
		abs = alignUp(abs, Align)
		addOffset[i] = abs - AbsBias
		abs += uint64(len(adds[i].Stored))
	}
	etocOffset := alignUp(abs, Align)

	var sumF, sumE uint64
	for i := range c.Members {
		r := c.Members[i].RowIndex
		c.TOC.Rows[r][c.iFSize].U = uint64(fsizes[i])
		c.TOC.Rows[r][c.iESize].U = uint64(esizes[i])
		c.TOC.Rows[r][c.iFOff].U = newOffset[i]
		sumF += uint64(fsizes[i])
		sumE += uint64(esizes[i])
	}

	type addAt struct {
		key string
		idx int
	}
	at := make([]addAt, len(adds))
	for i := range adds {
		at[i] = addAt{PathKey(adds[i].Dir, adds[i].Name), i}
	}
	sort.SliceStable(at, func(x, y int) bool { return at[x].key < at[y].key })
	for _, a := range at {
		add := adds[a.idx]
		rows := c.TOC.Rows
		ins := 0
		for ins < len(rows) && PathKey(string(rows[ins][c.iDir].S), string(rows[ins][c.iFile].S)) < a.key {
			ins++
		}
		nr := make([]utf.Value, len(rows[0]))
		for ci := range rows[0] {
			nr[ci] = cloneUTFValue(rows[0][ci])
		}
		nr[c.iDir] = utf.Value{Type: utf.TyStr, S: []byte(add.Dir)}
		nr[c.iFile] = utf.Value{Type: utf.TyStr, S: []byte(add.Name)}
		nr[c.iFSize] = utf.Value{Type: utf.TyU32, U: uint64(len(add.Stored))}
		nr[c.iESize] = utf.Value{Type: utf.TyU32, U: uint64(add.ExtractSize)}
		nr[c.iFOff] = utf.Value{Type: utf.TyU64, U: addOffset[a.idx]}
		nr[c.iID] = utf.Value{Type: utf.TyU32, U: uint64(add.ID)}
		rows = append(rows, nil)
		copy(rows[ins+1:], rows[ins:])
		rows[ins] = nr
		c.TOC.Rows = rows

		er := c.ETOC.Rows
		tmpl := make([]utf.Value, len(er[ins])) // keeps UpdateDateTime; column 1 = LocalDir
		for j := range er[ins] {
			tmpl[j] = cloneUTFValue(er[ins][j])
		}
		tmpl[1].S = []byte(add.Dir)
		er = append(er, nil)
		copy(er[ins+1:], er[ins:])
		er[ins] = tmpl
		c.ETOC.Rows = er
		sumF += uint64(len(add.Stored))
		sumE += uint64(add.ExtractSize)
	}

	tocBytes, err := utf.Build(c.TOC)
	if err != nil {
		return nil, err
	}
	etocBytes, err := utf.Build(c.ETOC)
	if err != nil {
		return nil, err
	}
	if uint64(len(tocBytes))+16 > c.ContentOffset-c.TocOffset {
		return nil, fmt.Errorf("TOC packet grew to %d, does not fit before ContentOffset %d", len(tocBytes)+16, c.ContentOffset)
	}
	if c.Header.Rows[0][c.hContentOffset].U != c.ContentOffset {
		return nil, errors.New("ContentOffset drifted")
	}
	c.Header.Rows[0][c.hContentSizeC].U = etocOffset - c.ContentOffset
	c.Header.Rows[0][c.hEtocOffsetC].U = etocOffset
	c.Header.Rows[0][c.hEnabledDataC].U = sumF
	c.Header.Rows[0][c.hEnabledPackedC].U = sumE
	c.Header.Rows[0][c.hFilesC].U = uint64(c.TOC.RowCount())
	c.Header.Rows[0][c.hTocSizeC].U = uint64(len(tocBytes)) + 16
	c.Header.Rows[0][c.hEtocSizeC].U = uint64(len(etocBytes)) + 16
	headerBytes, err := utf.Build(c.Header)
	if err != nil {
		return nil, err
	}

	total := etocOffset + uint64(len(etocBytes)) + 16
	out := make([]byte, total)
	copy(out[0:], c.Raw[:c.TocOffset]) // header region template (preserves (c)CRI, zeros)
	copy(out[0:], marshalPacket(c.HeaderPkt.Tag, c.HeaderPkt.Unk, headerBytes))
	copy(out[c.TocOffset:], marshalPacket(c.TOCPkt.Tag, c.TOCPkt.Unk, tocBytes))
	for i := range c.Members {
		copy(out[newOffset[i]+AbsBias:], payloads[i])
	}
	for i := range adds {
		copy(out[addOffset[i]+AbsBias:], adds[i].Stored)
	}
	copy(out[etocOffset:], marshalPacket(c.EtocPkt.Tag, c.EtocPkt.Unk, etocBytes))
	return out, nil
}
