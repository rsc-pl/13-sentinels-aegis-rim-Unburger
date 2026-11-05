// Package packaging turns one verified patch (a rebuilt romfs archive plus an
// executable patch) into the per-target archives a user installs.
//
// One build produces one common result; packaging only changes the directory
// layout and the metadata each loader expects. Both targets therefore carry the
// same modifications — identical archive bytes and identical patch
// bytes — arranged for the loader that consumes them.
package packaging

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"13s-patcher/internal/atomic"
	"13s-patcher/internal/hashutil"
)

// Target names one installation layout.
type Target string

// Supported targets.
const (
	// TargetAtmosphere lays the files out for the Atmosphère custom firmware:
	// the archive under a per-title content directory and the executable patch
	// under the exefs patch directory.
	TargetAtmosphere Target = "atmosphere"
	// TargetEmulator lays the files out as a single mod folder for emulators
	// that read a mod directory with romfs/ and exefs/ subdirectories.
	TargetEmulator Target = "emulator"
	// TargetBoth produces both archives from the same patched bytes.
	TargetBoth Target = "both"
)

// ParseTarget validates a target name.
func ParseTarget(s string) (Target, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", string(TargetBoth):
		return TargetBoth, nil
	case string(TargetAtmosphere), "atmos", "cfw":
		return TargetAtmosphere, nil
	case string(TargetEmulator), "emulators", "emu", "eden", "yuzu":
		return TargetEmulator, nil
	default:
		return "", fmt.Errorf("unknown target %q (want atmosphere, emulator or both)", s)
	}
}

// Targets expands a target into the concrete set to produce.
func (t Target) Targets() []Target {
	if t == TargetBoth {
		return []Target{TargetAtmosphere, TargetEmulator}
	}
	return []Target{t}
}

// Label is the human-readable name.
func (t Target) Label() string {
	switch t {
	case TargetAtmosphere:
		return "Atmosphère"
	case TargetEmulator:
		return "Emulators"
	default:
		return string(t)
	}
}

// Layout constants.
const (
	// ModFolder is the mod directory name emulators read. It is portable: the
	// game title with its colon stripped, plus the mod's public name, and no
	// character that is invalid on a common file system.
	ModFolder = "13 Sentinels Aegis Rim - Unburger Mod"
	// AtmosphereRoot is the root directory Atmosphère archives carry.
	AtmosphereRoot = "atmosphere"
	// ContentsDir is the per-title content directory under AtmosphereRoot.
	ContentsDir = "contents"
	// AtmospherePatchDir is the ExeFS patch directory for the executable patch.
	// Atmosphère reads every subdirectory of exefs_patches and applies the
	// <build id>.ips it contains, so the directory name is free to be branded.
	AtmospherePatchDir = "exefs_patches/UnburgerMod"
	// RomfsArchiveName is the effective-update archive name the game loads.
	RomfsArchiveName = "ROBO_PATCH_US.CPK"
)

// Request describes one packaging run.
type Request struct {
	// ArchiveBytes is the rebuilt romfs archive.
	ArchiveBytes []byte
	// PatchBytes is the executable patch (IPS container).
	PatchBytes []byte
	// BuildID is the program build identifier; the file name is its upper case.
	BuildID string
	// TitleID is the base application id.
	TitleID string
	// Release is the public release label used in file names. Never a private
	// or pre-release identifier.
	Release string
	// OutputDir receives the archives.
	OutputDir string
	// Overwrite allows replacing existing archives.
	Overwrite bool
}

// Entry is one packaged file.
type Entry struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Artifact is one produced archive.
type Artifact struct {
	Target    Target  `json:"target"`
	Label     string  `json:"label"`
	Path      string  `json:"path"`
	Size      int64   `json:"size"`
	SHA256    string  `json:"sha256"`
	Entries   []Entry `json:"entries"`
	Validated bool    `json:"validated"`
}

// Report is the packaging result.
type Report struct {
	TitleID     string     `json:"title_id"`
	BuildID     string     `json:"build_id"`
	Release     string     `json:"release,omitempty"`
	Target      Target     `json:"target"`
	Archive     InputRef   `json:"romfs_archive"`
	Patch       InputRef   `json:"executable_patch"`
	Artifacts   []Artifact `json:"artifacts"`
	Checks      []Check    `json:"checks,omitempty"`
	Determinism bool       `json:"deterministic_rebuild"`
}

// InputRef identifies an input by digest.
type InputRef struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Check is one validation result.
type Check struct {
	Target string `json:"target"`
	ID     string `json:"id"`
	Pass   bool   `json:"pass"`
	Detail string `json:"detail,omitempty"`
}

// fixedTime keeps the produced archives byte-identical between runs.
var fixedTime = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)

// ArchiveName returns the output file name for a target.
func ArchiveName(t Target, release string) string {
	stem := "13-Sentinels-Patch"
	if r := strings.TrimSpace(release); r != "" {
		stem += "-" + sanitize(r)
	}
	switch t {
	case TargetAtmosphere:
		return stem + "-Atmosphere.zip"
	case TargetEmulator:
		return stem + "-Emulators.zip"
	default:
		return stem + ".zip"
	}
}

func sanitize(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			out = append(out, r)
		default:
			out = append(out, '-')
		}
	}
	return string(out)
}

// entriesFor returns the (zip path -> payload) pairs for a target.
func entriesFor(t Target, req Request) (map[string][]byte, error) {
	build := strings.ToUpper(strings.TrimSpace(req.BuildID))
	if build == "" {
		return nil, fmt.Errorf("packaging: no build identifier")
	}
	title := strings.TrimSpace(req.TitleID)
	if title == "" {
		return nil, fmt.Errorf("packaging: no title identifier")
	}
	patchName := build + ".ips"
	m := map[string][]byte{}
	switch t {
	case TargetAtmosphere:
		m[AtmosphereRoot+"/"+ContentsDir+"/"+title+"/romfs/"+RomfsArchiveName] = req.ArchiveBytes
		m[AtmosphereRoot+"/"+AtmospherePatchDir+"/"+patchName] = req.PatchBytes
	case TargetEmulator:
		m[ModFolder+"/romfs/"+RomfsArchiveName] = req.ArchiveBytes
		m[ModFolder+"/exefs/"+patchName] = req.PatchBytes
	default:
		return nil, fmt.Errorf("packaging: unsupported target %q", t)
	}
	return m, nil
}

// Build produces one archive for a target and validates it before returning.
// It is Render followed by Commit.
func Build(req Request, t Target) (*Artifact, error) {
	raw, err := Render(req, t)
	if err != nil {
		return nil, err
	}
	return Commit(req, t, raw)
}

// Render assembles the archive bytes for a target without writing anything.
// It is used to stage every target in memory so a multi-target build can be
// committed only once all of them are ready.
func Render(req Request, t Target) ([]byte, error) {
	entries, err := entriesFor(t, req)
	if err != nil {
		return nil, err
	}
	return writeZip(entries)
}

// Commit writes previously rendered archive bytes to the output directory and
// validates the written file. Nothing is written unless the destination rules
// allow it; the write itself is atomic.
func Commit(req Request, t Target, raw []byte) (*Artifact, error) {
	entries, err := entriesFor(t, req)
	if err != nil {
		return nil, err
	}
	name := ArchiveName(t, req.Release)
	if strings.TrimSpace(req.OutputDir) == "" {
		return nil, fmt.Errorf("packaging: no output directory")
	}
	if err := os.MkdirAll(req.OutputDir, 0o755); err != nil {
		return nil, err
	}
	dst := filepath.Join(req.OutputDir, name)
	if err := atomic.WriteFile(dst, raw, atomic.Options{Overwrite: req.Overwrite}); err != nil {
		return nil, err
	}
	art := &Artifact{
		Target: t, Label: t.Label(), Path: dst,
		Size: int64(len(raw)), SHA256: hashutil.Bytes(raw),
	}
	for p, b := range entries {
		art.Entries = append(art.Entries, Entry{Path: p, Size: int64(len(b)), SHA256: hashutil.Bytes(b)})
	}
	sort.Slice(art.Entries, func(i, j int) bool { return art.Entries[i].Path < art.Entries[j].Path })
	checks, err := ValidateFile(dst, t, req)
	if err != nil {
		return art, err
	}
	art.Validated = true
	for _, c := range checks {
		if !c.Pass {
			art.Validated = false
		}
	}
	return art, nil
}

// ArchivePath returns the destination file a target archives to.
func ArchivePath(req Request, t Target) string {
	return filepath.Join(req.OutputDir, ArchiveName(t, req.Release))
}

// DestinationExists reports whether the target archive already exists.
func DestinationExists(req Request, t Target) bool {
	p := ArchivePath(req, t)
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// writeZip writes a deterministic archive: entries sorted by name, a fixed
// modification time and the same compression settings every run.
func writeZip(entries map[string][]byte) ([]byte, error) {
	names := make([]string, 0, len(entries))
	for n := range entries {
		names = append(names, n)
	}
	sort.Strings(names)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	zw.SetComment("")
	for _, n := range names {
		hdr := &zip.FileHeader{Name: n, Method: zip.Deflate, Modified: fixedTime}
		hdr.SetMode(0o644)
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(entries[n]); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ValidateFile reopens a produced archive and checks its layout.
func ValidateFile(path string, t Target, req Request) ([]Check, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return validateBytes(raw, t, req)
}

func validateBytes(raw []byte, t Target, req Request) ([]Check, error) {
	var checks []Check
	add := func(id string, pass bool, detail string) {
		checks = append(checks, Check{Target: string(t), ID: id, Pass: pass, Detail: detail})
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, fmt.Errorf("packaging: produced archive does not reopen: %w", err)
	}
	want, err := entriesFor(t, req)
	if err != nil {
		return nil, err
	}
	got := map[string][]byte{}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, err
		}
		got[f.Name] = b
	}
	// exact entry set
	extra := []string{}
	for n := range got {
		if _, ok := want[n]; !ok {
			extra = append(extra, n)
		}
	}
	missing := []string{}
	for n := range want {
		if _, ok := got[n]; !ok {
			missing = append(missing, n)
		}
	}
	sort.Strings(extra)
	sort.Strings(missing)
	add("layout.entries", len(extra) == 0 && len(missing) == 0,
		fmt.Sprintf("extra=%v missing=%v", extra, missing))
	// payload identity
	archivePath, patchPath := archiveEntry(t, req)
	if b, ok := got[archivePath]; ok {
		if len(req.ArchiveBytes) > 0 {
			add("payload.archive", bytes.Equal(b, req.ArchiveBytes),
				fmt.Sprintf("romfs archive byte-identical (%d bytes)", len(b)))
		} else {
			isCPK := len(b) >= 4 && string(b[:4]) == "CPK "
			add("payload.archive", isCPK, fmt.Sprintf("romfs archive carries the expected container markers (%d bytes)", len(b)))
		}
	}
	if b, ok := got[patchPath]; ok {
		if len(req.PatchBytes) > 0 {
			add("payload.patch", bytes.Equal(b, req.PatchBytes),
				fmt.Sprintf("executable patch byte-identical (%d bytes)", len(b)))
		}
	}
	// target-specific placement rules
	switch t {
	case TargetAtmosphere:
		noNesting := true
		ipsInRomfs := false
		for n := range got {
			if strings.HasPrefix(n, ModFolder+"/") {
				noNesting = false
			}
			if strings.Contains(n, "/romfs/") && strings.HasSuffix(n, ".ips") {
				ipsInRomfs = true
			}
		}
		add("layout.cfw_root", noNesting, "archive is rooted at atmosphere/")
		add("layout.patch_not_in_romfs", !ipsInRomfs, "executable patch is not inside romfs/")
		add("layout.title_dir", got[AtmosphereRoot+"/"+ContentsDir+"/"+strings.TrimSpace(req.TitleID)+"/romfs/"+RomfsArchiveName] != nil,
			"archive sits under the per-title content directory")
	case TargetEmulator:
		hasAtmosphere := false
		ipsInRomfs := false
		for n := range got {
			if strings.HasPrefix(n, AtmosphereRoot+"/") {
				hasAtmosphere = true
			}
			if strings.Contains(n, "/romfs/") && strings.HasSuffix(n, ".ips") {
				ipsInRomfs = true
			}
		}
		add("layout.no_cfw_nesting", !hasAtmosphere, "no atmosphere/ nesting")
		add("layout.patch_not_in_romfs", !ipsInRomfs, "executable patch is not inside romfs/")
		add("layout.mod_root", got[ModFolder+"/romfs/"+RomfsArchiveName] != nil, "archive sits under the mod folder")
	}
	// no private or foreign payloads
	forbidden := false
	for n := range got {
		l := strings.ToLower(n)
		if strings.HasSuffix(l, ".nsp") || strings.HasSuffix(l, ".xci") || strings.HasSuffix(l, ".nca") ||
			strings.HasSuffix(l, ".keys") || strings.HasSuffix(l, "prod.keys") || strings.HasSuffix(l, "title.keys") ||
			strings.Contains(l, "cache") || strings.Contains(l, "report") {
			forbidden = true
		}
	}
	add("layout.no_foreign_payload", !forbidden, "no containers, keys, caches or reports")
	// executable patch must parse as an IPS container
	if b, ok := got[patchPath]; ok {
		add("patch.container", len(b) >= 8 && string(b[:5]) == "PATCH" && string(b[len(b)-3:]) == "EOF", "IPS container markers present")
	}
	return checks, nil
}

func archiveEntry(t Target, req Request) (string, string) {
	build := strings.ToUpper(strings.TrimSpace(req.BuildID))
	patch := build + ".ips"
	switch t {
	case TargetAtmosphere:
		return AtmosphereRoot + "/" + ContentsDir + "/" + strings.TrimSpace(req.TitleID) + "/romfs/" + RomfsArchiveName,
			AtmosphereRoot + "/" + AtmospherePatchDir + "/" + patch
	case TargetEmulator:
		return ModFolder + "/romfs/" + RomfsArchiveName, ModFolder + "/exefs/" + patch
	}
	return "", ""
}

// ReadEntries returns the file entries of a produced archive, for inspection.
func ReadEntries(path string) ([]Entry, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	var out []Entry
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		h := sha256.New()
		n, err := io.Copy(h, rc)
		rc.Close()
		if err != nil {
			return nil, err
		}
		out = append(out, Entry{Path: f.Name, Size: n, SHA256: hex.EncodeToString(h.Sum(nil))})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// DescribeArtifacts renders a compact text listing for reports.
func DescribeArtifacts(arts []Artifact) string {
	var b strings.Builder
	for _, a := range arts {
		fmt.Fprintf(&b, "%s  %s  %d bytes  sha256 %s  validated=%v\n", a.Label, a.Path, a.Size, hashutil.Short(a.SHA256), a.Validated)
		for _, e := range a.Entries {
			fmt.Fprintf(&b, "    %-64s %10d  %s\n", e.Path, e.Size, hashutil.Short(e.SHA256))
		}
	}
	return b.String()
}

// MarshalReport renders a report as indented JSON.
func MarshalReport(r *Report) ([]byte, error) {
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}
