// Package hygiene scans the distributable source tree, its file names, its
// shipped binary metadata and the release boundary for markers that must never
// appear in a public release: internal project terminology, assistant/AI process
// provenance, coordination identifiers, private paths and internal build IDs.
//
// The marker strings are assembled from fragments so this file itself does not
// contain the markers it looks for, which means the scanner can safely include
// its own source in a scan. The comprehensive human-readable catalogue lives in
// the private .local/ policy area, never in the public tree.
//
// Three scan layers are provided:
//
//   - Scan: file contents (field-aware for the translation dataset), file and
//     directory names, and structured metadata of shipped binary assets.
//   - ScanVendor: the vendored third-party tree, checked only for the project's
//     own private markers; upstream assistant references are legitimate and are
//     not treated as project leakage.
//   - ScanTracked / CheckPrivateBoundary: read-only Git checks for accidental
//     tracking or non-ignored private instruction files.
//
// CheckReleaseTree applies the content, name and metadata layers to a staged
// release directory. None of these functions writes to the repository.
package hygiene

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ErrNotGit is returned by the Git-backed helpers when root is not a repository.
var ErrNotGit = errors.New("hygiene: not a git repository")

// Finding is one offending occurrence.
type Finding struct {
	// Path is repository-relative (or release-relative) and slash-separated.
	Path string
	// Line is 1-based for text content; 0 for names, metadata and boundary checks.
	Line int
	// Marker is the human-readable marker that matched.
	Marker string
	// Text is the offending text (or the offending path for name findings).
	Text string
	// Kind is "content", "path", "metadata", "tracked" or "boundary".
	Kind string
}

func (f Finding) String() string {
	kind := f.Kind
	if kind == "" {
		kind = "content"
	}
	loc := f.Path
	if f.Line > 0 {
		loc = fmt.Sprintf("%s:%d", f.Path, f.Line)
	}
	return fmt.Sprintf("%s: %s marker %q: %s", loc, kind, f.Marker, strings.TrimSpace(f.Text))
}

type marker struct {
	name string
	re   *regexp.Regexp
	// needle is a lower-case literal prefilter; empty means the regex must be
	// evaluated directly. It keeps the full-repository and vendor scans fast.
	needle string
}

// cat concatenates marker fragments so this file never contains a marker itself.
func cat(parts ...string) string { return strings.Join(parts, "") }

// literal matches a marker as a case-insensitive literal substring. It is used
// for project identifiers and paths that cannot legitimately appear in prose.
func literal(name string) marker {
	return marker{name: name, re: regexp.MustCompile(`(?i)` + regexp.QuoteMeta(name)), needle: strings.ToLower(name)}
}

// word matches a marker as a case-insensitive whole word. It is used for names
// that are ordinary English words elsewhere (for example an assistant name that
// is also a surname), so a substring in unrelated prose does not match.
func word(name string) marker {
	return marker{name: name, re: regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(name) + `\b`), needle: strings.ToLower(name)}
}

// expr matches a raw case-insensitive regular expression.
func expr(name, pattern string) marker {
	return marker{name: name, re: regexp.MustCompile(`(?i)` + pattern)}
}

// matches reports whether m matches line, using the literal prefilter when one
// is available.
func (m marker) matches(line, lower string) bool {
	if m.needle != "" && !strings.Contains(lower, m.needle) {
		return false
	}
	return m.re.MatchString(line)
}

// markers returns the complete forbidden pattern set applied to public content
// and file names. Ordinary technical words (model, prompt, agent, worker, token,
// cursor, system, user, reasoning) are deliberately NOT markers: they are
// legitimate domain vocabulary and game dialogue. Detection targets the
// unambiguous process markers instead.
//
// The public tree documents its user-state directory (".local/") because that is
// where a checkout keeps the keys and the tool state, so only the private
// subtrees of that directory — the project's own working areas — are forbidden,
// together with absolute user paths.
func markers() []marker {
	ms := []marker{
		// project identities and coordination vocabulary
		literal(cat("mu", "se")),
		literal(cat("chi", "se")),
		literal(cat("mus", "etool")),
		literal(cat("b9", "r4")),
		literal(cat("east", "er")),
		literal(cat("ow", "ner")),
		literal(cat("ow", "ner", "_verify")),
		literal(cat("check", "point")),
		literal(cat("13s", "_")),
		word(cat("full", "state")),
		word(cat("sub", "agent")),
		word(cat("multi", "-agent")),
		word(cat("hand", "off")),
		word(cat("hand", "-off")),
		word(cat("pro", "ject coordinator")),
		word(cat("the", " coordinator")),

		// private subtrees of the user-state directory
		literal(cat(".", "local/work")),
		literal(cat(".", "local/check")),
		literal(cat(".", "local/docs")),
		literal(cat(".", "local/chis")),
		literal(cat(".", "local/astra")),
		literal(cat(".", "local/reports")),
		literal(cat(".", "local/roms")),

		// absolute user paths and private workspace names
		literal(cat("/ho", "me/")),
		literal(cat("/Us", "ers/")),
		literal(cat("Hermes", "Workspace")),
		literal(cat("Da", "ne2")),

		// private instruction/config file names
		literal(cat("AG", "ENTS.md")),
		literal(cat("CL", "AUDE", ".md")),
		literal(cat("GEM", "INI", ".md")),
		literal(cat("open", "code", ".json")),
		literal(cat(".open", "code")),
		literal(cat(".", "clau", "de")),
		literal(cat(".cursor", "rules")),
	}

	// AI provider / assistant / agent-tool names. Word-bounded so ordinary
	// surnames and unrelated English words do not match.
	for _, n := range []string{
		cat("chat", "gpt"), cat("open", "ai"), cat("anthro", "pic"),
		cat("clau", "de"), cat("co", "pilot"), cat("gem", "ini"),
		cat("deep", "seek"), cat("qw", "en"), cat("lla", "ma"),
		cat("mist", "ral"), cat("gr", "ok"), cat("open", "router"),
		cat("open", "code"), cat("co", "dex"), cat("ai", "der"),
		cat("wind", "surf"), cat("tab", "nine"), cat("per", "plexity"),
		cat("hugging", "face"), cat("ol", "lama"), cat("lang", "chain"),
		cat("auto", "gpt"),
	} {
		ms = append(ms, word(n))
	}

	// AI-generation descriptions and conversational/prompt artefacts.
	for _, p := range []string{
		cat("large language ", "model"),
		cat("language ", "model"),
		cat("AI ", "assistant"),
		cat("as an ", "AI"),
		cat("AI-", "generated"),
		cat("generated by ", "AI"),
		cat("generated by ", "Chat", "GPT"),
		cat("generated by ", "Clau", "de"),
		cat("system ", "prompt"),
		cat("system_", "prompt"),
		cat("prompt ", "engineering"),
		cat("context ", "window"),
		cat("the ", "assistant"),
		cat("assistant ", "said"),
		cat("the model ", "said"),
		cat("as requested by ", "the ow", "ner"),
		cat("per the ", "ow", "ner"),
		cat("ow", "ner", " approved"),
		cat("ow", "ner", " decided"),
		cat("ow", "ner", " asked"),
		cat("ow", "ner", " told"),
		cat("next ", "chat"),
		cat("later ", "chat"),
		cat("previous ", "chat"),
		cat("earlier ", "chat"),
		cat("this ", "conversation"),
		cat("chat ", "log"),
		cat("chat ", "export"),
		cat("chat ", "transcript"),
		cat("dev ", "note"),
		cat("temp ", "note"),
		cat("scratch ", "note"),
		cat("internal ", "note"),
		cat("check", "point ", "archive"),
	} {
		ms = append(ms, literal(p))
	}

	// Internal milestone / phase / build identifiers.
	ms = append(ms,
		expr(cat("T11", "[2-9]"), cat("T11", "[2-9]")),
		expr(cat("P0", "[0-2]"), cat("P0", "[0-2]")),
		expr(cat("V0", "[12]"), cat("V0", "[12]")),
		expr(cat("L", "L", "M"), `\b`+cat("L", "L", "M")+`\b`),
		expr("parent role", `\b`+cat("parent", "_(state|task|session|context|agent)")+`\b`),
	)
	return ms
}

// vendorMarkers returns the reduced set applied to vendored third-party content.
// Upstream code legitimately contains common English words and absolute-path
// fragments in its own documentation; only markers specific to this project are
// meaningful there.
func vendorMarkers() []marker {
	ms := []marker{
		word(cat("mus", "etool")),
		word(cat("b9", "r4")),
		word(cat("ow", "ner", "_verify")),
		word(cat("full", "state")),
		word(cat("chi", "se")),
		word(cat("mu", "se")),
		expr(cat("13s", "_"), `\b`+cat("13s", "_")+`[A-Za-z0-9_]*`),
		literal(cat(".", "local/work")),
		literal(cat(".", "local/check")),
		literal(cat(".", "local/docs")),
		literal(cat(".", "local/chis")),
		literal(cat(".", "local/astra")),
		literal(cat(".", "local/reports")),
		literal(cat(".", "local/roms")),
		literal(cat("Hermes", "Workspace")),
		literal(cat("Da", "ne2")),
		literal(cat("open", "code", ".json")),
		literal(cat(".open", "code")),
		literal(cat(".", "clau", "de")),
		literal(cat(".cursor", "rules")),
	}
	return ms
}

// vendorPathMarkers are applied to vendored file names. Upstream agent
// instruction files are legitimate third-party content and are intentionally
// not path findings.
func vendorPathMarkers() []marker {
	var out []marker
	for _, m := range vendorMarkers() {
		if strings.Contains(strings.ToLower(m.name), cat("ag", "ents.md")) {
			continue
		}
		out = append(out, m)
	}
	return out
}

// highSignalMarkers are the long, project-specific markers safe to look for in
// opaque binary payloads (printable runs). Short common words are excluded to
// avoid random-byte false positives in compressed data.
func highSignalMarkers() []marker {
	var out []marker
	for _, m := range markers() {
		if len(m.name) >= 8 {
			out = append(out, m)
		}
	}
	return out
}

// skipDir reports whether a directory is excluded from the public tree scan.
// "build" is not skipped by name because it is also a legitimate package
// directory (internal/build); only the repository-root build output is skipped.
func skipDir(root, path, name string) bool {
	switch name {
	case "vendor", ".git", "node_modules", "dist", "bin", "reports",
		"target", "coverage", ".cache", "tmp":
		return true
	}
	if filepath.Clean(path) == filepath.Join(filepath.Clean(root), "build") {
		return true
	}
	return false
}

// privateDirName reports whether a directory holds private agent/config state
// that must never be part of the public candidate set.
func privateDirName(name string) bool {
	switch strings.ToLower(name) {
	case ".local", cat(".open", "code"), cat(".", "clau", "de"), ".cursor", ".idea", ".vscode":
		return true
	}
	return false
}

// privateFileName reports whether a file is a private agent/instruction file.
func privateFileName(name string) bool {
	switch strings.ToLower(name) {
	case cat("ag", "ents.md"), cat("cl", "aude", ".md"), cat("gem", "ini", ".md"),
		cat("open", "code", ".json"), cat("open", "code", ".jsonc"),
		cat(".cursor", "rules"), ".env":
		return true
	}
	return false
}

// Scan walks root and returns content, path-name and shipped-metadata findings.
// Directories that hold private state or generated output are skipped; the
// vendored tree is handled separately by ScanVendor.
func Scan(root string) ([]Finding, error) {
	root = filepath.Clean(root)
	ms := markers()
	var findings []Finding
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && (skipDir(root, path, d.Name()) || privateDirName(d.Name())) {
				return filepath.SkipDir
			}
			if path != root {
				rel, rerr := filepath.Rel(root, path)
				if rerr != nil {
					rel = path
				}
				findings = append(findings, scanPath(filepath.ToSlash(rel), ms)...)
			}
			return nil
		}
		if privateFileName(d.Name()) {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			rel = path
		}
		rel = filepath.ToSlash(rel)
		findings = append(findings, scanPath(rel, ms)...)

		fi, err := d.Info()
		if err != nil {
			return err
		}
		if fi.Size() > 32<<20 {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if binaryExt(rel) || isBinary(raw) {
			findings = append(findings, scanBinary(rel, raw)...)
			return nil
		}
		if isDatasetRecords(rel) {
			findings = append(findings, scanDatasetRecords(rel, raw, ms)...)
			return nil
		}
		findings = append(findings, scanBytes(rel, raw, ms)...)
		return nil
	})
	sortFindings(findings)
	return findings, err
}

// ScanVendor scans the vendored third-party tree for project-specific private
// markers only. Upstream source, licences and documentation are never flagged
// for their own assistant/editor references.
func ScanVendor(root string) ([]Finding, error) {
	vroot := filepath.Join(filepath.Clean(root), "patcher", "vendor")
	if fi, err := os.Stat(vroot); err != nil || !fi.IsDir() {
		return nil, nil
	}
	ms := vendorMarkers()
	pms := vendorPathMarkers()
	var findings []Finding
	err := filepath.WalkDir(vroot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, rerr := filepath.Rel(filepath.Clean(root), path)
		if rerr != nil {
			rel = path
		}
		rel = filepath.ToSlash(rel)
		findings = append(findings, scanPath(rel, pms)...)
		fi, err := d.Info()
		if err != nil {
			return err
		}
		if fi.Size() > 8<<20 {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if isBinary(raw) {
			findings = append(findings, scanRuns(rel, raw, ms)...)
			return nil
		}
		findings = append(findings, scanBytes(rel, raw, ms)...)
		return nil
	})
	sortFindings(findings)
	return findings, err
}

// ScanFile scans one explicit file (or directory) for the changed-file mode.
// The path is interpreted relative to root. Private paths are reported as
// boundary findings instead of being scanned.
func ScanFile(root, rel string) ([]Finding, error) {
	root = filepath.Clean(root)
	rel = filepath.ToSlash(rel)
	full := filepath.Join(root, filepath.FromSlash(rel))
	if !withinRoot(root, full) {
		return []Finding{{Path: rel, Kind: "boundary", Marker: "outside root", Text: rel}}, nil
	}
	if !strings.HasPrefix(rel, "patcher/vendor/") && privatePath(rel) {
		return []Finding{{Path: rel, Kind: "boundary", Marker: "private path", Text: rel}}, nil
	}
	fi, err := os.Lstat(full)
	if err != nil {
		return nil, err
	}
	if fi.IsDir() {
		var out []Finding
		err := filepath.WalkDir(full, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if path != full && (skipDir(root, path, d.Name()) || privateDirName(d.Name())) {
					return filepath.SkipDir
				}
				if path != full {
					r, rerr := filepath.Rel(root, path)
					if rerr != nil {
						r = path
					}
					out = append(out, scanPath(filepath.ToSlash(r), markers())...)
				}
				return nil
			}
			r, rerr := filepath.Rel(root, path)
			if rerr != nil {
				r = path
			}
			f, err := scanOneFile(root, filepath.ToSlash(r), path)
			out = append(out, f...)
			return err
		})
		sortFindings(out)
		return out, err
	}
	out, err := scanOneFile(root, rel, full)
	sortFindings(out)
	return out, err
}

func scanOneFile(root, rel, full string) ([]Finding, error) {
	vendor := strings.HasPrefix(rel, "patcher/vendor/")
	if !vendor && privateFileName(filepath.Base(rel)) {
		return []Finding{{Path: rel, Kind: "boundary", Marker: "private instruction file", Text: rel}}, nil
	}
	ms := markers()
	pms := ms
	if vendor {
		ms = vendorMarkers()
		pms = vendorPathMarkers()
	}
	out := scanPath(rel, pms)
	fi, err := os.Stat(full)
	if err != nil {
		return out, err
	}
	if fi.Size() > 32<<20 {
		return out, nil
	}
	raw, err := os.ReadFile(full)
	if err != nil {
		return out, err
	}
	if binaryExt(rel) || isBinary(raw) {
		if vendor {
			return append(out, scanRuns(rel, raw, ms)...), nil
		}
		return append(out, scanBinary(rel, raw)...), nil
	}
	if isDatasetRecords(rel) {
		return append(out, scanDatasetRecords(rel, raw, ms)...), nil
	}
	return append(out, scanBytes(rel, raw, ms)...), nil
}

// scanPath applies the marker set to a repository-relative path string.
func scanPath(rel string, ms []marker) []Finding {
	var out []Finding
	lower := strings.ToLower(rel)
	for _, m := range ms {
		if m.matches(rel, lower) {
			out = append(out, Finding{Path: rel, Marker: m.name, Text: rel, Kind: "path"})
		}
	}
	return out
}

// scanBytes checks one file's text line by line.
func scanBytes(rel string, raw []byte, ms []marker) []Finding {
	var findings []Finding
	for i, line := range strings.Split(string(raw), "\n") {
		lower := strings.ToLower(line)
		for _, m := range ms {
			if m.matches(line, lower) {
				findings = append(findings, Finding{Path: rel, Line: i + 1, Marker: m.name, Text: line, Kind: "content"})
			}
		}
	}
	return findings
}

// scanString applies the marker set to an already-decoded metadata string.
func scanString(rel, kind string, raw string, ms []marker) []Finding {
	var findings []Finding
	for i, line := range strings.Split(raw, "\n") {
		lower := strings.ToLower(line)
		for _, m := range ms {
			if m.matches(line, lower) {
				findings = append(findings, Finding{Path: rel, Line: i + 1, Marker: m.name, Text: line, Kind: kind})
			}
		}
	}
	return findings
}

// isDatasetRecords reports whether rel is the authored translation dataset
// record file, whose text values must be exempt from content marker scanning.
func isDatasetRecords(rel string) bool {
	return strings.EqualFold(filepath.Base(rel), "changes.jsonl") &&
		strings.Contains(filepath.ToSlash(rel), "translations/")
}

// ScanDataset checks a translation dataset directory. changes.jsonl is checked
// field-aware: only its JSON keys and its `bank_key` values are scanned, because
// the `text` values are authored English and may legitimately contain ordinary
// words that happen to share letters with a marker. Every other file — the
// manifest, the schema and the readme — is scanned in full.
func ScanDataset(dir string) ([]Finding, error) {
	ms := markers()
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var findings []Finding
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		path := filepath.Join(dir, e.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			return findings, err
		}
		if isBinary(raw) {
			continue
		}
		if e.Name() == "changes.jsonl" {
			findings = append(findings, scanDatasetRecords(e.Name(), raw, ms)...)
			continue
		}
		findings = append(findings, scanBytes(e.Name(), raw, ms)...)
	}
	sortFindings(findings)
	return findings, nil
}

// scanDatasetRecords scans the structural parts of a records file only.
func scanDatasetRecords(name string, raw []byte, ms []marker) []Finding {
	var findings []Finding
	line := 0
	for _, l := range strings.Split(string(raw), "\n") {
		line++
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		lower := strings.ToLower(l)
		for _, m := range ms {
			if m.needle != "" && !strings.Contains(lower, m.needle) {
				continue
			}
			loc := m.re.FindStringIndex(l)
			if loc == nil {
				continue
			}
			// The match is only a finding if it falls outside a text value.
			if !withinTextValue(l, loc[0]) {
				findings = append(findings, Finding{Path: name, Line: line, Marker: m.name, Text: l, Kind: "content"})
			}
		}
	}
	return findings
}

// withinTextValue reports whether byte offset off in a record line falls inside
// the `"text":"..."` string value.
func withinTextValue(line string, off int) bool {
	key := `"text":`
	i := strings.Index(line, key)
	if i < 0 || off < i {
		return false
	}
	start := i + len(key)
	for start < len(line) && (line[start] == ' ' || line[start] == '\t') {
		start++
	}
	if start < len(line) && line[start] == '"' {
		start++
	}
	end := start
	for end < len(line) {
		if line[end] == '\\' {
			end += 2
			continue
		}
		if line[end] == '"' {
			break
		}
		end++
	}
	return off >= start && off < end
}

// isBinary reports whether the content looks binary: a NUL in the first block.
func isBinary(raw []byte) bool {
	n := len(raw)
	if n > 8192 {
		n = 8192
	}
	return bytes.IndexByte(raw[:n], 0) >= 0
}

// binaryExt reports whether an asset extension is an opaque binary format whose
// only safe scan target is structured metadata or long high-signal runs.
func binaryExt(rel string) bool {
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".otf", ".ttf", ".ttc", ".woff", ".woff2",
		".ips", ".cpk", ".zip", ".gz", ".zst", ".bin", ".dat", ".exe", ".dll", ".so",
		".a", ".o", ".pdf", ".ico", ".bmp", ".webp":
		return true
	}
	return false
}

// scanBinary checks structured metadata (PNG text chunks, OpenType name tables)
// and, for opaque payloads, only long high-signal printable runs.
func scanBinary(rel string, raw []byte) []Finding {
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".png":
		return scanPNG(rel, raw)
	case ".otf", ".ttf", ".ttc":
		return scanSFNT(rel, raw)
	}
	return scanRuns(rel, raw, highSignalMarkers())
}

// scanPNG parses PNG text chunks (tEXt, zTXt, iTXt) without decoding pixels.
func scanPNG(rel string, raw []byte) []Finding {
	if len(raw) < 8 || !bytes.Equal(raw[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}) {
		return scanRuns(rel, raw, highSignalMarkers())
	}
	ms := markers()
	var out []Finding
	for i := 8; i+12 <= len(raw); {
		length := int(binary.BigEndian.Uint32(raw[i : i+4]))
		ctype := string(raw[i+4 : i+8])
		if length < 0 || i+12+length > len(raw) {
			break
		}
		data := raw[i+8 : i+8+length]
		switch ctype {
		case "tEXt":
			if key, text, ok := splitNUL(data); ok {
				out = append(out, scanString(rel, "metadata", key+"\n"+text, ms)...)
			}
		case "zTXt":
			if key, rest, ok := splitNUL(data); ok && len(rest) >= 1 {
				if plain, err := inflate([]byte(rest[1:])); err == nil {
					out = append(out, scanString(rel, "metadata", key+"\n"+plain, ms)...)
				}
			}
		case "iTXt":
			if key, rest, ok := splitNUL(data); ok {
				// compression flag, compression method, language tag, translated keyword, text
				if len(rest) >= 2 {
					flag := rest[0]
					body := rest[2:]
					if _, tail, ok := splitNUL([]byte(body)); ok { // language tag
						if _, text, ok := splitNUL([]byte(tail)); ok { // translated keyword
							if flag == 0 {
								out = append(out, scanString(rel, "metadata", key+"\n"+text, ms)...)
							} else if plain, err := inflate([]byte(text)); err == nil {
								out = append(out, scanString(rel, "metadata", key+"\n"+plain, ms)...)
							}
						}
					}
				}
			}
		}
		i += 12 + length
	}
	return out
}

// scanSFNT parses the OpenType/TrueType `name` table without decoding outlines.
func scanSFNT(rel string, raw []byte) []Finding {
	if len(raw) < 12 {
		return scanRuns(rel, raw, highSignalMarkers())
	}
	numTables := int(binary.BigEndian.Uint16(raw[4:6]))
	if 12+numTables*16 > len(raw) {
		return scanRuns(rel, raw, highSignalMarkers())
	}
	var nameOff, nameLen int
	for i := 0; i < numTables; i++ {
		rec := raw[12+i*16 : 12+i*16+16]
		if string(rec[0:4]) == "name" {
			nameOff = int(binary.BigEndian.Uint32(rec[8:12]))
			nameLen = int(binary.BigEndian.Uint32(rec[12:16]))
			break
		}
	}
	if nameOff <= 0 || nameLen <= 0 || nameOff+nameLen > len(raw) {
		return scanRuns(rel, raw, highSignalMarkers())
	}
	table := raw[nameOff : nameOff+nameLen]
	if len(table) < 6 {
		return scanRuns(rel, raw, highSignalMarkers())
	}
	count := int(binary.BigEndian.Uint16(table[2:4]))
	strOff := int(binary.BigEndian.Uint16(table[4:6]))
	ms := markers()
	var out []Finding
	for i := 0; i < count; i++ {
		base := 6 + i*12
		if base+12 > len(table) {
			break
		}
		platform := binary.BigEndian.Uint16(table[base : base+2])
		length := int(binary.BigEndian.Uint16(table[base+8 : base+10]))
		offset := int(binary.BigEndian.Uint16(table[base+10 : base+12]))
		start := strOff + offset
		if start < 0 || start+length > len(table) {
			continue
		}
		rawName := table[start : start+length]
		text := decodeName(platform, rawName)
		out = append(out, scanString(rel, "metadata", text, ms)...)
	}
	// A malformed table still gets the conservative long-run scan.
	out = append(out, scanRuns(rel, raw, highSignalMarkers())...)
	return out
}

// decodeName decodes an OpenType name record.
func decodeName(platform uint16, raw []byte) string {
	if platform == 0 || platform == 3 { // Unicode / Windows: UTF-16BE
		if len(raw)%2 != 0 {
			raw = raw[:len(raw)-1]
		}
		u := make([]uint16, 0, len(raw)/2)
		for i := 0; i+1 < len(raw); i += 2 {
			u = append(u, binary.BigEndian.Uint16(raw[i:i+2]))
		}
		return string(utf16Decode(u))
	}
	return string(raw) // Macintosh Roman / Latin-1-ish
}

func utf16Decode(u []uint16) []rune {
	out := make([]rune, 0, len(u))
	for i := 0; i < len(u); i++ {
		c := u[i]
		if c >= 0xD800 && c <= 0xDBFF && i+1 < len(u) {
			c2 := u[i+1]
			if c2 >= 0xDC00 && c2 <= 0xDFFF {
				out = append(out, rune(0x10000+(int32(c)-0xD800)<<10+(int32(c2)-0xDC00)))
				i++
				continue
			}
		}
		out = append(out, rune(c))
	}
	return out
}

// scanRuns extracts printable ASCII and UTF-16BE ASCII runs and applies ms. It
// is only used with long high-signal markers for opaque/compressed payloads.
func scanRuns(rel string, raw []byte, ms []marker) []Finding {
	var out []Finding
	// ASCII runs.
	for i := 0; i < len(raw); {
		if !printable(raw[i]) {
			i++
			continue
		}
		start := i
		for i < len(raw) && printable(raw[i]) {
			i++
		}
		if i-start >= 6 {
			out = append(out, scanString(rel, "metadata", string(raw[start:i]), ms)...)
		}
	}
	// UTF-16BE ASCII runs (zero high byte, printable low byte).
	for i := 0; i+1 < len(raw); {
		if raw[i] != 0 || !printable(raw[i+1]) {
			i += 2
			continue
		}
		start := i
		for i+1 < len(raw) && raw[i] == 0 && printable(raw[i+1]) {
			i += 2
		}
		if i-start >= 12 {
			var b strings.Builder
			for j := start; j+1 < i; j += 2 {
				b.WriteByte(raw[j+1])
			}
			out = append(out, scanString(rel, "metadata", b.String(), ms)...)
		}
	}
	return out
}

func printable(b byte) bool { return b >= 0x20 && b <= 0x7e }

func splitNUL(data []byte) (string, string, bool) {
	i := bytes.IndexByte(data, 0)
	if i < 0 {
		return "", "", false
	}
	return string(data[:i]), string(data[i+1:]), true
}

func inflate(data []byte) (string, error) {
	r, err := zlib.NewReader(bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	defer r.Close()
	plain, err := io.ReadAll(io.LimitReader(r, 1<<20))
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// ScanTracked reads the Git index and reports private instruction files, private
// data, build output and game artifacts that must never be tracked. It never
// modifies Git state. ErrNotGit is returned outside a repository.
func ScanTracked(root string) ([]Finding, error) {
	files, err := gitTracked(root)
	if err != nil {
		return nil, err
	}
	var out []Finding
	for _, rel := range files {
		out = append(out, trackedFindings(rel)...)
	}
	sortFindings(out)
	return out, nil
}

// trackedFindings returns the boundary findings for one tracked path.
func trackedFindings(rel string) []Finding {
	rel = filepath.ToSlash(rel)
	lower := strings.ToLower(rel)
	var out []Finding
	add := func(marker string) {
		out = append(out, Finding{Path: rel, Kind: "tracked", Marker: marker, Text: rel})
	}
	if strings.HasPrefix(lower, "patcher/vendor/") {
		// Vendored third-party files are expected to be tracked; upstream agent
		// instruction files are legitimate upstream content.
		return nil
	}
	if privatePath(rel) {
		add("private instruction/config path")
	}
	if hasPathSegment(lower, ".local") {
		add("private data directory")
	}
	if buildOutputPath(lower) {
		add("build output")
	}
	base := pathBase(lower)
	if sensitiveFile(base) {
		add("game key/container artifact")
	}
	if debugArtifact(base) {
		add("debug/temporary artifact")
	}
	return out
}

// CheckPrivateBoundary verifies, read-only, that private instruction files and
// the private data directory that exist in the working tree are ignored by Git
// and are not tracked. It also rejects a portable .gitignore entry for the root
// agent instructions: the project keeps those in .git/info/exclude.
func CheckPrivateBoundary(root string) ([]Finding, error) {
	root = filepath.Clean(root)
	tracked, err := gitTracked(root)
	if err != nil {
		return nil, err
	}
	trackedSet := make(map[string]bool, len(tracked))
	for _, f := range tracked {
		trackedSet[filepath.ToSlash(f)] = true
	}
	var out []Finding
	add := func(rel, marker string) {
		out = append(out, Finding{Path: rel, Kind: "boundary", Marker: marker, Text: rel})
	}

	// Root private instruction files must exist only as ignored working files.
	for _, name := range []string{
		cat("AG", "ENTS.md"), cat("agents", ".md"),
		cat("CL", "AUDE", ".md"), cat("cl", "aude", ".md"),
		cat("GEM", "INI", ".md"), cat("gem", "ini", ".md"),
		cat("open", "code", ".json"), cat("open", "code", ".jsonc"),
	} {
		full := filepath.Join(root, name)
		if fi, err := os.Stat(full); err != nil || fi.IsDir() {
			continue
		}
		if trackedSet[name] {
			add(name, "private instruction file is tracked")
		}
		if !gitIgnored(root, name) {
			add(name, "private instruction file is not git-ignored")
		}
		if name == cat("AG", "ENTS.md") && gitignoreMentions(root, name) {
			add(".gitignore", "agent instructions must stay in .git/info/exclude, not .gitignore")
		}
	}

	// The private data directory itself must be ignored when present.
	if fi, err := os.Stat(filepath.Join(root, ".local")); err == nil && fi.IsDir() {
		if !gitIgnored(root, ".local") {
			add(".local", "private data directory is not git-ignored")
		}
	}

	// Any private instruction file elsewhere in the tree must also be ignored.
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, werr error) error {
		if werr != nil {
			return nil
		}
		name := strings.ToLower(d.Name())
		if d.IsDir() {
			if name == ".git" || name == "vendor" || name == "node_modules" {
				return filepath.SkipDir
			}
			if privateDirName(name) {
				rel, rerr := filepath.Rel(root, path)
				if rerr != nil {
					return filepath.SkipDir
				}
				rel = filepath.ToSlash(rel)
				if !gitIgnored(root, rel) {
					add(rel, "private directory is not git-ignored")
				}
				return filepath.SkipDir
			}
			return nil
		}
		if privateFileName(name) {
			rel, rerr := filepath.Rel(root, path)
			if rerr != nil {
				return nil
			}
			rel = filepath.ToSlash(rel)
			if !gitIgnored(root, rel) {
				add(rel, "private instruction/config file is not git-ignored")
			}
		}
		return nil
	})
	if err != nil {
		return out, err
	}
	sortFindings(out)
	return out, nil
}

// CheckReleaseTree applies the content, name and metadata layers to a staged
// release directory and rejects private instruction/config files, private data,
// keys/containers, reports, caches and debug artifacts.
func CheckReleaseTree(root string) ([]Finding, error) {
	root = filepath.Clean(root)
	ms := markers()
	var out []Finding
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			rel = path
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			out = append(out, scanPath(rel, ms)...)
			if releaseForbiddenPath(rel) {
				out = append(out, Finding{Path: rel, Kind: "boundary", Marker: "private/release-forbidden path", Text: rel})
				return filepath.SkipDir
			}
			return nil
		}
		if releaseForbiddenPath(rel) {
			out = append(out, Finding{Path: rel, Kind: "boundary", Marker: "private/release-forbidden path", Text: rel})
			return nil
		}
		out = append(out, scanPath(rel, ms)...)
		fi, err := d.Info()
		if err != nil {
			return err
		}
		if fi.Size() > 32<<20 {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if isBinary(raw) || binaryExt(rel) {
			out = append(out, scanBinary(rel, raw)...)
			return nil
		}
		if isDatasetRecords(rel) {
			out = append(out, scanDatasetRecords(rel, raw, ms)...)
			return nil
		}
		out = append(out, scanBytes(rel, raw, ms)...)
		return nil
	})
	sortFindings(out)
	return out, err
}

// releaseForbiddenPath reports whether a release-relative path must not ship.
func releaseForbiddenPath(rel string) bool {
	lower := strings.ToLower(filepath.ToSlash(rel))
	if privatePath(rel) || hasPathSegment(lower, ".local") {
		return true
	}
	if buildOutputPath(lower) {
		return true
	}
	if strings.HasPrefix(lower, "translations/changes.jsonl") {
		return false
	}
	base := pathBase(lower)
	if sensitiveFile(base) || debugArtifact(base) {
		return true
	}
	for _, seg := range strings.Split(lower, "/") {
		switch seg {
		case "reports", cat("check", "points"), cat("check", "point"),
			cat("hand", "off"), cat("hand", "offs"),
			"transcripts", "chats", "qa-reports", "debug", ".cache", "tmp", "temp":
			return true
		case cat("check", "point-archive"), "chat-export", "chat-transcript":
			return true
		}
	}
	return false
}

// privatePath reports whether any path segment is a private instruction/config
// directory or file.
func privatePath(rel string) bool {
	for _, seg := range strings.Split(filepath.ToSlash(rel), "/") {
		if seg == "" || seg == "." {
			continue
		}
		if privateFileName(seg) || privateDirName(seg) {
			return true
		}
	}
	return false
}

func hasPathSegment(lower, seg string) bool {
	for _, s := range strings.Split(lower, "/") {
		if s == seg {
			return true
		}
	}
	return false
}

func pathBase(lower string) string {
	if i := strings.LastIndexByte(lower, '/'); i >= 0 {
		return lower[i+1:]
	}
	return lower
}

// buildOutputPath reports whether a path is generated build/report output.
func buildOutputPath(lower string) bool {
	for _, prefix := range []string{
		"bin/", "dist/", "target/", "coverage/", "reports/", "build/",
		"patcher/bin/", "patcher/dist/", "patcher/reports/", "patcher/build/",
		"patcher/examples/demo/out/", "patcher/examples/demo/reports/",
		"node_modules/",
	} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

// sensitiveFile reports a game key or container artifact that must never ship.
func sensitiveFile(base string) bool {
	for _, suffix := range []string{
		".keys", ".nsp", ".xci", ".nca", ".xcz", ".ncz", ".kip", ".kips",
	} {
		if strings.HasSuffix(base, suffix) {
			return true
		}
	}
	return false
}

// debugArtifact reports temporary/debug files that must never be public.
func debugArtifact(base string) bool {
	for _, suffix := range []string{
		".tmp", ".log", ".swp", ".swo", ".bak", ".orig", ".rej", ".pid",
		".core", ".dmp",
	} {
		if strings.HasSuffix(base, suffix) {
			return true
		}
	}
	switch base {
	case ".ds_store", "thumbs.db":
		return true
	}
	return strings.HasSuffix(base, "~")
}

// ChangedFiles returns the working-tree files Git considers changed: modified
// tracked files and untracked files that are not ignored. In a repository with
// no commits every public candidate is untracked, so this returns the candidate
// set; that is intentional and still useful as a changed-file gate.
func ChangedFiles(root string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	add := func(rel string) {
		rel = filepath.ToSlash(strings.TrimSpace(rel))
		if rel == "" || seen[rel] {
			return
		}
		seen[rel] = true
		out = append(out, rel)
	}
	queries := [][]string{
		{"diff", "--name-only", "-z", "--diff-filter=ACMRT"},
		{"diff", "--cached", "--name-only", "-z", "--diff-filter=ACMRT"},
		{"ls-files", "--others", "--exclude-standard", "-z"},
	}
	sawRepo := false
	for _, q := range queries {
		raw, err := gitOutput(root, q...)
		if err != nil {
			if errors.Is(err, ErrNotGit) {
				return nil, err
			}
			continue
		}
		sawRepo = true
		for _, rel := range splitNul(raw) {
			add(rel)
		}
	}
	if !sawRepo {
		return nil, ErrNotGit
	}
	sort.Strings(out)
	return out, nil
}

// SortFindings returns findings in deterministic order.
func SortFindings(fs []Finding) { sortFindings(fs) }

func sortFindings(fs []Finding) {
	sort.Slice(fs, func(i, j int) bool {
		if fs[i].Path != fs[j].Path {
			return fs[i].Path < fs[j].Path
		}
		if fs[i].Line != fs[j].Line {
			return fs[i].Line < fs[j].Line
		}
		if fs[i].Kind != fs[j].Kind {
			return fs[i].Kind < fs[j].Kind
		}
		return fs[i].Marker < fs[j].Marker
	})
}

// withinRoot reports whether full is inside root after cleaning.
func withinRoot(root, full string) bool {
	rel, err := filepath.Rel(root, full)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// gitTracked returns the tracked paths (git ls-files --cached).
func gitTracked(root string) ([]string, error) {
	out, err := gitOutput(root, "ls-files", "--cached", "-z")
	if err != nil {
		return nil, err
	}
	return splitNul(out), nil
}

// gitIgnored reports whether rel is ignored, using git check-ignore (read-only).
func gitIgnored(root, rel string) bool {
	cmd := exec.Command("git", "-C", root, "check-ignore", "-q", "--", rel)
	if err := cmd.Run(); err == nil {
		return true
	}
	return false
}

// gitignoreMentions reports whether the root .gitignore lists name directly.
func gitignoreMentions(root, name string) bool {
	raw, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimPrefix(line, "/")
		if strings.EqualFold(line, name) {
			return true
		}
	}
	return false
}

func gitOutput(root string, args ...string) ([]byte, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return nil, ErrNotGit
	}
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.ToLower(stderr.String())
		if strings.Contains(msg, "not a git repository") || strings.Contains(msg, "not a repository") {
			return nil, ErrNotGit
		}
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// FixSafe applies only deterministic, behaviour-preserving formatting to the
// explicitly listed files: gofmt for Go sources and trailing-space removal for
// machine-readable text. It never rewrites comments, Markdown, prose, the
// translation dataset or binary assets, so a semantic hygiene finding is left
// for a developer to correct in context.
func FixSafe(root string, rels []string) ([]string, error) {
	root = filepath.Clean(root)
	var touched []string
	for _, rel := range rels {
		rel = filepath.ToSlash(rel)
		full := filepath.Join(root, filepath.FromSlash(rel))
		if !withinRoot(root, full) {
			continue
		}
		switch strings.ToLower(filepath.Ext(rel)) {
		case ".go":
			if err := gofmtFile(full); err != nil {
				return touched, err
			}
			touched = append(touched, rel)
		case ".sh", ".mod", ".sum", ".json", ".toml", ".yaml", ".yml", ".cfg", ".conf":
			if strings.Contains(strings.ToLower(rel), "translations/") {
				continue
			}
			changed, err := trimTrailingSpace(full)
			if err != nil {
				return touched, err
			}
			if changed {
				touched = append(touched, rel)
			}
		}
	}
	sort.Strings(touched)
	return touched, nil
}

func gofmtFile(path string) error {
	if _, err := exec.LookPath("gofmt"); err != nil {
		return fmt.Errorf("gofmt not available: %w", err)
	}
	cmd := exec.Command("gofmt", "-w", path)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("gofmt %s: %w: %s", path, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// trimTrailingSpace removes trailing spaces/tabs from every line of a
// machine-readable file. CRLF line endings are preserved.
func trimTrailingSpace(path string) (bool, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	crlf := bytes.Contains(raw, []byte("\r\n"))
	body := strings.ReplaceAll(string(raw), "\r\n", "\n")
	lines := strings.Split(body, "\n")
	changed := false
	for i, line := range lines {
		trimmed := strings.TrimRight(line, " \t")
		if trimmed != line {
			lines[i] = trimmed
			changed = true
		}
	}
	if !changed {
		return false, nil
	}
	out := strings.Join(lines, "\n")
	if crlf {
		out = strings.ReplaceAll(out, "\n", "\r\n")
	}
	if err := os.WriteFile(path, []byte(out), fi.Mode().Perm()); err != nil {
		return false, err
	}
	return true, nil
}

func splitNul(raw []byte) []string {
	parts := bytes.Split(raw, []byte{0})
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if len(p) > 0 {
			out = append(out, string(p))
		}
	}
	return out
}
