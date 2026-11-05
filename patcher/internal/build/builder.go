// Package build applies a verified patch-data bundle to a validated game archive
// and produces a new archive. It never modifies its inputs and writes its output
// atomically.
package build

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"13s-patcher/internal/atomic"
	"13s-patcher/internal/cpk"
	"13s-patcher/internal/crilayla"
	"13s-patcher/internal/fmsb"
	"13s-patcher/internal/hashutil"
	"13s-patcher/internal/markup"
	"13s-patcher/internal/patchdata"
	"13s-patcher/internal/progress"
	"13s-patcher/internal/source"
)

// ErrCancelled is returned when the caller cancels a build.
var ErrCancelled = errors.New("build cancelled")

// RolePatchArchive is the required-source role naming the game patch archive.
const RolePatchArchive = "patch_archive"

// Resource is one archive member the caller contributes directly, independent
// of the patch-data bundle: a resource this patch adds. The caller supplies the
// logical content; the builder stores it with the same rules it uses for edited
// members and verifies it after the rebuild.
type Resource struct {
	// Member is the archive member path, e.g. "dir/name.ext".
	Member string
	// Logical is the uncompressed content.
	Logical []byte
	// Note is an optional short description used in reports.
	Note string
	// Raw stores the member verbatim (FileSize == ExtractSize, no CRILAYLA
	// wrapper) instead of compressing it. It is used for a resource whose base
	// counterpart is stored raw (the title texture atlas): the overlay member
	// must reproduce the base member's storage shape. The zero value keeps the
	// historical behaviour (compress when the content is at least the CRY
	// payload minimum).
	Raw bool
}

// Config is a build request.
type Config struct {
	SourceArchive string   // validated input archive (read-only)
	BundleDir     string   // patch-data bundle directory
	OutputPath    string   // destination archive
	Overwrite     bool     // allow replacing OutputPath
	Encoder       string   // "" = bundle default / "chain"
	ReadonlyRoots []string // paths writes must never touch
	ReportPath    string   // optional JSON report destination
	// BankDir holds logical base banks for members that the source archive does
	// not carry (for example, text banks introduced by the base release and only
	// edited here). When an operation targets such a member the bank is appended
	// to the archive as a new member. Empty means "no additions allowed".
	BankDir string
	// AllowRoots are paths that may be written even when they sit inside a
	// read-only root (the tool's own private state directory).
	AllowRoots []string
	// Resources are members the caller contributes directly (resources the
	// patch adds). They are appended to the archive after the bundle's
	// operations.
	Resources []Resource
	Ctx       context.Context
	// Progress, when set, receives structured progress events for the text-bank
	// rebuild, the resource additions and the archive write.
	Progress progress.Func
}

// MemberReport records one member's build outcome.
type MemberReport struct {
	Member           string `json:"member"`
	Mode             string `json:"mode"`
	Records          int    `json:"records"`
	Edits            int    `json:"edits"`
	OldStoredSize    int    `json:"old_stored_size"`
	NewStoredSize    int    `json:"new_stored_size"`
	OldExtractSize   int    `json:"old_extract_size"`
	NewExtractSize   int    `json:"new_extract_size"`
	OldLogicalSHA256 string `json:"old_logical_sha256"`
	NewLogicalSHA256 string `json:"new_logical_sha256"`
	NewStoredSHA256  string `json:"new_stored_sha256"`
	Storage          string `json:"storage"`
	SourceFile       string `json:"source_file,omitempty"`
	Note             string `json:"note,omitempty"`
}

// Check is one output verification result.
type Check struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Pass   bool   `json:"pass"`
	Detail string `json:"detail,omitempty"`
}

// Report is the per-build artifact.
type Report struct {
	SchemaVersion     int            `json:"schema_version"`
	Tool              string         `json:"tool"`
	SourceArchive     string         `json:"source_archive"`
	SourceSHA256      string         `json:"source_sha256"`
	OutputArchive     string         `json:"output_archive"`
	OutputSHA256      string         `json:"output_sha256"`
	OutputSize        int64          `json:"output_size"`
	BundleID          string         `json:"bundle_id"`
	BundleVersion     string         `json:"bundle_version"`
	ReleaseVersion    string         `json:"release_version,omitempty"`
	ManifestSHA256    string         `json:"manifest_sha256"`
	Encoder           string         `json:"encoder"`
	Operations        int            `json:"operations_applied"`
	MembersEdited     int            `json:"members_edited"`
	BanksAdded        int            `json:"banks_added_member"`
	MemberCountBefore int            `json:"member_count_before"`
	MemberCountAfter  int            `json:"member_count_after"`
	UnmodifiedIntact  bool           `json:"unmodified_members_byte_identical"`
	Members           []MemberReport `json:"members"`
	Checks            []Check        `json:"checks"`
	Verified          bool           `json:"verified"`
}

// Result is the outcome of one build. Bytes are not part of the JSON report.
type Result struct {
	OutputPath     string
	OutputSHA256   string
	OutputSize     int64
	SourceArchive  string
	SourceSHA256   string
	BundleID       string
	BundleVersion  string
	ManifestSHA256 string
	Operations     int
	MembersEdited  int
	Report         *Report
	Bytes          []byte
}

// Build validates the inputs, applies the bundle, verifies the result and writes
// the output atomically.
func Build(ctx context.Context, cfg Config) (*Result, error) {
	if cfg.Ctx != nil {
		ctx = cfg.Ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if cfg.SourceArchive == "" {
		return nil, fmt.Errorf("no source archive configured")
	}
	if cfg.BundleDir == "" {
		return nil, fmt.Errorf("no patch-data bundle configured")
	}
	if cfg.OutputPath == "" {
		return nil, fmt.Errorf("no output path configured")
	}

	bundle, err := patchdata.Load(cfg.BundleDir)
	if err != nil {
		return nil, err
	}
	spec, ok := bundle.SourceByRole(RolePatchArchive)
	if !ok {
		return nil, fmt.Errorf("bundle %s does not declare a %q source", bundle.Manifest.BundleID, RolePatchArchive)
	}
	id, err := source.Match(cfg.SourceArchive, spec)
	if err != nil {
		return nil, fmt.Errorf("source validation: %w", err)
	}

	outBytes, report, err := apply(ctx, cfg, bundle, id)
	if err != nil {
		return nil, err
	}
	report.OutputArchive = cfg.OutputPath
	report.OutputSHA256 = hashutil.Bytes(outBytes)
	report.OutputSize = int64(len(outBytes))

	if err := verifyOutput(bundle, outBytes, report, len(cfg.Resources)); err != nil {
		return nil, err
	}
	cfg.Progress.Emit(progress.Event{Op: "build", Stage: progress.StageVerify, Status: progress.StatusDone,
		Completed: 1, Total: 1, Note: "output constraints verified"})
	if err := guardOutput(cfg.OutputPath, cfg.ReadonlyRoots, cfg.AllowRoots); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, ErrCancelled
	}

	cfg.Progress.Emit(progress.Event{Op: "build", Stage: progress.StageCPK, Status: progress.StatusStart,
		Item: cfg.OutputPath, Total: int64(len(outBytes)), Note: cfg.OutputPath})
	// Only now — after the whole build and self-verification succeeded — is
	// anything written.
	if err := atomic.WriteFile(cfg.OutputPath, outBytes, atomic.Options{Overwrite: cfg.Overwrite}); err != nil {
		return nil, err
	}
	cfg.Progress.Emit(progress.Event{Op: "build", Stage: progress.StageCPK, Status: progress.StatusDone,
		Item: cfg.OutputPath, Completed: int64(len(outBytes)), Total: int64(len(outBytes)), Bytes: int64(len(outBytes)), Note: cfg.OutputPath})
	if cfg.ReportPath != "" {
		if err := guardOutput(cfg.ReportPath, cfg.ReadonlyRoots, cfg.AllowRoots); err != nil {
			return nil, err
		}
		// Reports are derived artifacts keyed by bundle id and are refreshed on
		// every build, so they may be replaced.
		if err := atomic.WriteJSON(cfg.ReportPath, report, atomic.Options{Overwrite: true}); err != nil {
			return nil, err
		}
	}
	res := &Result{
		OutputPath: cfg.OutputPath, OutputSHA256: report.OutputSHA256, OutputSize: report.OutputSize,
		SourceArchive: cfg.SourceArchive, SourceSHA256: id.SHA256,
		BundleID: bundle.Manifest.BundleID, BundleVersion: bundle.Manifest.BundleVersion,
		ManifestSHA256: bundle.ManifestSHA256, Operations: report.Operations, MembersEdited: report.MembersEdited,
		Report: report, Bytes: outBytes,
	}
	return res, nil
}

type memberWork struct {
	member string
	ops    []patchdata.Operation
}

func apply(ctx context.Context, cfg Config, bundle *patchdata.Bundle, id *source.Identity) ([]byte, *Report, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, ErrCancelled
	}
	raw, err := os.ReadFile(cfg.SourceArchive)
	if err != nil {
		return nil, nil, err
	}
	archive, err := cpk.Parse(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("source archive: %w", err)
	}

	encoder := cfg.Encoder
	if encoder == "" {
		encoder = bundle.Manifest.Output.Encoder
	}
	if encoder == "" {
		encoder = crilayla.EncoderChain
	}
	if encoder != crilayla.EncoderChain && encoder != crilayla.EncoderOld {
		return nil, nil, fmt.Errorf("unknown encoder %q", encoder)
	}

	report := &Report{
		SchemaVersion:     1,
		Tool:              ToolName + " " + ToolVersion,
		SourceArchive:     cfg.SourceArchive,
		SourceSHA256:      id.SHA256,
		BundleID:          bundle.Manifest.BundleID,
		BundleVersion:     bundle.Manifest.BundleVersion,
		ReleaseVersion:    bundle.Manifest.ReleaseVersion,
		ManifestSHA256:    bundle.ManifestSHA256,
		Encoder:           encoder,
		MemberCountBefore: len(archive.Members),
	}

	// Group operations by member, deterministically ordered.
	byMember := map[string]*memberWork{}
	for _, op := range bundle.Manifest.Operations {
		key := strings.ToLower(strings.ReplaceAll(op.Member, "\\", "/"))
		w := byMember[key]
		if w == nil {
			w = &memberWork{member: op.Member}
			byMember[key] = w
		}
		w.ops = append(w.ops, op)
	}
	order := make([]string, 0, len(byMember))
	for k := range byMember {
		order = append(order, k)
	}
	sort.Strings(order)

	var edits []cpk.MemberEdit
	var adds []cpk.MemberAdd
	expectedLogical := map[string][]byte{}
	addedKeys := map[string]string{} // member key -> dir/name
	maxID := uint32(0)
	for i := range archive.Members {
		if archive.Members[i].ID >= maxID {
			maxID = archive.Members[i].ID + 1
		}
	}
	cfg.Progress.Emit(progress.Event{Op: "build", Stage: progress.StageFMSB, Status: progress.StatusStart,
		Total: int64(len(order))})
	for i, key := range order {
		if err := ctx.Err(); err != nil {
			return nil, nil, ErrCancelled
		}
		w := byMember[key]
		sort.Slice(w.ops, func(i, j int) bool { return w.ops[i].RecordIndex < w.ops[j].RecordIndex })

		m := archive.MemberByPath(w.member)
		var logical []byte
		var br MemberReport
		if m != nil {
			var derr error
			logical, derr = archive.Logical(m)
			if derr != nil {
				return nil, nil, fmt.Errorf("%s: decode: %w", w.member, derr)
			}
			br = MemberReport{
				Member:           w.member,
				Mode:             "edit-existing",
				Records:          0,
				Edits:            len(w.ops),
				OldStoredSize:    len(m.Stored),
				OldExtractSize:   int(m.ExtractSize),
				OldLogicalSHA256: hashutil.Bytes(logical),
			}
		} else {
			if cfg.BankDir == "" {
				return nil, nil, fmt.Errorf("operation targets member %q which is not present in the source archive (no base bank directory supplied)", w.member)
			}
			srcFile, rerr := resolveBankFile(cfg.BankDir, w.member)
			if rerr != nil {
				return nil, nil, rerr
			}
			logical, rerr = os.ReadFile(srcFile)
			if rerr != nil {
				return nil, nil, fmt.Errorf("%s: base bank file: %w", w.member, rerr)
			}
			br = MemberReport{
				Member:           w.member,
				Mode:             "add-member",
				Edits:            len(w.ops),
				OldExtractSize:   len(logical),
				OldLogicalSHA256: hashutil.Bytes(logical),
				SourceFile:       srcFile,
			}
		}
		if op0 := w.ops[0]; op0.MemberLogicalSHA256 != "" {
			if got := hashutil.Bytes(logical); got != op0.MemberLogicalSHA256 {
				return nil, nil, fmt.Errorf("%s: member logical sha256 %s != required %s (unsupported source revision)", w.member, got, op0.MemberLogicalSHA256)
			}
		}
		bank, err := fmsb.Parse(logical)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: not a text bank: %w", w.member, err)
		}
		br.Records = len(bank.Strings)
		for _, op := range w.ops {
			if op.RecordIndex >= len(bank.Strings) {
				return nil, nil, fmt.Errorf("%s: operation %s record index %d out of range (%d)", w.member, op.ID, op.RecordIndex, len(bank.Strings))
			}
			orig := string(bank.Strings[op.RecordIndex])
			if got := hashutil.Bytes([]byte(orig)); got != op.OriginalSHA256 {
				return nil, nil, fmt.Errorf("%s#%d (operation %s): original text does not match the bundle (source revision mismatch)", w.member, op.RecordIndex, op.ID)
			}
			replacement := bundle.Replacement[op.ID]
			if replacement == "" {
				return nil, nil, fmt.Errorf("%s: operation %s has an empty replacement", w.member, op.ID)
			}
			if replacement == orig {
				return nil, nil, fmt.Errorf("%s: operation %s is a no-op", w.member, op.ID)
			}
			if op.PreserveTokens && !op.AllowTokenChange {
				if ok, detail := markup.Equivalent(orig, replacement); !ok {
					return nil, nil, fmt.Errorf("%s: operation %s changes control tokens (%s)", w.member, op.ID, detail)
				}
			}
			bank.Strings[op.RecordIndex] = []byte(replacement)
		}
		newLogical := bank.Rebuild(bank.Strings)
		wasCompressed := m != nil && m.FileSize != m.ExtractSize
		payload, compressed, err := memberStorage(newLogical, wasCompressed, encoder)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", w.member, err)
		}
		br.Storage = storageName(compressed)
		if m != nil && wasCompressed && !compressed {
			br.Note = fmt.Sprintf("stored raw: edited text is %d byte(s), below the CRY payload minimum", len(newLogical))
		}
		br.NewStoredSize = len(payload)
		br.NewExtractSize = len(newLogical)
		br.NewLogicalSHA256 = hashutil.Bytes(newLogical)
		br.NewStoredSHA256 = hashutil.Bytes(payload)
		report.Members = append(report.Members, br)
		report.MembersEdited++
		report.Operations += len(w.ops)
		if m != nil {
			edits = append(edits, cpk.MemberEdit{RowIndex: m.RowIndex, Payload: payload, ExtractSize: uint32(len(newLogical))})
		} else {
			dir, name := splitMemberPath(w.member)
			if dir == "" || name == "" {
				return nil, nil, fmt.Errorf("%s: cannot split the member path", w.member)
			}
			adds = append(adds, cpk.MemberAdd{Dir: dir, Name: name, ID: maxID, Stored: payload, ExtractSize: uint32(len(newLogical))})
			addedKeys[key] = dir + "/" + name
			maxID++
			report.BanksAdded++
		}
		expectedLogical[key] = newLogical
		cfg.Progress.Emit(progress.Event{Op: "build", Stage: progress.StageFMSB, Status: progress.StatusProgress,
			Item: w.member, Completed: int64(i + 1), Total: int64(len(order)), Bytes: int64(br.NewStoredSize)})
	}

	// Caller-contributed resources: members the patch adds directly.
	cfg.Progress.Emit(progress.Event{Op: "build", Stage: progress.StageFMSB, Status: progress.StatusDone,
		Completed: int64(len(order)), Total: int64(len(order))})
	cfg.Progress.Emit(progress.Event{Op: "build", Stage: progress.StageCompress, Status: progress.StatusDone,
		Completed: int64(len(edits) + len(adds)), Total: int64(len(edits) + len(adds))})
	resources := append([]Resource(nil), cfg.Resources...)
	sort.Slice(resources, func(i, j int) bool { return resources[i].Member < resources[j].Member })
	cfg.Progress.Emit(progress.Event{Op: "build", Stage: progress.StageTech, Status: progress.StatusStart,
		Total: int64(len(resources))})
	for ri, r := range resources {
		if err := ctx.Err(); err != nil {
			return nil, nil, ErrCancelled
		}
		key := strings.ToLower(strings.ReplaceAll(r.Member, "\\", "/"))
		if _, dup := expectedLogical[key]; dup {
			return nil, nil, fmt.Errorf("resource %s duplicates a member the bundle already edits", r.Member)
		}
		if m := archive.MemberByPath(r.Member); m != nil {
			return nil, nil, fmt.Errorf("resource %s already exists in the source archive", r.Member)
		}
		dir, name := splitMemberPath(r.Member)
		if dir == "" || name == "" {
			return nil, nil, fmt.Errorf("resource %s: cannot split the member path", r.Member)
		}
		if len(r.Logical) == 0 {
			return nil, nil, fmt.Errorf("resource %s: empty content", r.Member)
		}
		payload, compressed, err := memberStorage(r.Logical, !r.Raw, encoder)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", r.Member, err)
		}
		note := r.Note
		if r.Raw {
			if note != "" {
				note += " · "
			}
			note += "stored raw (FileSize == ExtractSize, no CRILAYLA wrapper)"
		}
		report.Members = append(report.Members, MemberReport{
			Member: r.Member, Mode: "add-resource", Storage: storageName(compressed),
			OldExtractSize: len(r.Logical), NewExtractSize: len(r.Logical),
			OldLogicalSHA256: hashutil.Bytes(r.Logical), NewLogicalSHA256: hashutil.Bytes(r.Logical),
			NewStoredSize: len(payload), NewStoredSHA256: hashutil.Bytes(payload), Note: note,
		})
		adds = append(adds, cpk.MemberAdd{Dir: dir, Name: name, ID: maxID, Stored: payload, ExtractSize: uint32(len(r.Logical))})
		addedKeys[key] = dir + "/" + name
		maxID++
		report.BanksAdded++
		expectedLogical[key] = r.Logical
		cfg.Progress.Emit(progress.Event{Op: "build", Stage: progress.StageTech, Status: progress.StatusProgress,
			Item: r.Member, Completed: int64(ri + 1), Total: int64(len(resources)), Bytes: int64(len(r.Logical))})
	}
	cfg.Progress.Emit(progress.Event{Op: "build", Stage: progress.StageTech, Status: progress.StatusDone,
		Completed: int64(len(resources)), Total: int64(len(resources))})

	if len(edits) == 0 && len(adds) == 0 {
		return nil, nil, fmt.Errorf("bundle contains no operations to apply")
	}
	var out []byte
	if len(adds) > 0 {
		out, err = archive.BuildWithAdd(edits, adds)
	} else {
		out, err = archive.BuildLayout(edits, nil)
	}
	if err != nil {
		return nil, nil, err
	}
	if err := selfVerify(out, archive, edits, adds, expectedLogical, addedKeys); err != nil {
		return nil, nil, err
	}
	report.UnmodifiedIntact = true
	report.MemberCountAfter = len(archive.Members) + len(adds)
	sort.Slice(report.Members, func(i, j int) bool { return report.Members[i].Member < report.Members[j].Member })
	return out, report, nil
}

// BankFileBase maps a member path to the flat base bank file name, e.g.
// "Fms/ScriptText.fms" -> "Fms__ScriptText.fms".
func BankFileBase(member string) string {
	clean := strings.TrimPrefix(strings.ReplaceAll(member, "\\", "/"), "/")
	return strings.ReplaceAll(clean, "/", "__")
}

// resolveBankFile finds the base bank file for a member, matching the on-disk
// name case-insensitively so the extraction's capitalisation never matters.
func resolveBankFile(dir, member string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		return "", fmt.Errorf("%s: no base bank directory supplied", member)
	}
	want := BankFileBase(member)
	exact := filepath.Join(dir, want)
	if fi, err := os.Stat(exact); err == nil && !fi.IsDir() {
		return exact, nil
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("%s: base bank directory: %w", member, err)
	}
	lower := strings.ToLower(want)
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		if strings.ToLower(e.Name()) == lower {
			return filepath.Join(dir, e.Name()), nil
		}
	}
	return "", fmt.Errorf("%s: base bank file %s not found in %s", member, want, dir)
}

// splitMemberPath splits "dir/name" into its parts.
func splitMemberPath(member string) (string, string) {
	clean := strings.TrimPrefix(strings.ReplaceAll(member, "\\", "/"), "/")
	i := strings.LastIndex(clean, "/")
	if i <= 0 || i == len(clean)-1 {
		return "", ""
	}
	return clean[:i], clean[i+1:]
}

func storageName(compressed bool) string {
	if compressed {
		return "compressed"
	}
	return "raw"
}

func memberStorage(newLogical []byte, wasCompressed bool, encoder string) (payload []byte, storedCompressed bool, err error) {
	if len(newLogical) < 0x100 || !wasCompressed {
		return newLogical, false, nil
	}
	defer func() {
		if r := recover(); r != nil {
			payload, storedCompressed, err = nil, false, fmt.Errorf("compression panic for %d bytes: %v", len(newLogical), r)
		}
	}()
	blk, err := crilayla.Encode(newLogical, encoder)
	if err != nil {
		return nil, false, err
	}
	return blk, true, nil
}

// selfVerify re-parses the built archive and checks every invariant.
func selfVerify(out []byte, src *cpk.Archive, edits []cpk.MemberEdit, adds []cpk.MemberAdd, expectedLogical map[string][]byte, addedKeys map[string]string) error {
	vc, err := cpk.Parse(out)
	if err != nil {
		return fmt.Errorf("rebuilt archive failed to re-parse: %w", err)
	}
	editedRows := map[int]bool{}
	for _, e := range edits {
		editedRows[e.RowIndex] = true
	}
	for i := range src.Members {
		old := src.Members[i]
		nm := vc.MemberByPath(string(old.Dir) + "/" + string(old.FileName))
		if nm == nil {
			return fmt.Errorf("rebuilt archive lost member %s/%s", old.Dir, old.FileName)
		}
		if !editedRows[old.RowIndex] {
			if string(nm.Stored) != string(old.Stored) {
				return fmt.Errorf("unmodified member changed: %s/%s", old.Dir, old.FileName)
			}
			if nm.FileSize != old.FileSize || nm.ExtractSize != old.ExtractSize {
				return fmt.Errorf("unmodified member sizes changed: %s/%s", old.Dir, old.FileName)
			}
		}
	}
	for key, want := range expectedLogical {
		p := key
		if added, ok := addedKeys[key]; ok {
			p = added
		}
		nm := vc.MemberByPath(p)
		if nm == nil {
			return fmt.Errorf("rebuilt archive missing %s", p)
		}
		lg, err := vc.Logical(nm)
		if err != nil {
			return fmt.Errorf("%s: decode after rebuild: %w", p, err)
		}
		if string(lg) != string(want) {
			return fmt.Errorf("%s: logical content mismatch after rebuild", p)
		}
	}
	if len(vc.Members) != len(src.Members)+len(adds) {
		return fmt.Errorf("member count %d != %d + %d", len(vc.Members), len(src.Members), len(adds))
	}
	return nil
}

// verifyOutput checks the built bytes against the bundle's output constraints.
// extraAdds is the number of caller-contributed resources, which the bundle
// manifest cannot know about.
func verifyOutput(bundle *patchdata.Bundle, out []byte, report *Report, extraAdds int) error {
	oc := bundle.Manifest.Output
	add := func(id, title string, ok bool, detail string) {
		report.Checks = append(report.Checks, Check{ID: id, Title: title, Pass: ok, Detail: detail})
	}
	delta := report.MemberCountAfter - report.MemberCountBefore
	add("output.member_count", "member count delta matches the bundle", delta == oc.MemberCountDelta+extraAdds,
		fmt.Sprintf("%d (want %d)", delta, oc.MemberCountDelta+extraAdds))
	add("output.unmodified", "unmodified members byte-identical", report.UnmodifiedIntact, "")
	digest := hashutil.Bytes(out)
	add("output.rebuildable", "output re-parses", true, "")
	if oc.ExpectedSize != 0 {
		add("output.size", "output size matches the bundle", int64(len(out)) == oc.ExpectedSize,
			fmt.Sprintf("%d (want %d)", len(out), oc.ExpectedSize))
	}
	if oc.ExpectedSHA256 != "" {
		add("output.sha256", "output digest matches the bundle", digest == oc.ExpectedSHA256,
			fmt.Sprintf("%s (want %s)", hashutil.Short(digest), hashutil.Short(oc.ExpectedSHA256)))
	}
	for _, c := range report.Checks {
		if !c.Pass {
			return fmt.Errorf("output verification failed: %s (%s)", c.Title, c.Detail)
		}
	}
	report.Verified = true
	return nil
}

func guardOutput(out string, roots, allow []string) error {
	if strings.TrimSpace(out) == "" {
		return fmt.Errorf("output path is empty")
	}
	outAbs, err := filepath.Abs(out)
	if err != nil {
		return err
	}
	for _, a := range allow {
		if strings.TrimSpace(a) == "" {
			continue
		}
		if aAbs, err := filepath.Abs(a); err == nil && pathWithin(outAbs, aAbs) {
			return nil
		}
	}
	for _, r := range roots {
		if strings.TrimSpace(r) == "" {
			continue
		}
		rAbs, err := filepath.Abs(r)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(rAbs, outAbs)
		_ = rel
		if err != nil {
			continue
		}
		if pathWithin(outAbs, rAbs) {
			return fmt.Errorf("refusing to write inside read-only root %s: %s", rAbs, outAbs)
		}
	}
	return nil
}

// pathWithin reports whether p is inside dir (or equals it).
func pathWithin(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..")
}
