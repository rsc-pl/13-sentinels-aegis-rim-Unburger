// Package localsources discovers and identifies the local game containers the
// workflow builds from. It searches the configured and default locations,
// identifies each candidate by content (size + SHA-256) rather than by name,
// and reports clearly what was found and what is still missing. Everything is
// read-only.
package localsources

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"13s-patcher/internal/gameprofile"
	"13s-patcher/internal/hashutil"
)

// Archive is one located container.
type Archive struct {
	Role           string   `json:"role"`
	Path           string   `json:"path"`
	Size           int64    `json:"size"`
	SHA256         string   `json:"sha256,omitempty"`
	Identified     bool     `json:"identified"`
	TitleID        string   `json:"title_id,omitempty"`
	DisplayVersion string   `json:"display_version,omitempty"`
	VersionRaw     uint64   `json:"version_raw,omitempty"`
	Required       bool     `json:"required"`
	Notes          []string `json:"notes,omitempty"`
}

// Request configures discovery.
type Request struct {
	// Dirs are the directories to scan (non-recursively).
	Dirs []string
	// Explicit maps a role to an explicit container path.
	Explicit map[string]string
	// HashUnknown asks for the digest of candidates that match no known
	// identity. It is off by default: hashing a multi-gigabyte container that
	// cannot be accepted is wasteful, so only size-matching candidates are
	// hashed unless this is set.
	HashUnknown bool
}

// Report is the discovery result.
type Report struct {
	Found   []Archive `json:"found"`
	Missing []string  `json:"missing"`
	Dirs    []string  `json:"search_dirs"`
	Notes   []string  `json:"notes,omitempty"`
}

// Discover locates and identifies containers.
func Discover(req Request) (*Report, error) {
	rep := &Report{Dirs: append([]string(nil), req.Dirs...)}
	seen := map[string]bool{}

	add := func(path string, required bool, explicit bool) error {
		abs, err := filepath.Abs(path)
		if err != nil {
			return err
		}
		if seen[abs] {
			return nil
		}
		fi, err := os.Stat(abs)
		if err != nil {
			if explicit {
				rep.Notes = append(rep.Notes, fmt.Sprintf("configured path is not readable: %s", path))
			}
			return nil
		}
		if fi.IsDir() {
			return nil
		}
		seen[abs] = true
		a := Archive{Path: abs, Size: fi.Size(), Required: required}
		a.Role = "unknown"
		if known := findKnownSize(a.Size); known != nil {
			sum, _, err := hashutil.File(abs)
			if err != nil {
				return err
			}
			a.SHA256 = sum
			if known.SHA256 == sum {
				a.Identified = true
				a.Role = known.Role
				a.TitleID = known.TitleID
				a.DisplayVersion = known.DisplayVersion
				a.VersionRaw = known.VersionRaw
				a.Required = known.Required
			} else {
				a.Notes = append(a.Notes, "same size as a known container but a different digest")
			}
		} else if req.HashUnknown {
			sum, _, err := hashutil.File(abs)
			if err != nil {
				return err
			}
			a.SHA256 = sum
		}
		if a.TitleID == "" {
			if tid, ok := titleHintFromContainer(abs); ok {
				a.TitleID = tid
				a.Notes = append(a.Notes, "title id read from the container ticket")
			}
		}
		rep.Found = append(rep.Found, a)
		return nil
	}

	for _, role := range roleOrder() {
		if p := strings.TrimSpace(req.Explicit[role]); p != "" {
			if err := add(p, true, true); err != nil {
				return rep, err
			}
		}
	}
	for _, dir := range req.Dirs {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		ents, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range ents {
			if e.IsDir() || !isContainerName(e.Name()) {
				continue
			}
			if err := add(filepath.Join(dir, e.Name()), false, false); err != nil {
				return rep, err
			}
		}
	}

	sort.SliceStable(rep.Found, func(i, j int) bool {
		if rep.Found[i].Role != rep.Found[j].Role {
			return rep.Found[i].Role < rep.Found[j].Role
		}
		return rep.Found[i].Path < rep.Found[j].Path
	})
	for _, in := range gameprofile.KnownInputs {
		if !in.Required {
			continue
		}
		found := false
		for _, a := range rep.Found {
			if a.Identified && a.Role == in.Role {
				found = true
				break
			}
		}
		if !found {
			rep.Missing = append(rep.Missing, in.Role)
		}
	}
	return rep, nil
}

// Has reports whether an identified container for role was found.
func (r *Report) Has(role string) *Archive {
	for i := range r.Found {
		if r.Found[i].Identified && r.Found[i].Role == role {
			return &r.Found[i]
		}
	}
	return nil
}

func roleOrder() []string { return []string{"base", "update", "extra"} }

func findKnownSize(size int64) *gameprofile.Input {
	for i := range gameprofile.KnownInputs {
		if gameprofile.KnownInputs[i].Size == size {
			return &gameprofile.KnownInputs[i]
		}
	}
	return nil
}

func isContainerName(name string) bool {
	lower := strings.ToLower(name)
	for _, ext := range []string{".nsp", ".xci", ".cpk", ".nsz"} {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}

// titleHintFromContainer reads the title id from the container's ticket entry
// name (the ticket is not encrypted, so no keys are needed). It is a hint only;
// acceptance still requires a digest match.
func titleHintFromContainer(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	pfs, err := ParsePFS0(f)
	if err != nil {
		return "", false
	}
	for _, e := range pfs.Files {
		base := filepath.Base(e.Name)
		if !strings.HasSuffix(strings.ToLower(base), ".tik") {
			continue
		}
		id := strings.TrimSuffix(base, filepath.Ext(base))
		if len(id) < 16 || !isHex(id) {
			continue
		}
		return strings.ToUpper(id[:16]), true
	}
	return "", false
}

func isHex(s string) bool {
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

// FindByHash walks roots looking for a regular file with the given size whose
// digest matches. It is used to reuse an already-produced artifact wherever it
// happens to live, without depending on its file name.
func FindByHash(roots []string, size int64, sha string) (string, bool) {
	for _, root := range roots {
		if strings.TrimSpace(root) == "" {
			continue
		}
		var found string
		filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || found != "" {
				if d != nil && d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				return nil
			}
			fi, err := d.Info()
			if err != nil || fi.Size() != size {
				return nil
			}
			sum, _, err := hashutil.File(path)
			if err != nil {
				return nil
			}
			if sum == sha {
				found = path
				return filepath.SkipAll
			}
			return nil
		})
		if found != "" {
			return found, true
		}
	}
	return "", false
}
