package extraction

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"13s-patcher/internal/atomic"
	"13s-patcher/internal/cpk"
)

// MembersSubdir is the cache subdirectory holding logical game members the
// build consumes but the update overlay does not itself carry (the text banks'
// source scripts and the title atlas). It uses the same path-flattening
// convention as the base banks: "GUI/change_kurabe.mbs" is stored as
// "GUI__change_kurabe.mbs".
const MembersSubdir = "base_members"

// stageCPKArtifacts finds the base game archive in an unpacked container tree,
// stages its logical text banks and the requested logical members. The base
// archive is game-derived and read from the user's own container output; no
// archive or member is shipped.
//
// It reports whether a usable archive was found so a caller can fall back to a
// loose-file extraction. A member the caller requires but the archive lacks is
// an error: that means the container revision does not match the accepted one,
// and silently continuing would produce a partial build.
func stageCPKArtifacts(unpackedDir, banksDir, membersDir string, members []string, naming BankNaming) (bool, error) {
	path, err := findBaseArchive(unpackedDir)
	if err != nil {
		return false, err
	}
	if path == "" {
		return false, nil
	}
	arch, err := cpk.OpenFile(path)
	if err != nil {
		return false, fmt.Errorf("base archive %s: %w", filepath.Base(path), err)
	}
	defer arch.Close()

	if err := os.MkdirAll(banksDir, 0o755); err != nil {
		return true, err
	}
	banks := 0
	for i := range arch.Members {
		m := &arch.Members[i]
		name := string(m.FileName)
		if !strings.HasSuffix(strings.ToLower(name), strings.ToLower(naming.Suffix)) {
			continue
		}
		logical, lerr := arch.Logical(m)
		if lerr != nil {
			return true, fmt.Errorf("base archive bank %s/%s: %w", m.Dir, m.FileName, lerr)
		}
		key := strings.ReplaceAll(string(m.Dir)+"/"+name, "/", "__")
		if err := atomic.WriteFile(filepath.Join(banksDir, key), logical, atomic.Options{Overwrite: true}); err != nil {
			return true, err
		}
		banks++
	}

	if len(members) == 0 {
		return true, nil
	}
	if err := os.MkdirAll(membersDir, 0o755); err != nil {
		return true, err
	}
	var missing []string
	for _, want := range members {
		member := strings.TrimPrefix(strings.ReplaceAll(want, "\\", "/"), "/")
		m := arch.MemberByPath(member)
		if m == nil {
			missing = append(missing, member)
			continue
		}
		logical, lerr := arch.Logical(m)
		if lerr != nil {
			return true, fmt.Errorf("base archive member %s: %w", member, lerr)
		}
		key := strings.ReplaceAll(member, "/", "__")
		if err := atomic.WriteFile(filepath.Join(membersDir, key), logical, atomic.Options{Overwrite: true}); err != nil {
			return true, err
		}
	}
	if len(missing) > 0 {
		return true, fmt.Errorf("base archive %s does not carry the required member(s): %s", filepath.Base(path), strings.Join(missing, ", "))
	}
	return true, nil
}

// findBaseArchive returns the base game CPK inside an unpacked container tree.
// The title's archive name is preferred; any other CPK is considered only by
// descending size, so a small unrelated archive cannot win.
func findBaseArchive(root string) (string, error) {
	var cps []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if strings.EqualFold(filepath.Ext(d.Name()), ".cpk") {
			cps = append(cps, path)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(cps) == 0 {
		return "", nil
	}
	sort.Slice(cps, func(i, j int) bool {
		ai, aj := archiveRank(cps[i]), archiveRank(cps[j])
		if ai != aj {
			return ai < aj
		}
		fi, _ := os.Stat(cps[i])
		fj, _ := os.Stat(cps[j])
		if fi != nil && fj != nil && fi.Size() != fj.Size() {
			return fi.Size() > fj.Size()
		}
		return cps[i] < cps[j]
	})
	return cps[0], nil
}

// archiveRank orders the preferred archive names ahead of any other CPK.
func archiveRank(path string) int {
	switch strings.ToLower(filepath.Base(path)) {
	case "robo_us.cpk":
		return 0
	case "robo.cpk":
		return 1
	}
	return 2
}

// copyMembers copies a staged member directory into the verified cache,
// checking that every staged file is readable and non-empty. The build verifies
// each member against its pinned identity before using it.
func copyMembers(srcDir, dstDir string) (int, error) {
	ents, err := os.ReadDir(srcDir)
	if err != nil {
		return 0, fmt.Errorf("base members: %w", err)
	}
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return 0, err
	}
	n := 0
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		raw, rerr := os.ReadFile(filepath.Join(srcDir, e.Name()))
		if rerr != nil {
			return n, rerr
		}
		if len(raw) == 0 {
			return n, fmt.Errorf("base member %s is empty", e.Name())
		}
		if err := atomic.WriteFile(filepath.Join(dstDir, e.Name()), raw, atomic.Options{Overwrite: true}); err != nil {
			return n, err
		}
		n++
	}
	if n == 0 {
		return 0, errors.New("base members: no staged files")
	}
	return n, nil
}
