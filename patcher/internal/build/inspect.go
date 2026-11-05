package build

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"13s-patcher/internal/cpk"
	"13s-patcher/internal/hashutil"
)

// ToolName is the executable/tool name.
const ToolName = "13s-patcher"

// ToolVersion is the application version. It can be overridden at build time with
//
//	-ldflags "-X 13s-patcher/internal/build.ToolVersion=x.y.z"
var (
	ToolVersion = "0.1.0"
	// Commit and BuildDate are optional build metadata.
	Commit    = ""
	BuildDate = ""
)

// VersionString renders the full version line.
func VersionString() string {
	v := ToolVersion
	if Commit != "" {
		v += " (" + Commit + ")"
	}
	if BuildDate != "" {
		v += " " + BuildDate
	}
	return v
}

// InspectHeader summarises an archive header.
type InspectHeader struct {
	Tvers             string `json:"tvers"`
	Version           int    `json:"version"`
	Revision          int    `json:"revision"`
	Align             int    `json:"align"`
	Files             int    `json:"files"`
	TocOffset         int64  `json:"toc_offset"`
	ContentOffset     int64  `json:"content_offset"`
	EtocOffset        int64  `json:"etoc_offset"`
	ContentSize       int64  `json:"content_size"`
	EnabledDataSize   int64  `json:"enabled_data_size"`
	EnabledPackedSize int64  `json:"enabled_packed_size"`
}

func u64At(t *cpk.Archive, name string) uint64 {
	if t == nil || t.Header == nil {
		return 0
	}
	i := t.Header.ColIndex(name)
	if i < 0 || len(t.Header.Rows) == 0 {
		return 0
	}
	return t.Header.Rows[0][i].U
}

func strAt(t *cpk.Archive, name string) string {
	if t == nil || t.Header == nil {
		return ""
	}
	i := t.Header.ColIndex(name)
	if i < 0 || len(t.Header.Rows) == 0 {
		return ""
	}
	return string(t.Header.Rows[0][i].S)
}

func headerOf(c *cpk.Archive) InspectHeader {
	return InspectHeader{
		Tvers:           strAt(c, "Tvers"),
		Version:         int(u64At(c, "Version")),
		Revision:        int(u64At(c, "Revision")),
		Align:           int(u64At(c, "Align")),
		Files:           int(u64At(c, "Files")),
		TocOffset:       int64(u64At(c, "TocOffset")),
		ContentOffset:   int64(u64At(c, "ContentOffset")),
		EtocOffset:      int64(u64At(c, "EtocOffset")),
		ContentSize:     int64(u64At(c, "ContentSize")),
		EnabledDataSize: int64(u64At(c, "EnabledDataSize")),

		EnabledPackedSize: int64(u64At(c, "EnabledPackedSize")),
	}
}

// InspectionMember is one member in a reconstructed inspection.
type InspectionMember struct {
	ID            int64  `json:"id"`
	Path          string `json:"path"`
	StoredSize    int    `json:"stored_size"`
	ExtractSize   int    `json:"extract_size"`
	Offset        int64  `json:"file_offset"`
	Aligned       bool   `json:"aligned"`
	Compressed    bool   `json:"compressed"`
	StoredSHA256  string `json:"stored_sha256"`
	LogicalSHA256 string `json:"logical_sha256,omitempty"`
	DecodeError   string `json:"decode_error,omitempty"`
}

// Inspection is a full reconstruct of one archive.
type Inspection struct {
	Path            string             `json:"path"`
	SHA256          string             `json:"sha256"`
	Size            int64              `json:"size"`
	Header          InspectHeader      `json:"header"`
	Members         []InspectionMember `json:"members"`
	MemberCount     int                `json:"member_count"`
	CompressedCount int                `json:"compressed_members"`
	Checks          []Check            `json:"checks"`
	FailedChecks    []string           `json:"failed_checks,omitempty"`
	AllChecksOK     bool               `json:"all_checks_ok"`
}

// Inspect parses an archive file and reconstructs its inspection.
func Inspect(path string) (*Inspection, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	ins, err := InspectBytes(raw)
	if err != nil {
		return nil, err
	}
	ins.Path = path
	return ins, nil
}

// InspectBytes reconstructs an inspection from archive bytes.
func InspectBytes(raw []byte) (*Inspection, error) {
	c, err := cpk.Parse(raw)
	if err != nil {
		return nil, err
	}
	ins := &Inspection{SHA256: hashutil.Bytes(raw), Size: int64(len(raw))}
	ins.Header = headerOf(c)
	for i := range c.Members {
		m := c.Members[i]
		path := string(m.Dir) + "/" + string(m.FileName)
		im := InspectionMember{
			ID: int64(m.ID), Path: path, StoredSize: int(m.FileSize), ExtractSize: int(m.ExtractSize),
			Offset: int64(m.FileOffset), Aligned: (m.FileOffset+cpk.AbsBias)%cpk.Align == 0,
			Compressed: m.FileSize != m.ExtractSize, StoredSHA256: hashutil.Bytes(m.Stored),
		}
		if lg, err := c.Logical(&c.Members[i]); err == nil {
			im.LogicalSHA256 = hashutil.Bytes(lg)
		} else {
			im.DecodeError = err.Error()
		}
		if im.Compressed {
			ins.CompressedCount++
		}
		ins.Members = append(ins.Members, im)
	}
	ins.MemberCount = len(ins.Members)
	h := ins.Header
	add := func(id, title string, ok bool, detail string) {
		if !ok {
			ins.FailedChecks = append(ins.FailedChecks, id)
		}
		ins.Checks = append(ins.Checks, Check{ID: id, Title: title, Pass: ok, Detail: detail})
	}
	sortedOK := true
	for i := 1; i < len(ins.Members); i++ {
		if strings.ToLower(ins.Members[i].Path) < strings.ToLower(ins.Members[i-1].Path) {
			sortedOK = false
		}
	}
	alignedOK := true
	decoded := ""
	for _, m := range ins.Members {
		if !m.Aligned {
			alignedOK = false
		}
		if m.DecodeError != "" {
			decoded += m.Path + " "
		}
	}
	add("inspect.files", "Files == member count", h.Files == ins.MemberCount, fmt.Sprintf("%d / %d", h.Files, ins.MemberCount))
	add("inspect.alignment", "every member aligned", alignedOK, "")
	add("inspect.sorted", "members path-sorted", sortedOK, "")
	add("inspect.decode", "every member decodes", decoded == "", decoded)
	ins.AllChecksOK = len(ins.FailedChecks) == 0
	return ins, nil
}

// Diff is a deterministic classification of two inspections.
type Diff struct {
	From      string   `json:"from"`
	To        string   `json:"to"`
	Changed   []string `json:"changed_members"`
	Added     []string `json:"added_members"`
	Removed   []string `json:"removed_members"`
	Moved     []string `json:"moved_members"`
	Preserved int      `json:"preserved_members"`
	AllIntact bool     `json:"no_member_lost_or_mutated"`
}

// Compare classifies a -> b.
func Compare(a, b *Inspection) *Diff {
	d := &Diff{From: a.Path, To: b.Path, AllIntact: true}
	am, bm := map[string]InspectionMember{}, map[string]InspectionMember{}
	for _, m := range a.Members {
		am[strings.ToLower(m.Path)] = m
	}
	for _, m := range b.Members {
		bm[strings.ToLower(m.Path)] = m
	}
	for k, x := range am {
		y, ok := bm[k]
		if !ok {
			d.Removed = append(d.Removed, x.Path)
			d.AllIntact = false
			continue
		}
		if x.StoredSHA256 != y.StoredSHA256 {
			d.Changed = append(d.Changed, y.Path)
		} else {
			d.Preserved++
			if x.Offset != y.Offset || x.ID != y.ID {
				d.Moved = append(d.Moved, y.Path)
			}
		}
	}
	for k, y := range bm {
		if _, ok := am[k]; !ok {
			d.Added = append(d.Added, y.Path)
		}
	}
	sort.Strings(d.Changed)
	sort.Strings(d.Added)
	sort.Strings(d.Removed)
	sort.Strings(d.Moved)
	return d
}
