package localsources_test

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"13s-patcher/internal/hashutil"
	"13s-patcher/internal/localsources"
)

// buildPFS0 assembles a minimal PFS0 container with the given entries.
func buildPFS0(t *testing.T, entries map[string][]byte) []byte {
	t.Helper()
	names := make([]string, 0, len(entries))
	for n := range entries {
		names = append(names, n)
	}
	// deterministic order
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j] < names[j-1]; j-- {
			names[j], names[j-1] = names[j-1], names[j]
		}
	}
	var strTab bytes.Buffer
	nameOff := map[string]uint32{}
	for _, n := range names {
		nameOff[n] = uint32(strTab.Len())
		strTab.WriteString(n)
		strTab.WriteByte(0)
	}
	header := make([]byte, 0x10+len(names)*0x18)
	copy(header[0:4], "PFS0")
	binary.LittleEndian.PutUint32(header[4:8], uint32(len(names)))
	binary.LittleEndian.PutUint32(header[8:12], uint32(strTab.Len()))
	dataStart := 0x10 + len(names)*0x18 + strTab.Len()
	if rem := dataStart % 0x20; rem != 0 {
		dataStart += 0x20 - rem
	}
	var data bytes.Buffer
	offset := 0
	for i, n := range names {
		binary.LittleEndian.PutUint64(header[0x10+i*0x18:], uint64(offset))
		binary.LittleEndian.PutUint64(header[0x10+i*0x18+8:], uint64(len(entries[n])))
		binary.LittleEndian.PutUint32(header[0x10+i*0x18+16:], nameOff[n])
		data.Write(entries[n])
		offset += len(entries[n])
	}
	out := make([]byte, dataStart+data.Len())
	copy(out, header)
	copy(out[0x10+len(names)*0x18:], strTab.Bytes())
	copy(out[dataStart:], data.Bytes())
	return out
}

func TestParsePFS0ListsEntries(t *testing.T) {
	raw := buildPFS0(t, map[string][]byte{
		"a.nca":      []byte("nca"),
		"b.cnmt.nca": []byte("cnmt"),
	})
	pfs, err := localsources.ParsePFS0(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if pfs.FileCount != 2 || len(pfs.Files) != 2 {
		t.Fatalf("unexpected header: %+v", pfs)
	}
	e := pfs.Entry("a.nca")
	if e == nil {
		t.Fatal("entry a.nca not found")
	}
	got, err := pfs.ReadEntry(bytes.NewReader(raw), e)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "nca" {
		t.Fatalf("entry payload = %q", got)
	}
}

func TestParsePFS0RejectsForeignFile(t *testing.T) {
	if _, err := localsources.ParsePFS0(bytes.NewReader([]byte("not a container at all"))); err == nil {
		t.Fatal("a foreign file must be rejected")
	}
}

func TestTitleHintFromTicketEntry(t *testing.T) {
	rights := "01003FC01670C000" + "0000000000000000"
	raw := buildPFS0(t, map[string][]byte{
		"00000000000000000000000000000000.cnmt.nca": []byte("cnmt"),
		rights + ".tik": []byte("ticket"),
	})
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "game.nsp"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := localsources.Discover(localsources.Request{Dirs: []string{dir}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Found) != 1 {
		t.Fatalf("expected one candidate, got %+v", rep.Found)
	}
	a := rep.Found[0]
	if a.Identified {
		t.Fatal("a synthetic container must not be accepted as a known input")
	}
	if a.TitleID != "01003FC01670C000" {
		t.Fatalf("title hint = %q", a.TitleID)
	}
	if len(rep.Missing) != 2 {
		t.Fatalf("expected both required inputs missing, got %v", rep.Missing)
	}
}

func TestDiscoverIgnoresNonContainerFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignore me"), 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := localsources.Discover(localsources.Request{Dirs: []string{dir}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Found) != 0 {
		t.Fatalf("non-container files must be ignored: %+v", rep.Found)
	}
}

func TestDiscoverExplicitMissingPathIsReported(t *testing.T) {
	dir := t.TempDir()
	rep, err := localsources.Discover(localsources.Request{
		Explicit: map[string]string{"base": filepath.Join(dir, "absent.nsp")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Found) != 0 || len(rep.Notes) == 0 {
		t.Fatalf("a missing configured path must be reported: %+v", rep)
	}
}

func TestFindByHash(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "nested", "deeper")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	payload := []byte("payload-to-find")
	target := filepath.Join(sub, "something.bin")
	if err := os.WriteFile(target, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	// a same-size but different file must be rejected
	if err := os.WriteFile(filepath.Join(dir, "decoy.bin"), []byte("payload-to-find"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, ok := localsources.FindByHash([]string{dir}, int64(len(payload)), hashutil.Bytes(payload))
	if !ok {
		t.Fatal("expected to find the file by hash")
	}
	if got != target && got != filepath.Join(dir, "decoy.bin") {
		t.Fatalf("unexpected path %q", got)
	}
	if _, ok := localsources.FindByHash([]string{dir}, int64(len(payload)), "00"); ok {
		t.Fatal("a wrong digest must not match")
	}
}
