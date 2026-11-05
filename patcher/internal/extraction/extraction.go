// Package extraction resolves the build inputs the integrated workflow needs —
// the effective update overlay archive and the base text banks that the overlay
// does not itself carry — from a verified local cache, from an existing
// extraction, or by invoking a configured extractor.
//
// Everything here is fail-closed and read-only with respect to the user's
// containers and keys: nothing is ever written outside the cache directory, and
// no key material is read, logged or reported.
package extraction

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"13s-patcher/internal/atomic"
	"13s-patcher/internal/fmsb"
	"13s-patcher/internal/gameprofile"
	"13s-patcher/internal/hashutil"
	"13s-patcher/internal/localsources"
)

// Cache file names.
const (
	OverlayDir     = "overlay"
	CacheStateFile = "cache.json"
	BanksSubdir    = "base_banks"
)

// Artifact names one required extraction product.
type Artifact string

const (
	// ArtifactOverlay is the effective update overlay archive.
	ArtifactOverlay Artifact = "overlay"
	// ArtifactBaseBanks is the set of logical base text banks.
	ArtifactBaseBanks Artifact = "base_banks"
	// ArtifactBaseMembers is the set of logical base game members the build
	// consumes directly (the plate scripts and the title atlas).
	ArtifactBaseMembers Artifact = "base_members"
)

// BankNaming describes how base bank files are named in an extraction. The
// convention replaces path separators with a double underscore.
type BankNaming struct {
	Prefix string // e.g. "Fms__"
	Suffix string // e.g. ".fms"
}

// DefaultBankNaming matches the extraction convention used by the project.
var DefaultBankNaming = BankNaming{Prefix: "Fms__", Suffix: ".fms"}

// ExtractRequest is handed to an Extractor.
type ExtractRequest struct {
	BaseContainer   string // identified base container (read-only)
	UpdateContainer string // identified update container (read-only)
	KeysDir         string // directory holding key files (never read by this package)
	DestDir         string // extraction workspace; the extractor writes here only
	Artifacts       []Artifact
	// BaseMembers lists the logical base game members the caller needs, in
	// addition to every text bank (e.g. "GUI/change_kurabe.mbs"). Empty means
	// only the overlay and banks are required.
	BaseMembers []string
}

// Extractor produces the required artifacts from the local containers.
type Extractor interface {
	// Describe names the extractor for reports.
	Describe() string
	// Available verifies the extractor's external requirements.
	Available() error
	// Extract writes the requested artifacts into req.DestDir:
	//   ArtifactOverlay      -> <DestDir>/<OverlayDir>/<any name matching the accepted revision>
	//   ArtifactBaseBanks    -> <DestDir>/<BanksSubdir>/<name><suffix>
	//   ArtifactBaseMembers  -> <DestDir>/<MembersSubdir>/<flattened member name>
	Extract(ctx context.Context, req ExtractRequest) error
}

// CommandExtractor runs a configured command template. Placeholders:
//
//	{base} {update} {keys} {dest} {overlay} {banks}
//
// {overlay} is a directory the extractor may write the overlay archive into;
// {banks} is the directory it must write the base bank files into.
type CommandExtractor struct {
	Argv []string
}

// Describe implements Extractor.
func (c *CommandExtractor) Describe() string { return strings.Join(c.Argv, " ") }

// Available implements Extractor.
func (c *CommandExtractor) Available() error {
	if len(c.Argv) == 0 {
		return errors.New("no extraction command configured")
	}
	bin := c.Argv[0]
	if strings.ContainsRune(bin, filepath.Separator) {
		if fi, err := os.Stat(bin); err != nil || fi.IsDir() {
			return fmt.Errorf("extraction command %s is not executable", bin)
		}
		return nil
	}
	if _, err := exec.LookPath(bin); err != nil {
		return fmt.Errorf("extraction command %s was not found on PATH", bin)
	}
	return nil
}

// Extract implements Extractor.
func (c *CommandExtractor) Extract(ctx context.Context, req ExtractRequest) error {
	if err := c.Available(); err != nil {
		return err
	}
	repl := strings.NewReplacer(
		"{base}", req.BaseContainer,
		"{update}", req.UpdateContainer,
		"{keys}", req.KeysDir,
		"{dest}", req.DestDir,
		"{overlay}", filepath.Join(req.DestDir, OverlayDir),
		"{banks}", filepath.Join(req.DestDir, BanksSubdir),
	)
	args := make([]string, len(c.Argv))
	for i, a := range c.Argv {
		args[i] = repl.Replace(a)
	}
	if len(req.Artifacts) == 0 {
		return errors.New("no artifacts requested")
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("extraction command failed: %w (%s)", err, tail(string(out), 3))
	}
	return nil
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}

// Tools reports which optional external tools are present, for diagnostics.
type Tools struct {
	Hactool    bool `json:"hactool"`
	Hactoolnet bool `json:"hactoolnet"`
	Nstool     bool `json:"nstool"`
	Python     bool `json:"python"`
}

// ProbeTools looks for the optional external helpers on PATH.
func ProbeTools() Tools {
	has := func(name string) bool { _, err := exec.LookPath(name); return err == nil }
	return Tools{Hactool: has("hactool"), Hactoolnet: has("hactoolnet"), Nstool: has("nstool"), Python: has("python3")}
}

// State is a resolved, verified set of build inputs.
type State struct {
	OverlayPath    string `json:"overlay_path"`
	OverlayName    string `json:"overlay_name"`
	OverlaySHA256  string `json:"overlay_sha256"`
	OverlaySize    int64  `json:"overlay_size"`
	BaseBanksDir   string `json:"base_banks_dir"`
	BankCount      int    `json:"bank_count"`
	BaseMembersDir string `json:"base_members_dir,omitempty"`
	MemberCount    int    `json:"member_count,omitempty"`
	CacheDir       string `json:"cache_dir"`
	FromCache      bool   `json:"from_cache"`
	Notes          []string
}

// Request configures resolution.
type Request struct {
	// CacheDir is the verified cache location. Required.
	CacheDir string
	// BankNaming describes base bank file names.
	BankNaming BankNaming
	// SearchRoots are scanned for an existing overlay archive and bank set.
	SearchRoots []string
	// OverlayPath explicitly names the overlay archive.
	OverlayPath string
	// BaseBanksDir explicitly names a base bank directory.
	BaseBanksDir string
	// BaseMembersDir explicitly names a base member directory using the
	// flattened naming convention.
	BaseMembersDir string
	// BaseMembers lists the logical base members required in addition to the
	// text banks. Each is cached under its flattened name.
	BaseMembers []string
	// BaseContainer / UpdateContainer are identified local containers.
	BaseContainer   string
	UpdateContainer string
	// KeysDir holds the user's key files (presence only).
	KeysDir string
	// Extractor performs the decode step when no existing artifact is found.
	Extractor Extractor
	// ExpectOverlaySize / ExpectOverlaySHA256 override the accepted overlay
	// identity. Empty/zero uses the built-in accepted revision.
	ExpectOverlaySize   int64
	ExpectOverlaySHA256 string
	// MinBankCount is the minimum number of bank files accepted (default 1).
	MinBankCount int
	// VerifyOnly limits Resolve to verifying an existing cache; it never
	// populates one and never invokes an extractor.
	VerifyOnly bool
	Logf       func(string)
	Ctx        context.Context
}

type cacheState struct {
	Schema          string            `json:"schema"`
	OverlayName     string            `json:"overlay_name"`
	OverlaySize     int64             `json:"overlay_size"`
	OverlaySHA256   string            `json:"overlay_sha256"`
	BankNaming      BankNaming        `json:"bank_naming"`
	BankFiles       map[string]string `json:"bank_files"` // file name -> sha256
	BaseBankCount   int               `json:"base_bank_count"`
	MemberFiles     map[string]string `json:"member_files,omitempty"` // file name -> sha256
	BaseMemberCount int               `json:"base_member_count,omitempty"`
	SourceBase      string            `json:"source_base,omitempty"`
	SourceUpdate    string            `json:"source_update,omitempty"`
}

const cacheSchema = "13s-patcher-cache.1"

// overlaySize returns the accepted overlay size for this request.
func (r *Request) overlaySize() int64 {
	if r.ExpectOverlaySize > 0 {
		return r.ExpectOverlaySize
	}
	return gameprofile.OverlaySize
}

// overlaySHA returns the accepted overlay digest for this request.
func (r *Request) overlaySHA() string {
	if r.ExpectOverlaySHA256 != "" {
		return r.ExpectOverlaySHA256
	}
	return gameprofile.OverlaySHA256
}

func (r *Request) log(format string, args ...any) {
	if r.Logf != nil {
		r.Logf(fmt.Sprintf(format, args...))
	}
}

// Resolve returns verified build inputs, populating the cache when needed.
func Resolve(req *Request) (*State, error) {
	if req == nil || strings.TrimSpace(req.CacheDir) == "" {
		return nil, errors.New("extraction: no cache directory configured")
	}
	if req.Ctx == nil {
		req.Ctx = context.Background()
	}
	if req.BankNaming == (BankNaming{}) {
		req.BankNaming = DefaultBankNaming
	}
	if req.MinBankCount <= 0 {
		req.MinBankCount = 1
	}
	banksDst := filepath.Join(req.CacheDir, BanksSubdir)
	membersDst := filepath.Join(req.CacheDir, MembersSubdir)
	wantMembers := len(req.BaseMembers) > 0

	if cached, _, ok := ReadCachedOverlay(req.CacheDir); ok {
		st, err := verifyCache(req, cached, banksDst, membersDst)
		if err == nil {
			st.Notes = append(st.Notes, "reused verified cache")
			return st, nil
		}
		if req.VerifyOnly {
			return nil, err
		}
	} else if req.VerifyOnly {
		return nil, errors.New("no verified cache present")
	}

	// Populate (or repair) the cache from an explicit path, an existing
	// extraction, or the configured extractor.
	if err := os.MkdirAll(req.CacheDir, 0o755); err != nil {
		return nil, err
	}

	overlaySrc, err := req.resolveOverlay()
	if err != nil {
		return nil, err
	}
	banksSrc, err := req.resolveBanks()
	if err != nil {
		return nil, err
	}
	membersSrc := ""
	if wantMembers {
		membersSrc, err = req.resolveMembers(membersDst)
		if err != nil {
			return nil, err
		}
	}
	if overlaySrc == "" || banksSrc == "" || (wantMembers && membersSrc == "") {
		// one or more products need decoding: delegate to the extractor.
		if req.Extractor == nil {
			return nil, req.missingDiagnostic(overlaySrc != "", banksSrc != "", wantMembers && membersSrc == "")
		}
		if err := req.Extractor.Available(); err != nil {
			return nil, fmt.Errorf("%w\nextraction is required but unavailable: %s", req.missingDiagnostic(overlaySrc != "", banksSrc != "", wantMembers && membersSrc == ""), err)
		}
		var arts []Artifact
		if overlaySrc == "" {
			arts = append(arts, ArtifactOverlay)
		}
		if banksSrc == "" {
			arts = append(arts, ArtifactBaseBanks)
		}
		if wantMembers && membersSrc == "" {
			arts = append(arts, ArtifactBaseMembers)
		}
		req.log("extracting %s using %s", strings.Join(artifactNames(arts), ", "), req.Extractor.Describe())
		tmp, err := os.MkdirTemp(req.CacheDir, ".extract-*")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(tmp)
		if err := checkSpace(req.CacheDir, 64<<20); err != nil {
			return nil, err
		}
		ereq := ExtractRequest{
			BaseContainer: req.BaseContainer, UpdateContainer: req.UpdateContainer,
			KeysDir: req.KeysDir, DestDir: tmp, Artifacts: arts, BaseMembers: req.BaseMembers,
		}
		if err := req.Extractor.Extract(req.Ctx, ereq); err != nil {
			return nil, err
		}
		if overlaySrc == "" {
			overlaySrc = findOverlayIn(tmp, req.overlaySize(), req.overlaySHA())
			if overlaySrc == "" {
				return nil, fmt.Errorf("the extractor produced no overlay archive in %s", tmp)
			}
		}
		if banksSrc == "" {
			banksSrc = filepath.Join(tmp, BanksSubdir)
		}
		if wantMembers && membersSrc == "" {
			membersSrc = filepath.Join(tmp, MembersSubdir)
			if !hasAnyFile(membersSrc) {
				return nil, fmt.Errorf("the extractor produced no base members in %s; the required member(s) are %s", membersSrc, strings.Join(req.BaseMembers, ", "))
			}
		}
	}

	overlayDst, err := copyOverlay(overlaySrc, filepath.Join(req.CacheDir, OverlayDir), req.overlaySize(), req.overlaySHA())
	if err != nil {
		return nil, err
	}
	if err := copyBanks(banksSrc, banksDst, req.BankNaming); err != nil {
		return nil, err
	}
	if wantMembers {
		if _, err := copyMembers(membersSrc, membersDst); err != nil {
			return nil, err
		}
	}
	if err := writeCacheState(req, overlayDst, banksDst, membersDst); err != nil {
		return nil, err
	}
	st, err := verifyCache(req, overlayDst, banksDst, membersDst)
	if err != nil {
		return nil, err
	}
	st.Notes = append(st.Notes, "cache populated and verified")
	return st, nil
}

// hasAnyFile reports whether dir holds at least one regular file.
func hasAnyFile(dir string) bool {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range ents {
		if !e.IsDir() {
			return true
		}
	}
	return false
}

// resolveMembers returns a directory holding every required member, or "".
func (r *Request) resolveMembers(cacheDir string) (string, error) {
	if p := strings.TrimSpace(r.BaseMembersDir); p != "" {
		fi, err := os.Stat(p)
		if err != nil || !fi.IsDir() {
			return "", fmt.Errorf("configured base member directory is not readable: %s", p)
		}
		return p, nil
	}
	if membersDirHas(cacheDir, r.BaseMembers) {
		return cacheDir, nil
	}
	for _, root := range r.SearchRoots {
		if strings.TrimSpace(root) == "" {
			continue
		}
		var found string
		filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || found != "" {
				if err != nil && d != nil && d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				if membersDirHas(path, r.BaseMembers) {
					found = path
				}
				return nil
			}
			return nil
		})
		if found != "" {
			r.log("found an existing base member directory")
			return found, nil
		}
	}
	return "", nil
}

// membersDirHas reports whether dir holds every requested member under its
// flattened name.
func membersDirHas(dir string, members []string) bool {
	for _, m := range members {
		name := strings.ReplaceAll(strings.TrimPrefix(strings.ReplaceAll(m, "\\", "/"), "/"), "/", "__")
		if name == "" {
			return false
		}
		fi, err := os.Stat(filepath.Join(dir, name))
		if err != nil || fi.IsDir() || fi.Size() == 0 {
			return false
		}
	}
	return len(members) > 0
}

func artifactNames(arts []Artifact) []string {
	out := make([]string, 0, len(arts))
	for _, a := range arts {
		out = append(out, string(a))
	}
	return out
}

// resolveOverlay returns a path to an existing overlay archive, or "".
func (r *Request) resolveOverlay() (string, error) {
	if p := strings.TrimSpace(r.OverlayPath); p != "" {
		fi, err := os.Stat(p)
		if err != nil || fi.IsDir() {
			return "", fmt.Errorf("configured overlay archive is not readable: %s", p)
		}
		if fi.Size() != r.overlaySize() {
			return "", fmt.Errorf("configured overlay archive has size %d, expected %d", fi.Size(), r.overlaySize())
		}
		sum, _, err := hashutil.File(p)
		if err != nil {
			return "", err
		}
		if sum != r.overlaySHA() {
			return "", fmt.Errorf("configured overlay archive digest does not match the accepted revision")
		}
		return p, nil
	}
	if p, ok := localsources.FindByHash(r.SearchRoots, r.overlaySize(), r.overlaySHA()); ok {
		r.log("found an existing overlay archive")
		return p, nil
	}
	return "", nil
}

// resolveBanks returns a path to an existing base bank directory, or "".
func (r *Request) resolveBanks() (string, error) {
	if p := strings.TrimSpace(r.BaseBanksDir); p != "" {
		fi, err := os.Stat(p)
		if err != nil || !fi.IsDir() {
			return "", fmt.Errorf("configured base bank directory is not readable: %s", p)
		}
		return p, nil
	}
	for _, root := range r.SearchRoots {
		if strings.TrimSpace(root) == "" {
			continue
		}
		if dir, ok := findBankDir(root, r.BankNaming, r.MinBankCount); ok {
			r.log("found an existing base bank directory")
			return dir, nil
		}
	}
	return "", nil
}

// findBankDir walks root for a directory holding at least min bank files that
// all parse as text banks.
func findBankDir(root string, naming BankNaming, min int) (string, bool) {
	var match string
	filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.IsDir() || match != "" {
			return nil
		}
		ents, err := os.ReadDir(path)
		if err != nil {
			return nil
		}
		var banks []string
		for _, e := range ents {
			name := e.Name()
			if e.IsDir() || !strings.HasPrefix(name, naming.Prefix) || !strings.HasSuffix(name, naming.Suffix) {
				continue
			}
			banks = append(banks, filepath.Join(path, name))
		}
		if len(banks) < min {
			return nil
		}
		for _, b := range banks {
			raw, err := os.ReadFile(b)
			if err != nil {
				return nil
			}
			if _, err := fmsb.Parse(raw); err != nil {
				return nil
			}
		}
		match = path
		return nil
	})
	return match, match != ""
}

// findOverlayIn returns the first regular file in dir whose size and digest match
// the accepted overlay revision.
func findOverlayIn(dir string, size int64, sha string) string {
	var found string
	filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || found != "" {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		fi, ierr := d.Info()
		if ierr != nil || fi.Size() != size {
			return nil
		}
		if sum, _, herr := hashutil.File(path); herr == nil && sum == sha {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// copyOverlay copies the overlay archive into the cache keeping its original
// file name, and returns the destination path.
func copyOverlay(src, dstDir string, expectSize int64, expectSHA string) (string, error) {
	raw, err := os.ReadFile(src)
	if err != nil {
		return "", fmt.Errorf("overlay archive: %w", err)
	}
	if int64(len(raw)) != expectSize {
		return "", fmt.Errorf("overlay archive size %d does not match the accepted %d", len(raw), expectSize)
	}
	if got := hashutil.Bytes(raw); got != expectSHA {
		return "", fmt.Errorf("overlay archive digest %s does not match the accepted revision", hashutil.Short(got))
	}
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(dstDir, filepath.Base(src))
	if err := atomic.WriteFile(dst, raw, atomic.Options{Overwrite: true}); err != nil {
		return "", err
	}
	return dst, nil
}

func copyBanks(srcDir, dstDir string, naming BankNaming) error {
	ents, err := os.ReadDir(srcDir)
	if err != nil {
		return fmt.Errorf("base banks: %w", err)
	}
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return err
	}
	var names []string
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, naming.Prefix) || !strings.HasSuffix(name, naming.Suffix) {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return fmt.Errorf("base banks: no %s*%s files found in %s", naming.Prefix, naming.Suffix, srcDir)
	}
	for _, name := range names {
		raw, err := os.ReadFile(filepath.Join(srcDir, name))
		if err != nil {
			return err
		}
		if _, err := fmsb.Parse(raw); err != nil {
			return fmt.Errorf("base bank %s: %w", name, err)
		}
		if err := atomic.WriteFile(filepath.Join(dstDir, name), raw, atomic.Options{Overwrite: true}); err != nil {
			return err
		}
	}
	return nil
}

func writeCacheState(req *Request, overlayPath, banksPath, membersPath string) error {
	st := cacheState{
		Schema:        cacheSchema,
		OverlayName:   filepath.Base(overlayPath),
		OverlaySize:   req.overlaySize(),
		OverlaySHA256: req.overlaySHA(),
		BankNaming:    req.BankNaming,
		BankFiles:     map[string]string{},
		SourceBase:    req.BaseContainer,
		SourceUpdate:  req.UpdateContainer,
	}
	ents, err := os.ReadDir(banksPath)
	if err != nil {
		return err
	}
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		sum, _, err := hashutil.File(filepath.Join(banksPath, e.Name()))
		if err != nil {
			return err
		}
		st.BankFiles[e.Name()] = sum
	}
	st.BaseBankCount = len(st.BankFiles)
	if len(req.BaseMembers) > 0 {
		st.MemberFiles = map[string]string{}
		ments, merr := os.ReadDir(membersPath)
		if merr != nil {
			return merr
		}
		for _, e := range ments {
			if e.IsDir() {
				continue
			}
			sum, _, herr := hashutil.File(filepath.Join(membersPath, e.Name()))
			if herr != nil {
				return herr
			}
			st.MemberFiles[e.Name()] = sum
		}
		st.BaseMemberCount = len(st.MemberFiles)
	}
	if _, err := os.Stat(overlayPath); err != nil {
		return err
	}
	return atomic.WriteJSON(filepath.Join(req.CacheDir, CacheStateFile), st, atomic.Options{Overwrite: true})
}

// ReadCachedOverlay returns the cached overlay archive path, when a valid cache
// state exists. It does not verify the archive itself.
func ReadCachedOverlay(cacheDir string) (string, string, bool) {
	raw, err := os.ReadFile(filepath.Join(cacheDir, CacheStateFile))
	if err != nil {
		return "", "", false
	}
	var st cacheState
	if err := json.Unmarshal(raw, &st); err != nil || st.OverlayName == "" {
		return "", "", false
	}
	p := filepath.Join(cacheDir, OverlayDir, st.OverlayName)
	if _, err := os.Stat(p); err != nil {
		return "", "", false
	}
	return p, st.OverlayName, true
}

func verifyCache(req *Request, overlayPath, banksPath, membersPath string) (*State, error) {
	raw, err := os.ReadFile(filepath.Join(req.CacheDir, CacheStateFile))
	if err != nil {
		return nil, err
	}
	var st cacheState
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, fmt.Errorf("cache state: %w", err)
	}
	if st.Schema != cacheSchema {
		return nil, fmt.Errorf("cache state schema %q is not supported", st.Schema)
	}
	if st.OverlaySHA256 != req.overlaySHA() || st.OverlaySize != req.overlaySize() {
		return nil, errors.New("cache state declares an unexpected overlay identity")
	}
	fi, err := os.Stat(overlayPath)
	if err != nil || fi.IsDir() {
		return nil, errors.New("cache overlay archive is missing")
	}
	if fi.Size() != req.overlaySize() {
		return nil, errors.New("cache overlay archive has the wrong size")
	}
	sum, _, err := hashutil.File(overlayPath)
	if err != nil {
		return nil, err
	}
	if sum != req.overlaySHA() {
		return nil, errors.New("cache overlay archive digest mismatch")
	}
	if len(st.BankFiles) < req.MinBankCount {
		return nil, fmt.Errorf("cache holds %d base bank(s), expected at least %d", len(st.BankFiles), req.MinBankCount)
	}
	for name, want := range st.BankFiles {
		got, _, err := hashutil.File(filepath.Join(banksPath, name))
		if err != nil {
			return nil, fmt.Errorf("cache base bank %s: %w", name, err)
		}
		if got != want {
			return nil, fmt.Errorf("cache base bank %s digest mismatch", name)
		}
	}
	out := &State{
		OverlayPath:   overlayPath,
		OverlayName:   filepath.Base(overlayPath),
		OverlaySHA256: req.overlaySHA(),
		OverlaySize:   req.overlaySize(),
		BaseBanksDir:  banksPath,
		BankCount:     len(st.BankFiles),
		CacheDir:      req.CacheDir,
		FromCache:     true,
	}
	if len(req.BaseMembers) > 0 {
		if len(st.MemberFiles) == 0 {
			return nil, errors.New("cache holds no base members; the required game-derived members must be extracted from the local containers")
		}
		for _, want := range req.BaseMembers {
			name := flattenMember(want)
			sum, ok := st.MemberFiles[name]
			if !ok {
				return nil, fmt.Errorf("cache does not hold the required base member %s", want)
			}
			got, _, err := hashutil.File(filepath.Join(membersPath, name))
			if err != nil {
				return nil, fmt.Errorf("cache base member %s: %w", name, err)
			}
			if got != sum {
				return nil, fmt.Errorf("cache base member %s digest mismatch", name)
			}
		}
		out.BaseMembersDir = membersPath
		out.MemberCount = len(st.MemberFiles)
	}
	return out, nil
}

// flattenMember maps a logical member path to its cache file name.
func flattenMember(member string) string {
	member = strings.TrimPrefix(strings.ReplaceAll(member, "\\", "/"), "/")
	return strings.ReplaceAll(member, "/", "__")
}

// missingDiagnostic explains exactly what must be supplied.
func (r *Request) missingDiagnostic(haveOverlay, haveBanks, haveMembers bool) error {
	var need []string
	if !haveOverlay {
		need = append(need, "the effective update overlay archive")
	}
	if !haveBanks {
		need = append(need, "the base text banks")
	}
	if !haveMembers {
		need = append(need, "the base game members the build consumes")
	}
	tools := ProbeTools()
	var have []string
	if tools.Hactool {
		have = append(have, "hactool")
	}
	if tools.Nstool {
		have = append(have, "nstool")
	}
	if tools.Python {
		have = append(have, "python3")
	}
	msg := fmt.Sprintf("extraction is required: %s could not be found in an existing cache or extraction", strings.Join(need, " and "))
	if len(have) == 0 {
		return fmt.Errorf("%s; no decryption helper is available and no extraction command is configured (supply -overlay/-banks or configure extractor_command)", msg)
	}
	return fmt.Errorf("%s; available helpers: %s (configure extractor_command to use them)", msg, strings.Join(have, ", "))
}
