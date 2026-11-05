// Package source identifies and validates the game resource archives a build
// consumes. It never modifies an input: it opens files read-only, records their
// identity (size + SHA-256) and structurally parses them.
package source

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"13s-patcher/internal/cpk"
	"13s-patcher/internal/hashutil"
	"13s-patcher/internal/patchdata"
)

// Identity describes a detected archive.
type Identity struct {
	Path       string `json:"path"`
	Size       int64  `json:"size"`
	SHA256     string `json:"sha256"`
	Members    int    `json:"members"`
	Compressed int    `json:"compressed_members"`
	Tvers      string `json:"tvers,omitempty"`
	Supported  bool   `json:"supported"`
	Note       string `json:"note,omitempty"`
}

// Identify hashes and parses an archive read-only.
func Identify(path string) (*Identity, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if fi.IsDir() {
		return nil, fmt.Errorf("%s is a directory, not an archive", path)
	}
	sum, size, err := hashutil.File(path)
	if err != nil {
		return nil, err
	}
	id := &Identity{Path: path, Size: size, SHA256: sum}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	c, err := cpk.Parse(raw)
	if err != nil {
		id.Note = err.Error()
		return id, nil
	}
	id.Supported = true
	id.Members = len(c.Members)
	for i := range c.Members {
		if c.Members[i].FileSize != c.Members[i].ExtractSize {
			id.Compressed++
		}
	}
	if c.Header != nil {
		if i := c.Header.ColIndex("Tvers"); i >= 0 && len(c.Header.Rows) > 0 {
			id.Tvers = string(c.Header.Rows[0][i].S)
		}
	}
	return id, nil
}

// Match validates an archive against a required source identity.
func Match(path string, spec patchdata.SourceSpec) (*Identity, error) {
	id, err := Identify(path)
	if err != nil {
		return nil, err
	}
	if spec.Size != 0 && id.Size != spec.Size {
		return id, fmt.Errorf("%s: size %d does not match the required %d", path, id.Size, spec.Size)
	}
	if spec.SHA256 != "" && id.SHA256 != spec.SHA256 {
		return id, fmt.Errorf("%s: sha256 %s does not match the required %s", path, id.SHA256, spec.SHA256)
	}
	if !id.Supported {
		return id, fmt.Errorf("%s: unsupported archive structure (%s)", path, id.Note)
	}
	return id, nil
}

// Candidate is a possible archive found during auto-detection.
type Candidate struct {
	Path string
	Size int64
	Hint bool // matched the manifest's path_hint or a known name
}

// FindArchive searches dir (non-recursively) for archive candidates. When hint is
// non-empty it is matched by basename first. Results are sorted with hinted
// candidates first.
func FindArchive(dir, hint string) ([]Candidate, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	hintBase := strings.ToLower(filepath.Base(hint))
	var out []Candidate
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		lower := strings.ToLower(name)
		isCPK := strings.HasSuffix(lower, ".cpk")
		if !isCPK && hintBase != "" && lower != hintBase {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, Candidate{Path: filepath.Join(dir, name), Size: fi.Size(), Hint: hintBase != "" && lower == hintBase})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Hint != out[j].Hint {
			return out[i].Hint
		}
		return out[i].Path < out[j].Path
	})
	return out, nil
}
