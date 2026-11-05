package cpk

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
)

// OpenFile parses a CPK stored on disk without loading its member payloads into
// memory. The TOC and ETOC are read once; a member's bytes are read only when
// Logical asks for them. This keeps a multi-gigabyte game archive usable as a
// read-only source on a machine that could not hold it twice.
//
// The accepted-revision checks are exactly those Parse applies. Rebuilding an
// archive opened this way is refused: Build needs the complete header region.
func OpenFile(path string) (*Archive, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			f.Close()
		}
	}()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := fi.Size()
	if size < 0x800 {
		return nil, errors.New("bad CPK magic")
	}
	head := make([]byte, 0x800)
	if _, err := f.ReadAt(head, 0); err != nil {
		return nil, fmt.Errorf("cpk header: %w", err)
	}
	if string(head[:4]) != "CPK " {
		return nil, errors.New("bad CPK magic")
	}
	if string(head[0x7FA:0x800]) != "(c)CRI" {
		return nil, errors.New("missing (c)CRI signature")
	}
	hp, err := readPacketAt(f, 0, size)
	if err != nil {
		return nil, fmt.Errorf("cpk header: %w", err)
	}
	c, err := newArchive(hp)
	if err != nil {
		return nil, err
	}
	c.file = f
	c.fileSize = size
	tp, err := readPacketAt(f, int64(c.TocOffset), size)
	if err != nil {
		return nil, fmt.Errorf("toc: %w", err)
	}
	ep, err := readPacketAt(f, int64(c.EtocOffset), size)
	if err != nil {
		return nil, fmt.Errorf("etoc: %w", err)
	}
	if err := c.loadTables(tp, ep); err != nil {
		return nil, err
	}
	if err := c.loadMembers(size, func(abs int64, n uint32) ([]byte, error) {
		return readAt(f, abs, int64(n), size)
	}, false); err != nil {
		return nil, err
	}
	ok = true
	return c, nil
}

// Close releases the file handle of an archive opened with OpenFile.
func (c *Archive) Close() error {
	if c.file == nil {
		return nil
	}
	err := c.file.Close()
	c.file = nil
	return err
}

// readPacketAt reads one outer CPK packet at an absolute file offset.
func readPacketAt(f *os.File, off, size int64) (*Packet, error) {
	if off < 0 || off+16 > size {
		return nil, errors.New("packet offset out of range")
	}
	hdr := make([]byte, 16)
	if _, err := f.ReadAt(hdr, off); err != nil {
		return nil, err
	}
	n := binary.LittleEndian.Uint64(hdr[8:16])
	if n > uint64(size-off-16) {
		return nil, fmt.Errorf("packet size %d out of range", n)
	}
	buf := make([]byte, 16+int(n))
	if _, err := f.ReadAt(buf, off); err != nil {
		return nil, err
	}
	return readPacket(buf, 0)
}

// readAt reads exactly n bytes at an absolute offset, refusing a range that
// leaves the file.
func readAt(f *os.File, off, n, size int64) ([]byte, error) {
	if off < 0 || n < 0 || off+n > size {
		return nil, errors.New("read out of range")
	}
	buf := make([]byte, n)
	if n == 0 {
		return buf, nil
	}
	if _, err := f.ReadAt(buf, off); err != nil {
		return nil, err
	}
	return buf, nil
}
