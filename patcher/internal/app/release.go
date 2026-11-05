package app

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"13s-patcher/internal/build"
	"13s-patcher/internal/hashutil"
	"13s-patcher/internal/modversion"
	"13s-patcher/internal/packaging"
	"13s-patcher/internal/plateorder"
	"13s-patcher/internal/progress"
	"13s-patcher/internal/techdata"
	"13s-patcher/internal/texpack"
	"13s-patcher/internal/translations"
)

// ResourcesDirName is the state subdirectory that holds optional user overrides
// for images the added resources are composed over.
const ResourcesDirName = "resources"

// ShippedPlateName is the project's own presentation plate, shipped in the
// public tree under assets/images. It is the default background for the added
// end-of-route page; a user override is optional.
const ShippedPlateName = "end_route_plate.png"

// sourcePathOr returns the archive a build should read: the located source when
// known, otherwise the verified cache overlay.
func sourcePathOr(rep *PreflightReport, res *prepareResult) string {
	if rep.Source.Path != "" {
		return rep.Source.Path
	}
	if res.State != nil {
		return res.State.OverlayPath
	}
	return ""
}

// Target resolves the configured installation target.
func (s *Service) Target() packaging.Target {
	t, err := packaging.ParseTarget(s.Cfg.Target)
	if err != nil {
		return packaging.TargetBoth
	}
	return t
}

// ResourcesDir is where optional user overrides for composed images are
// discovered.
func (s *Service) ResourcesDir() string { return filepath.Join(s.StateDir(), ResourcesDirName) }

// PlateBasePath resolves the explicit background image path, or "".
func (s *Service) PlateBasePath() string {
	if p := strings.TrimSpace(s.Cfg.PlateBase); p != "" {
		return s.resolve(p)
	}
	return ""
}

// shippedPlateDirs lists the directories the project's presentation plate may
// live in, most authoritative first. It resolves relative to the actual project
// root (found the same way version.txt is), never to the shell working
// directory, and falls back to an upward walk from the executable so a built
// binary run from anywhere still finds the shipped asset.
func (s *Service) shippedPlateDirs() []string {
	var out []string
	if s.PlateAssetsDir != "" {
		out = append(out, s.PlateAssetsDir)
	}
	if root := modversion.FindRoot(s.WorkDir, s.Loc.Root); root != "" {
		out = append(out, filepath.Join(root, "patcher", "assets", "images"))
		out = append(out, filepath.Join(root, "assets", "images"))
	}
	if exe, err := os.Executable(); err == nil {
		out = appendAssetWalkUps(out, filepath.Dir(exe), "images")
	}
	out = appendAssetWalkUps(out, s.WorkDir, "images")
	return dedupe(out)
}

// ShippedPlatePath returns the first existing shipped presentation plate, or
// "" when the public asset is not present where the tool can see it.
func (s *Service) ShippedPlatePath() string {
	for _, d := range s.shippedPlateDirs() {
		p := filepath.Join(d, ShippedPlateName)
		if fileIsFile(p) {
			return p
		}
	}
	return ""
}

// ResourceStatus describes one added resource.
type ResourceStatus struct {
	Member        string `json:"member"`
	Kind          string `json:"kind"`
	Present       bool   `json:"present"`
	LogicalSize   int64  `json:"logical_size,omitempty"`
	LogicalSHA256 string `json:"logical_sha256,omitempty"`
	// Storage is the archive storage shape the resource is written with:
	// "raw" (FileSize == ExtractSize, no CRILAYLA wrapper) or "compressed".
	Storage string `json:"storage,omitempty"`
	Base    string `json:"base,omitempty"`
	Detail  string `json:"detail,omitempty"`
	Error   string `json:"error,omitempty"`
	// RuntimeStatus carries the last runtime observation for this
	// resource, verbatim from the patch descriptor. A static PASS never
	// overrides it.
	RuntimeStatus string `json:"runtime_status,omitempty"`
	// RendererVersion is the footer renderer implementation that produced a
	// composed title member (0 for other resources).
	RendererVersion int `json:"renderer_version,omitempty"`
	// FontSHA256 is the digest of the display face read from disk for a
	// composed title member.
	FontSHA256 string `json:"font_sha256,omitempty"`
	// CacheIdentity provably identifies the composed member's inputs
	// (renderer, font bytes, normalised style, label).
	CacheIdentity string `json:"cache_identity,omitempty"`
	// Composition classifies the live face/style as the runtime-confirmed
	// reference ("reference"), the shipped configuration ("shipped") or a
	// user-modified one ("modified").
	Composition string `json:"composition,omitempty"`
	// RuntimeVerified reports the runtime-confirmed state of the exact
	// composition: true only for the historical reference composition (the
	// recorded 2026-10-19 face with the built-in default style). The shipped
	// face/style pair has no game run behind it, so a normal build is false.
	RuntimeVerified bool `json:"runtime_verified"`
}

// StateLabel is the human label for the resource's build state. "withheld"
// means the settings deliberately excluded it (not an error and not a missing
// input): the last-known-good build shape.
func (r ResourceStatus) StateLabel() string {
	if r.Present {
		return "present"
	}
	if strings.TrimSpace(r.Error) != "" {
		return "missing: " + r.Error
	}
	if strings.Contains(r.Detail, "withheld") {
		return "withheld by settings"
	}
	return "absent"
}

// PatchComponents is the itemised content of the patch both targets carry.
type PatchComponents struct {
	TranslationsRecords int              `json:"translations_records"`
	TranslationsSHA256  string           `json:"translations_sha256,omitempty"`
	BuildID             string           `json:"build_id"`
	TitleID             string           `json:"title_id"`
	ExecutablePatchSize int64            `json:"executable_patch_size"`
	ExecutablePatchSHA  string           `json:"executable_patch_sha256"`
	ExecutablePatchRecs int              `json:"executable_patch_records"`
	AddedResources      []ResourceStatus `json:"added_resources,omitempty"`
	// TitleBranding is the effective in-game title-branding state of this
	// build. It carries the footer label actually rendered (from version.txt,
	// never invented), whether the member was included, and the resource's
	// recorded runtime status, so every report states plainly whether the
	// member is present and whether its exact composition is confirmed.
	TitleBranding TitleBrandingStatus `json:"title_branding"`
	// Incomplete reports that a component the patch normally carries was not
	// produced because allow_missing_resources was explicitly enabled. Such a
	// build is a deliberate partial build and never a complete one.
	Incomplete bool     `json:"incomplete,omitempty"`
	Omitted    []string `json:"omitted,omitempty"`
	Notes      []string `json:"notes,omitempty"`
}

// TitleBrandingStatus is the effective state of the in-game title-screen
// footer branding.
type TitleBrandingStatus struct {
	// Enabled mirrors the persisted title_branding selection.
	Enabled bool `json:"enabled"`
	// Member is the archive member the resource replaces (GUI/title.ftx).
	Member string `json:"member"`
	// Included reports whether the member was actually added to the archive.
	Included bool `json:"included"`
	// Storage is the archive storage shape of the included member: "raw"
	// (FileSize == ExtractSize, no CRILAYLA wrapper, matching the base atlas)
	// or "compressed". Empty when the member is withheld.
	Storage string `json:"storage,omitempty"`
	// Footer is the rendered label (e.g. "Unburger Mod v0.1.0"); empty when
	// the resource is withheld or version.txt cannot be read.
	Footer string `json:"footer,omitempty"`
	// FooterError carries the version.txt diagnostic when the label could not
	// be resolved; it is never a substitute for an invented number.
	FooterError string `json:"footer_error,omitempty"`
	// RuntimeStatus is the recorded runtime observation, verbatim from the
	// patch descriptor. It is never a success claim.
	RuntimeStatus string `json:"runtime_status,omitempty"`
	// Composition classifies the live face/style as the runtime-confirmed
	// reference ("reference"), the shipped configuration ("shipped") or a
	// user-modified one ("modified").
	Composition string `json:"composition,omitempty"`
	// RuntimeVerified reports the runtime-confirmed state of the exact
	// composition: true only for the historical reference composition (the
	// recorded 2026-10-19 face with the built-in default style). The shipped
	// face/style pair has no game run behind it, so a normal build is false.
	RuntimeVerified bool `json:"runtime_verified"`
	// RendererVersion is the footer renderer implementation version.
	RendererVersion int `json:"renderer_version,omitempty"`
	// FontSHA256 is the digest of the display face actually read from disk.
	FontSHA256 string `json:"font_sha256,omitempty"`
	// CacheIdentity provably identifies the composed member's inputs
	// (renderer, font bytes, normalised style, label).
	CacheIdentity string `json:"cache_identity,omitempty"`
}

// StorageLabel is the human label for the included member's storage shape
// ("RAW" for a verbatim member that matches a raw base atlas, "compressed" for
// a CRILAYLA-wrapped member, and empty while the member is withheld).
func (t TitleBrandingStatus) StorageLabel() string {
	switch t.Storage {
	case "raw":
		return "RAW"
	case "compressed":
		return "compressed"
	}
	return ""
}

// resolvePlateBase finds the background image an added resource is composed
// over. Resolution order is: an explicit setting, then the tool's own resource
// directory, then the presentation plate shipped in the public tree, then a
// matching image in the search roots. The shipped asset is the standard default,
// so a fresh clone builds without any user-supplied image; the first two steps
// let a user override it deliberately.
func (s *Service) resolvePlateBase(hint string) (string, string, error) {
	if p := s.PlateBasePath(); p != "" {
		if fileIsFile(p) {
			return p, "configured path", nil
		}
		return "", "", fmt.Errorf("configured background image not found: %s", p)
	}
	dir := s.ResourcesDir()
	for _, name := range baseCandidateNames(hint) {
		p := filepath.Join(dir, name)
		if fileIsFile(p) {
			return p, "resource directory", nil
		}
	}
	if p := s.ShippedPlatePath(); p != "" {
		return p, "shipped project asset", nil
	}
	for _, root := range s.SearchRoots() {
		for _, name := range baseCandidateNames(hint) {
			p := filepath.Join(root, name)
			if fileIsFile(p) {
				return p, "search root", nil
			}
		}
	}
	return "", "", fmt.Errorf("presentation plate not found: expected the shipped asset %s under %s, an optional override in %s, or a matching image in the search roots", ShippedPlateName, filepath.Join("patcher", "assets", "images"), dir)
}

// baseCandidateNames lists the file names accepted for a background hint.
func baseCandidateNames(hint string) []string {
	var out []string
	if hint != "" {
		base := strings.ReplaceAll(hint, "/", "__")
		out = append(out, base+".png", base+".jpg")
	}
	out = append(out, "plate_base.png", "plate_base.jpg")
	return out
}

// resolveTitleBase finds the user-derived title texture atlas that the title
// footer branding is composed over. The atlas is game-owned artwork, so it is
// never shipped: the user extracts GUI/title.ftx from their own copy and drops
// it in the tool's resource directory (or a configured search root), exactly
// like the manual-page background image.
func (s *Service) resolveTitleBase(hint string) (string, string, error) {
	names := titleBaseNames(hint)
	dir := s.ResourcesDir()
	for _, name := range names {
		if p := filepath.Join(dir, name); fileIsFile(p) {
			return p, "resource directory", nil
		}
	}
	for _, root := range s.SearchRoots() {
		for _, name := range names {
			for _, cand := range []string{filepath.Join(root, name), filepath.Join(root, "base_members", name)} {
				if fileIsFile(cand) {
					return cand, "search root", nil
				}
			}
		}
	}
	roots := s.SearchRoots()
	where := "the configured search roots (none configured)"
	if len(roots) > 0 {
		where = "the search roots " + strings.Join(roots, ", ")
	}
	return "", "", fmt.Errorf("title texture atlas not found: rebuild the verified cache from your own containers so GUI/title.ftx is extracted, or save it as %s in %s, or in %s", names[0], dir, where)
}

// TitleResource describes the patch's title-branding resource, when present.
func TitleResource() (member, baseHint, baseLabel, runtimeStatus string, ok bool) {
	for _, r := range techdata.AddedResources() {
		if r.Kind == "compose_title" {
			return r.Member, r.BaseHint, r.BaseLabel, r.RuntimeStatus, true
		}
	}
	return "", "", "", "", false
}

// ResolveTitleBase resolves the user-derived title texture atlas the footer
// branding is composed over. It is exported for the preflight check so a
// missing base is reported before a build starts, with the same actionable
// message the build uses.
func (s *Service) ResolveTitleBase() (string, string, error) {
	_, hint, _, _, ok := TitleResource()
	if !ok {
		return "", "", nil
	}
	return s.resolveTitleBase(hint)
}

// TitleBrandingStatus reports the effective in-game title-branding state for
// the current settings: the selection, whether the base is resolvable, and the
// footer label rendered from version.txt. It never invents a version.
func (s *Service) titleBrandingStatus(resources []ResourceStatus) TitleBrandingStatus {
	st := TitleBrandingStatus{Enabled: s.Cfg.TitleBrandingEnabled(), Member: texpack.TitleMember, RuntimeVerified: false}
	for _, r := range resources {
		if r.Kind == "compose_title" {
			st.Member = r.Member
			st.Included = r.Present
			st.Storage = r.Storage
			st.RuntimeStatus = r.RuntimeStatus
			st.RendererVersion = r.RendererVersion
			st.FontSHA256 = r.FontSHA256
			st.CacheIdentity = r.CacheIdentity
			st.Composition = r.Composition
			st.RuntimeVerified = r.Present && r.RuntimeVerified
		}
	}
	if !st.Enabled {
		return st
	}
	if v, err := s.ModVersion(); err == nil {
		st.Footer = modversion.Display(v)
	} else {
		st.FooterError = err.Error()
	}
	return st
}

// titleBaseNames lists the accepted file names for the base title atlas.
func titleBaseNames(hint string) []string {
	out := []string{}
	if hint != "" {
		out = append(out, strings.ReplaceAll(hint, "/", "__"))
	}
	out = append(out, texpack.TitleBaseCandidateNames()...)
	seen := map[string]bool{}
	var uniq []string
	for _, n := range out {
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		uniq = append(uniq, n)
	}
	return uniq
}

// resolveBaseMember locates a user-derived base archive member. The file name is
// the member path with "/" replaced by "__", the same convention the extraction
// uses for the base text banks, so "GUI/change_kurabe.mbs" is read from
// "GUI__change_kurabe.mbs". The member is game-owned artwork/script data, so it
// is never shipped: the user extracts it from their own copy.
func (s *Service) resolveBaseMember(hint string) (string, string, error) {
	member := strings.TrimPrefix(strings.ReplaceAll(hint, "\\", "/"), "/")
	name := strings.ReplaceAll(member, "/", "__")
	if name == "" {
		return "", "", fmt.Errorf("base member hint is empty")
	}
	dir := s.ResourcesDir()
	if p := filepath.Join(dir, name); fileIsFile(p) {
		return p, "resource directory", nil
	}
	for _, root := range s.SearchRoots() {
		for _, cand := range []string{filepath.Join(root, name), filepath.Join(root, "base_members", name)} {
			if fileIsFile(cand) {
				return cand, "search root", nil
			}
		}
	}
	return "", "", fmt.Errorf("%s not found: rebuild the verified cache from your own containers so %s is extracted, or save it as %s in %s, or in a base_members/ directory under one of the search roots", name, member, name, dir)
}

// plateOrderSpec converts one descriptor into the transform spec.
func plateOrderSpec(r techdata.AddedResource) (plateorder.Spec, error) {
	if r.Transform == nil {
		return plateorder.Spec{}, fmt.Errorf("added resource %s: missing transform descriptor", r.Member)
	}
	spec := plateorder.Spec{
		Member:     r.Member,
		BaseSize:   int(r.BaseSize),
		BaseSHA256: r.BaseSHA256,
		S4Offset:   r.Transform.S4Offset,
		PairSpec: plateorder.PairSpec{
			GivenBlock:   r.Transform.GivenBlock,
			SurnameBlock: r.Transform.SurnameBlock,
			Keyframes:    r.Transform.Keyframes,
			GivenTXShift: r.Transform.GivenTXShift,
			GivenTXBasis: r.Transform.GivenTXBasis,
			PairTXShift:  r.Transform.PairTXShift,
			PairTXBasis:  r.Transform.PairTXBasis,
			GivenLabel:   r.Transform.GivenName,
			SurnameLabel: r.Transform.SurnameName,
			Slot:         r.Transform.Slot,
		},
	}
	if sp := r.Transform.SecondPair; sp != nil {
		spec.Second = &plateorder.PairSpec{
			GivenBlock:   sp.GivenBlock,
			SurnameBlock: sp.SurnameBlock,
			Keyframes:    sp.Keyframes,
			GivenTXShift: sp.GivenTXShift,
			GivenTXBasis: sp.GivenTXBasis,
			PairTXShift:  sp.PairTXShift,
			PairTXBasis:  sp.PairTXBasis,
			GivenLabel:   r.Transform.GivenName,
			SurnameLabel: r.Transform.SurnameName,
			Slot:         sp.Slot,
		}
	}
	return spec, nil
}

// ResolvePlateBase resolves one story-plate base script. It is exported for the
// preflight check so a missing member is reported before a build starts.
func (s *Service) ResolvePlateBase(r techdata.AddedResource) (string, string, error) {
	return s.resolveBaseMember(r.BaseHint)
}

// buildResources turns the added-resource descriptors into archive members.
// When a resource cannot be produced it returns an error (never a silent
// omission) unless the user has explicitly allowed missing resources.
func (s *Service) buildResources() ([]build.Resource, []ResourceStatus, error) {
	desc := techdata.AddedResources()
	var out []build.Resource
	var status []ResourceStatus
	overlay, err := techdata.OverlayImage()
	if err != nil {
		return nil, nil, fmt.Errorf("added resource overlay is unavailable: %w", err)
	}
	var missing []string
	for _, r := range desc {
		st := ResourceStatus{Member: r.Member, Kind: r.Kind, RuntimeStatus: r.RuntimeStatus}
		// A resource whose last runtime observation is a failure is only
		// produced when the settings explicitly enable it; the default build is
		// the last-known-good shape. The decision is recorded, never silent.
		if r.Kind == "compose_title" && !s.Cfg.TitleBrandingEnabled() {
			st.Detail = "withheld: title_branding=false in settings; the footer member is not added"
			status = append(status, st)
			continue
		}
		switch r.Kind {
		case "compose":
			basePath, how, berr := s.resolvePlateBase(r.BaseHint)
			if berr != nil {
				if !s.Cfg.AllowMissingResources {
					st.Error = berr.Error()
					status = append(status, st)
					missing = append(missing, fmt.Sprintf("%s (needs %s: %s)", r.Member, r.BaseLabel, berr))
					continue
				}
				st.Error = berr.Error() + " (allowed by settings)"
				status = append(status, st)
				continue
			}
			baseRaw, rerr := os.ReadFile(basePath)
			if rerr != nil {
				return nil, nil, fmt.Errorf("%s: %w", basePath, rerr)
			}
			baseImg, derr := texpack.DecodeImage(baseRaw)
			if derr != nil {
				return nil, nil, derr
			}
			ovImg, derr := texpack.DecodeImage(overlay)
			if derr != nil {
				return nil, nil, derr
			}
			logical, cerr := texpack.ComposeManualPage(baseImg, ovImg)
			if cerr != nil {
				return nil, nil, cerr
			}
			if r.LogicalSize != 0 && int64(len(logical)) != r.LogicalSize {
				return nil, nil, fmt.Errorf("%s: composed size %d does not match the expected %d", r.Member, len(logical), r.LogicalSize)
			}
			out = append(out, build.Resource{Member: r.Member, Logical: logical, Note: r.Description})
			st.Present = true
			st.LogicalSize = int64(len(logical))
			st.LogicalSHA256 = hashutil.Bytes(logical)
			st.Base = basePath
			st.Detail = "composed from " + how
			status = append(status, st)
		case "compose_title":
			basePath, how, berr := s.resolveTitleBase(r.BaseHint)
			if berr != nil {
				if !s.Cfg.AllowMissingResources {
					st.Error = berr.Error()
					status = append(status, st)
					missing = append(missing, fmt.Sprintf("%s (needs %s: %s)", r.Member, r.BaseLabel, berr))
					continue
				}
				st.Error = berr.Error() + " (allowed by settings)"
				status = append(status, st)
				continue
			}
			baseRaw, rerr := os.ReadFile(basePath)
			if rerr != nil {
				return nil, nil, fmt.Errorf("%s: %w", basePath, rerr)
			}
			// Read the display face and the style fresh from disk for THIS
			// build: no embedded copy, no cached style and no cached face, so
			// replacing the .otf or editing title_footer.toml changes the next
			// build without recompiling or restarting the dashboard.
			assets, aerr := s.LoadTitleAssets()
			if aerr != nil {
				return nil, nil, aerr
			}
			v, verr := s.ModVersion()
			// A missing or malformed version.txt yields the explicit unknown
			// marker, never a fabricated number; the diagnostic is recorded in
			// the resource status so it is never silent.
			label := modversion.Display(v)
			logical, cerr := texpack.ComposeTitleFooter(baseRaw, label, assets.FontBytes, assets.Style)
			if cerr != nil {
				return nil, nil, cerr
			}
			if r.LogicalSize != 0 && int64(len(logical)) != r.LogicalSize {
				return nil, nil, fmt.Errorf("%s: composed size %d does not match the expected %d", r.Member, len(logical), r.LogicalSize)
			}
			confirmed := assets.RuntimeConfirmed()
			composition := assets.Composition()
			out = append(out, build.Resource{Member: r.Member, Logical: logical, Note: r.Description, Raw: true})
			st.Present = true
			st.Storage = "raw"
			st.LogicalSize = int64(len(logical))
			st.LogicalSHA256 = hashutil.Bytes(logical)
			st.Base = basePath
			st.RendererVersion = texpack.TitleFooterRendererVersion
			st.FontSHA256 = assets.FontSHA256
			st.CacheIdentity = assets.CacheIdentity(label)
			st.Composition = composition
			st.RuntimeVerified = confirmed
			st.Detail = "composed from " + how + " · label " + label + " · stored raw"
			st.Detail += fmt.Sprintf(" · renderer v%d · font %s · style %s",
				texpack.TitleFooterRendererVersion, hashutil.Short(assets.FontSHA256), assets.StyleSource())
			if face, fallback := texpack.TitleLabelRendering(assets.FontBytes, label); face != "" {
				st.Detail += " · rendered with the " + face
				if fallback != "" {
					st.Detail += " (" + fallback + ")"
				}
			}
			switch composition {
			case CompositionReference:
				st.Detail += " · reference composition: runtime confirmed (2026-10-19)"
			case CompositionShipped:
				st.Detail += " · shipped composition: runtime unverified (no game run for this face/style)"
			default:
				st.Detail += " · modified face or style: runtime unverified until tested"
			}
			st.Detail += " · identity " + hashutil.Short(st.CacheIdentity)
			if verr != nil {
				st.Detail += " · " + verr.Error()
			}
			status = append(status, st)
		case "order_plate":
			spec, serr := plateOrderSpec(r)
			if serr != nil {
				return nil, nil, serr
			}
			basePath, how, berr := s.resolveBaseMember(r.BaseHint)
			if berr != nil {
				// The production shape is the complete 13-character set. A
				// missing script fails the build instead of quietly shipping a
				// partially corrected plate set; only the explicit
				// allow_missing_resources opt-out records and skips it.
				if !s.Cfg.AllowMissingResources {
					st.Error = berr.Error()
					status = append(status, st)
					missing = append(missing, fmt.Sprintf("%s (needs %s: %s)", r.Member, r.BaseLabel, berr))
					continue
				}
				st.Error = berr.Error() + " (allowed by settings; surname-first order skipped for this plate)"
				status = append(status, st)
				continue
			}
			baseRaw, rerr := os.ReadFile(basePath)
			if rerr != nil {
				return nil, nil, fmt.Errorf("%s: %w", basePath, rerr)
			}
			res, terr := plateorder.Apply(spec, baseRaw)
			if terr != nil {
				// A present but mismatching base is refused, never patched
				// blindly: that would produce a wrong or corrupt member.
				return nil, nil, terr
			}
			if r.LogicalSize != 0 && int64(res.LogicalSize) != r.LogicalSize {
				return nil, nil, fmt.Errorf("%s: transformed size %d does not match the expected %d", r.Member, res.LogicalSize, r.LogicalSize)
			}
			out = append(out, build.Resource{Member: r.Member, Logical: res.Logical, Note: r.Description + " · " + res.Describe()})
			st.Present = true
			st.Storage = "compressed"
			st.LogicalSize = int64(res.LogicalSize)
			st.LogicalSHA256 = res.LogicalSHA256
			st.Base = basePath
			st.RuntimeStatus = r.RuntimeStatus
			st.Detail = fmt.Sprintf("transformed from %s · base %s · %s · stored compressed",
				how, hashutil.Short(res.BaseSHA256), res.Describe())
			status = append(status, st)
		default:
			return nil, nil, fmt.Errorf("added resource %s: unsupported kind %q", r.Member, r.Kind)
		}
	}
	if len(missing) > 0 {
		return out, status, fmt.Errorf("added resources could not be produced and missing resources are not allowed: %s", strings.Join(missing, "; "))
	}
	return out, status, nil
}

// Components assembles the itemised component list for reports.
func (s *Service) Components(ds *translationsSummary, resources []ResourceStatus) PatchComponents {
	c := PatchComponents{
		BuildID: techdata.BuildIDUpper(), TitleID: techdata.TitleID(),
		ExecutablePatchSize: techdata.ExecutablePatchSize(),
		ExecutablePatchSHA:  techdata.ExecutablePatchSHA256(),
		AddedResources:      resources,
		TitleBranding:       s.titleBrandingStatus(resources),
	}
	if regs, _, err := techdata.ParseIPS(techdata.ExecutablePatchBytes()); err == nil {
		c.ExecutablePatchRecs = regs
	}
	if ds != nil {
		c.TranslationsRecords = ds.Records
		c.TranslationsSHA256 = ds.ChangesSHA256
	}
	c.Notes = append(c.Notes, "audio behaviour and audio-language selection are enforced by the executable patch")
	c.Notes = append(c.Notes, "the end-of-route presentation screen is an added archive member")
	for _, r := range resources {
		switch {
		case !r.Present && strings.TrimSpace(r.Error) != "":
			c.Omitted = append(c.Omitted, r.Member)
		case r.Kind == "compose_title" && !r.Present:
			c.Notes = append(c.Notes, "the title-screen footer branding is WITHHELD (title_branding=false in settings)")
		case r.Kind == "compose_title":
			note := fmt.Sprintf("the title-screen footer branding is an added archive member, versioned from version.txt, rendered live from patcher/assets/fonts (renderer v%d, font %s)", r.RendererVersion, hashutil.Short(r.FontSHA256))
			switch r.Composition {
			case CompositionReference:
				note += "; reference composition: runtime confirmed in Eden (2026-10-19)"
			case CompositionShipped:
				note += "; shipped composition: runtime unverified (no game run for this face/style)"
			default:
				note += "; modified face or style: runtime unverified until tested"
			}
			c.Notes = append(c.Notes, note+"; runtime status: "+r.RuntimeStatus)
		}
	}
	if len(c.Omitted) > 0 {
		c.Incomplete = true
		c.Notes = append(c.Notes, "PARTIAL BUILD: allow_missing_resources=true omitted "+strings.Join(c.Omitted, ", ")+"; the result is not the complete patch")
	}
	return c
}

// translationsSummary is the small identity record reports carry.
type translationsSummary struct {
	Records       int
	ChangesSHA256 string
}

// datasetSummary converts a loaded dataset into the report identity. A build
// driven by an explicit patch-data bundle has no dataset of its own, so an
// empty identity is returned instead.
func datasetSummary(ds *translations.Dataset) *translationsSummary {
	if ds == nil {
		return nil
	}
	return &translationsSummary{Records: len(ds.Records), ChangesSHA256: ds.Manifest.ChangesSHA256}
}

// BuildResult is the outcome of one build: the shared archive, the itemised
// components and the per-target archives.
type BuildResult struct {
	Archive    *build.Result
	Packaging  *packaging.Report
	Artifacts  []packaging.Artifact
	Components PatchComponents
	OutputDir  string
	WhenUTC    string
	// Built is false when every requested target was reused and no rebuild
	// happened ("keep existing").
	Built bool
	// Reused lists the targets whose existing archive was kept.
	Reused []packaging.Target
	// States carries the pre-build destination state of every requested target.
	States []DestinationState
}

// Build prepares the inputs, rebuilds the archive once and packages it for the
// requested targets. The rebuilt archive is an intermediate: it is written to
// the work cache, never among the ZIP outputs, so a repeated build never
// collides with its own intermediate. A single packaging pass renders every
// target in memory and commits them only once all of them are ready, so a
// failed or cancelled multi-target build leaves the existing outputs intact.
func (s *Service) Build(ctx context.Context, overwrite bool, target packaging.Target) (*BuildResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if target == "" {
		target = s.Target()
	}
	return s.buildFor(ctx, "build", target, target.Targets(), overwrite, nil)
}

// BuildKeepingExisting is the "keep existing outputs" decision: every requested
// target whose archive already exists and passes its layout check is reused
// unchanged, and only the missing or invalid targets are produced. When every
// target can be reused nothing is rebuilt at all.
func (s *Service) BuildKeepingExisting(ctx context.Context, target packaging.Target) (*BuildResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if target == "" {
		target = s.Target()
	}
	states, err := s.InspectDestinations(ctx, target)
	if err != nil {
		return nil, err
	}
	var produce, reuse []packaging.Target
	for _, st := range states {
		if st.Exists && st.Valid {
			reuse = append(reuse, st.Target)
		} else {
			produce = append(produce, st.Target)
		}
	}
	if len(produce) == 0 {
		return s.reuseResult(target, states, reuse)
	}
	br, err := s.buildFor(ctx, "build (keep existing)", target, produce, true, nil)
	if err != nil {
		return nil, err
	}
	br.Reused = reuse
	br.States = states
	return br, nil
}

// buildFor runs the shared build and packages exactly the produce targets.
func (s *Service) buildFor(ctx context.Context, op string, target packaging.Target, produce []packaging.Target, overwrite bool, initialStates []DestinationState) (*BuildResult, error) {
	start := time.Now()
	emit := func(e progress.Event) {
		if e.Op == "" {
			e.Op = op
		}
		if e.Elapsed == 0 {
			e.Elapsed = time.Since(start)
		}
		s.emit(e)
	}
	res, err := s.prepare(ctx, true)
	if err != nil {
		emit(progress.Event{Stage: progress.StageVerify, Status: progress.StatusError, Error: err.Error()})
		return nil, err
	}
	rep := res.Report
	if !rep.Ready {
		var fails []string
		for _, s := range rep.Steps {
			if s.Status == StatusFail {
				fails = append(fails, s.Title+": "+s.Detail)
			}
		}
		err := fmt.Errorf("preflight failed (%d problem(s)): %s; run `preflight` for details", rep.Failed, strings.Join(fails, "; "))
		emit(progress.Event{Stage: progress.StageVerify, Status: progress.StatusError, Error: err.Error()})
		return nil, err
	}
	emit(progress.Event{Stage: progress.StageTech, Status: progress.StatusStart, Note: "added resources"})
	resources, rstatus, rerr := s.buildResources()
	if rerr != nil {
		emit(progress.Event{Stage: progress.StageTech, Status: progress.StatusError, Error: rerr.Error()})
		return nil, rerr
	}
	emit(progress.Event{Stage: progress.StageTech, Status: progress.StatusDone,
		Completed: int64(len(resources)), Total: int64(len(resources))})
	bundleDir := rep.PatchData.Dir
	bankDir := ""
	if s.Cfg.PatchDataDir != "" {
		bundleDir = s.BundleDir()
	} else {
		derived, derr := s.WriteDerivedBundle(res.Plan, res.Dataset)
		if derr != nil {
			return nil, derr
		}
		s.log("derived patch data: %s (%d operations)", derived.Dir, derived.Operations)
		bundleDir = derived.Dir
		bankDir = res.State.BaseBanksDir
	}
	// The rebuilt archive is an intermediate: it goes to the work cache and is
	// always replaceable, so it never blocks a repeated build the way a file in
	// the output directory would.
	outPath := s.BuildArchivePath()
	reportPath := filepath.Join(s.ReportsDir(), "last_build.report.json")
	s.log("build: archive -> %s", outPath)
	emit(progress.Event{Stage: progress.StageCPK, Status: progress.StatusStart, Item: outPath, Note: outPath})
	cpk, err := build.Build(ctx, build.Config{
		SourceArchive: sourcePathOr(rep, res),
		BundleDir:     bundleDir,
		BankDir:       bankDir,
		OutputPath:    outPath,
		Overwrite:     true,
		Encoder:       s.Cfg.Encoder,
		ReportPath:    reportPath,
		ReadonlyRoots: s.ReadonlyRoots(),
		AllowRoots:    s.AllowedWriteRoots(),
		Resources:     resources,
		Ctx:           ctx,
		Progress:      progress.Func(emit),
	})
	if err != nil {
		emit(progress.Event{Stage: progress.StageCPK, Status: progress.StatusError, Error: err.Error()})
		return nil, err
	}
	s.log("build: archive ok size=%d sha256=%s members=%d→%d ops=%d in %s",
		cpk.OutputSize, hashutil.Short(cpk.OutputSHA256), cpk.Report.MemberCountBefore,
		cpk.Report.MemberCountAfter, cpk.Operations, time.Since(start).Round(time.Millisecond))

	if err := ctx.Err(); err != nil {
		return nil, ErrCancelled
	}
	pr := &packaging.Report{
		TitleID: techdata.TitleID(), BuildID: techdata.BuildIDUpper(), Release: s.ReleaseLabel(),
		Target:  target,
		Archive: packaging.InputRef{Name: packaging.RomfsArchiveName, Size: cpk.OutputSize, SHA256: cpk.OutputSHA256},
		Patch:   packaging.InputRef{Name: techdata.BuildIDUpper() + ".ips", Size: techdata.ExecutablePatchSize(), SHA256: techdata.ExecutablePatchSHA256()},
	}
	arts, verr := s.packageTargets(ctx, produce, cpk.Bytes, overwrite)
	pr.Artifacts = arts
	if verr != nil {
		// The packaging writes each target atomically and in order, so report
		// exactly which targets were committed before the failure.
		for _, a := range arts {
			s.log("packaging: %s was written before the failure: %s", a.Target, a.Path)
		}
		emit(progress.Event{Stage: progress.StagePackage, Status: progress.StatusError, Error: verr.Error(),
			Completed: int64(len(arts)), Total: int64(len(produce)),
			Note: fmt.Sprintf("%d of %d target archive(s) written before the failure", len(arts), len(produce))})
		return nil, verr
	}
	// determinism: a second packaging pass over the same bytes must be identical
	second, derr := s.packageTargets(ctx, produce, cpk.Bytes, true)
	if derr != nil {
		return nil, derr
	}
	pr.Determinism = sameArtifacts(arts, second)
	for _, a := range arts {
		pr.Checks = append(pr.Checks, packaging.Check{Target: string(a.Target), ID: "artifact.validated", Pass: a.Validated, Detail: a.Path})
	}
	emit(progress.Event{Stage: progress.StageVerify, Status: progress.StatusDone,
		Completed: int64(len(arts)), Total: int64(len(arts)), Note: "outputs verified"})
	br := &BuildResult{
		Archive: cpk, Packaging: pr, Artifacts: arts,
		Components: s.Components(datasetSummary(res.Dataset), rstatus),
		OutputDir:  s.OutputDirOrDefault(), WhenUTC: time.Now().UTC().Format(time.RFC3339),
		Built: true, States: initialStates,
	}
	bs := &BuildSummary{
		OutputPath: cpk.OutputPath, OutputSHA256: cpk.OutputSHA256, OutputSize: cpk.OutputSize,
		BundleID: cpk.BundleID, BundleVersion: cpk.BundleVersion, Operations: cpk.Operations,
		MembersEdited: cpk.MembersEdited, BanksAdded: cpk.Report.BanksAdded, Verified: true,
		WhenUTC: br.WhenUTC, ReportPath: reportPath,
	}
	s.Cfg.LastBuild = filepath.Join(s.ReportsDir(), "last_build.json")
	if err := os.MkdirAll(s.ReportsDir(), 0o755); err == nil {
		_ = writeJSON(s.Cfg.LastBuild, bs)
		_ = writeJSON(filepath.Join(s.ReportsDir(), "last_package.json"), pr)
		for _, a := range arts {
			_ = writeJSON(filepath.Join(s.ReportsDir(), "report-"+string(a.Target)+".json"), map[string]any{
				"target": string(a.Target), "artifact": a, "components": br.Components, "packaging": pr,
			})
		}
	}
	_ = s.savePersisted()
	return br, nil
}

// reuseResult assembles a build result from existing archives without running a
// build. Every reused archive is re-validated and re-hashed so the report is
// never a claim taken on trust.
func (s *Service) reuseResult(target packaging.Target, states []DestinationState, reuse []packaging.Target) (*BuildResult, error) {
	var arts []packaging.Artifact
	for _, st := range states {
		if !st.Exists || !st.Valid {
			continue
		}
		a := packaging.Artifact{Target: st.Target, Label: st.Target.Label(), Path: st.Path, Size: st.Size, SHA256: st.SHA256, Validated: true}
		if entries, err := packaging.ReadEntries(st.Path); err == nil {
			a.Entries = entries
		}
		arts = append(arts, a)
		s.log("reuse: %s %s (%d bytes, %s)", st.Target.Label(), filepath.Base(st.Path), st.Size, hashutil.Short(st.SHA256))
	}
	pr := &packaging.Report{
		TitleID: techdata.TitleID(), BuildID: techdata.BuildIDUpper(), Release: s.ReleaseLabel(),
		Target: target, Artifacts: arts, Determinism: true,
	}
	for _, a := range arts {
		pr.Checks = append(pr.Checks, packaging.Check{Target: string(a.Target), ID: "artifact.validated", Pass: a.Validated, Detail: a.Path})
	}
	br := &BuildResult{
		Packaging: pr, Artifacts: arts, OutputDir: s.OutputDirOrDefault(),
		WhenUTC: time.Now().UTC().Format(time.RFC3339),
		Built:   false, Reused: reuse, States: states,
		Components: s.Components(nil, nil),
	}
	s.emit(progress.Event{Op: "build (keep existing)", Stage: progress.StageVerify, Status: progress.StatusDone,
		Completed: int64(len(arts)), Total: int64(len(arts)), Note: "existing outputs reused"})
	return br, nil
}

// packageTargets renders every target in memory first, then commits them. A
// failure while rendering any target therefore writes nothing, and a failure
// while committing the first target leaves the remaining existing archives
// untouched.
func (s *Service) packageTargets(ctx context.Context, targets []packaging.Target, archiveBytes []byte, overwrite bool) ([]packaging.Artifact, error) {
	req := packaging.Request{
		ArchiveBytes: archiveBytes,
		PatchBytes:   techdata.ExecutablePatchBytes(),
		BuildID:      techdata.BuildIDUpper(),
		TitleID:      techdata.TitleID(),
		Release:      s.ReleaseLabel(),
		OutputDir:    s.OutputDirOrDefault(),
		Overwrite:    overwrite,
	}
	type staged struct {
		target packaging.Target
		raw    []byte
	}
	var stage []staged
	for _, t := range targets {
		if err := ctx.Err(); err != nil {
			return nil, ErrCancelled
		}
		raw, err := packaging.Render(req, t)
		if err != nil {
			return nil, err
		}
		stage = append(stage, staged{target: t, raw: raw})
	}
	var out []packaging.Artifact
	for i, stg := range stage {
		if err := ctx.Err(); err != nil {
			return out, ErrCancelled
		}
		s.emit(progress.Event{Stage: progress.StagePackage, Status: progress.StatusProgress,
			Item: stg.target.Label(), Completed: int64(i), Total: int64(len(stage)),
			Bytes: int64(len(stg.raw)), Note: packaging.ArchiveName(stg.target, s.ReleaseLabel())})
		a, err := packaging.Commit(req, stg.target, stg.raw)
		if err != nil {
			return out, err
		}
		out = append(out, *a)
	}
	s.emit(progress.Event{Stage: progress.StagePackage, Status: progress.StatusDone,
		Completed: int64(len(out)), Total: int64(len(out))})
	return out, nil
}

func sameArtifacts(a, b []packaging.Artifact) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].SHA256 != b[i].SHA256 || a[i].Size != b[i].Size {
			return false
		}
	}
	return true
}

// Package produces the per-target archives from an already built archive.
func (s *Service) Package(ctx context.Context, archivePath string, target packaging.Target, overwrite bool) (*packaging.Report, error) {
	if target == "" {
		target = s.Target()
	}
	if archivePath == "" {
		if p := s.BuildArchivePath(); fileIsFile(p) {
			archivePath = p
		} else {
			archivePath = s.OutputPath()
		}
	}
	raw, err := os.ReadFile(archivePath)
	if err != nil {
		return nil, err
	}
	arts, err := s.packageTargets(ctx, target.Targets(), raw, overwrite)
	if err != nil {
		return nil, err
	}
	pr := &packaging.Report{
		TitleID: techdata.TitleID(), BuildID: techdata.BuildIDUpper(), Release: s.ReleaseLabel(),
		Target:    target,
		Archive:   packaging.InputRef{Name: filepath.Base(archivePath), Size: int64(len(raw)), SHA256: hashutil.Bytes(raw)},
		Patch:     packaging.InputRef{Name: techdata.BuildIDUpper() + ".ips", Size: techdata.ExecutablePatchSize(), SHA256: techdata.ExecutablePatchSHA256()},
		Artifacts: arts,
	}
	for _, a := range arts {
		pr.Checks = append(pr.Checks, packaging.Check{Target: string(a.Target), ID: "artifact.validated", Pass: a.Validated, Detail: a.Path})
	}
	return pr, nil
}

// ValidateResult is the full validation report for a target.
type ValidateResult struct {
	Tool          string               `json:"tool"`
	Version       string               `json:"version"`
	Target        packaging.Target     `json:"target"`
	Ready         bool                 `json:"ready"`
	Components    PatchComponents      `json:"components"`
	TitleBranding TitleBrandingStatus  `json:"title_branding"`
	Artifacts     []packaging.Artifact `json:"artifacts"`
	Checks        []packaging.Check    `json:"checks"`
	Error         string               `json:"error,omitempty"`
	ReportPath    string               `json:"report_path,omitempty"`
}

// Validate builds into a temporary directory and verifies every produced
// archive. Nothing is written to the output directory.
func (s *Service) Validate(ctx context.Context, target packaging.Target) (*ValidateResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if target == "" {
		target = s.Target()
	}
	vr := &ValidateResult{Tool: build.ToolName, Version: build.VersionString(), Target: target}
	vr.TitleBranding = s.titleBrandingStatus(nil)
	res, err := s.prepare(ctx, true)
	if err != nil {
		return vr, err
	}
	if res.Report == nil || !res.Report.Ready {
		vr.Error = "preflight failed; validation could not run"
		return vr, nil
	}
	resources, rstatus, rerr := s.buildResources()
	if rerr != nil {
		vr.Error = rerr.Error()
		return vr, nil
	}
	bundleDir := res.Report.PatchData.Dir
	bankDir := ""
	if s.Cfg.PatchDataDir != "" {
		bundleDir = s.BundleDir()
	} else {
		derived, derr := s.WriteDerivedBundle(res.Plan, res.Dataset)
		if derr != nil {
			vr.Error = derr.Error()
			return vr, derr
		}
		bundleDir = derived.Dir
		bankDir = res.State.BaseBanksDir
	}
	outDir := s.OutputDirOrDefault()
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return vr, err
	}
	tmpDir, err := os.MkdirTemp(outDir, ".validate-*")
	if err != nil {
		return vr, err
	}
	defer os.RemoveAll(tmpDir)
	br, err := build.Build(ctx, build.Config{
		SourceArchive: sourcePathOr(res.Report, res), BundleDir: bundleDir, BankDir: bankDir,
		OutputPath: filepath.Join(tmpDir, "validate.CPK"), Overwrite: true, Encoder: s.Cfg.Encoder,
		ReadonlyRoots: s.ReadonlyRoots(), AllowRoots: s.AllowedWriteRoots(),
		Resources: resources, Ctx: ctx,
	})
	if err != nil {
		vr.Error = err.Error()
		return vr, err
	}
	req := packaging.Request{
		ArchiveBytes: br.Bytes,
		PatchBytes:   techdata.ExecutablePatchBytes(),
		BuildID:      techdata.BuildIDUpper(),
		TitleID:      techdata.TitleID(),
		Release:      s.ReleaseLabel(),
		OutputDir:    tmpDir,
		Overwrite:    true,
	}
	ok := true
	for _, t := range target.Targets() {
		a, err := packaging.Build(req, t)
		if err != nil {
			vr.Error = err.Error()
			return vr, err
		}
		vr.Artifacts = append(vr.Artifacts, *a)
		checks, cerr := packaging.ValidateFile(a.Path, t, req)
		if cerr != nil {
			vr.Error = cerr.Error()
			return vr, cerr
		}
		for _, c := range checks {
			vr.Checks = append(vr.Checks, c)
			if !c.Pass {
				ok = false
			}
		}
		// remove the temporary archives so nothing survives validation
		_ = os.Remove(a.Path)
	}
	// determinism probe: rebuild the archives and compare digests
	probe, derr := s.packageTargets(ctx, target.Targets(), br.Bytes, true)
	if derr != nil {
		vr.Error = derr.Error()
		return vr, derr
	}
	det := sameArtifacts(vr.Artifacts, probe)
	vr.Checks = append(vr.Checks, packaging.Check{ID: "packaging.determinism", Pass: det, Detail: "two passes produce identical archives"})
	if !det {
		ok = false
	}
	vr.Components = s.Components(datasetSummary(res.Dataset), rstatus)
	vr.TitleBranding = vr.Components.TitleBranding
	vr.Ready = ok && br.Report.Verified && br.Report.UnmodifiedIntact
	reportPath := filepath.Join(s.ReportsDir(), "last_validation.json")
	vr.ReportPath = reportPath
	if err := os.MkdirAll(s.ReportsDir(), 0o755); err == nil {
		_ = writeJSON(reportPath, vr)
	}
	s.Cfg.LastValidation = reportPath
	_ = s.savePersisted()
	return vr, nil
}

// InspectResult reports on a produced archive or the built CPK.
type InspectResult struct {
	Target     packaging.Target  `json:"target,omitempty"`
	Path       string            `json:"path"`
	Entries    []packaging.Entry `json:"entries,omitempty"`
	Checks     []packaging.Check `json:"checks,omitempty"`
	CPK        *build.Inspection `json:"cpk_inspection,omitempty"`
	Ready      bool              `json:"ready"`
	ArchiveSHA string            `json:"archive_sha256,omitempty"`
	PatchSHA   string            `json:"patch_sha256,omitempty"`
	// TitleIncluded reports whether the rebuilt archive inside this output
	// carries the title-branding member. It is detected from the bytes, not
	// from the current settings.
	TitleIncluded bool `json:"title_included"`
	// TitleRuntime is the recorded runtime observation for the title resource.
	TitleRuntime string `json:"title_runtime,omitempty"`
	// TitleNote carries a diagnostic when inclusion could not be determined.
	TitleNote string `json:"title_note,omitempty"`
}

// Inspect examines a produced archive (zip) or the built CPK.
func (s *Service) InspectOutput(ctx context.Context, path string, target packaging.Target) (*InspectResult, error) {
	if target == "" {
		target = s.Target()
	}
	targets := target.Targets()
	if path == "" {
		if len(targets) == 1 {
			path = filepath.Join(s.OutputDirOrDefault(), packaging.ArchiveName(targets[0], s.ReleaseLabel()))
		} else {
			path = filepath.Join(s.OutputDirOrDefault(), packaging.ArchiveName(packaging.TargetAtmosphere, s.ReleaseLabel()))
		}
	}
	if !fileIsFile(path) {
		return nil, fmt.Errorf("output not found: %s", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	res := &InspectResult{Path: path, Target: target}
	if bytes.HasPrefix(raw, []byte("CPK ")) {
		ins, ierr := build.Inspect(path)
		if ierr != nil {
			return nil, ierr
		}
		res.CPK = ins
		res.Ready = ins.AllChecksOK
		res.ArchiveSHA = ins.SHA256
		res.TitleIncluded = inspectionCarriesTitle(ins)
		if res.TitleIncluded {
			res.TitleRuntime = titleResourceRuntime()
		}
		return res, nil
	}
	if len(raw) < 4 || !(raw[0] == 'P' && raw[1] == 'K') {
		return nil, fmt.Errorf("not an archive this tool produces: %s", path)
	}
	// figure out the target from the archive name, then from the entry layout
	candidates := target.Targets()
	req := packaging.Request{
		BuildID: techdata.BuildIDUpper(), TitleID: techdata.TitleID(),
		Release: s.ReleaseLabel(), OutputDir: filepath.Dir(path),
	}
	concrete := candidates[0]
	identified := false
	for _, t := range candidates {
		if filepath.Base(path) == filepath.Base(packaging.ArchiveName(t, s.ReleaseLabel())) {
			concrete = t
			identified = true
			break
		}
	}
	if !identified && len(candidates) > 1 {
		// The file name did not identify the target, so pick the layout whose
		// checks all pass. A layout whose bytes merely reopen is not enough:
		// ValidateFile reports layout failures as failing checks, not as errors.
		for _, t := range candidates {
			checks, verr := packaging.ValidateFile(path, t, req)
			if verr != nil {
				continue
			}
			ok := true
			for _, c := range checks {
				if !c.Pass {
					ok = false
					break
				}
			}
			if ok {
				concrete = t
				break
			}
		}
	}
	res.Target = concrete
	entries, eerr := packaging.ReadEntries(path)
	if eerr != nil {
		return nil, eerr
	}
	res.Entries = entries
	checks, cerr := packaging.ValidateFile(path, res.Target, req)
	if cerr != nil {
		return nil, cerr
	}
	res.Checks = checks
	res.Ready = true
	for _, c := range checks {
		if !c.Pass {
			res.Ready = false
		}
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Path, ".ips") {
			res.PatchSHA = e.SHA256
		}
		if strings.HasSuffix(e.Path, ".CPK") {
			res.ArchiveSHA = e.SHA256
		}
	}
	// The title member lives inside the rebuilt archive, so inspection reads
	// the embedded archive's members rather than the ZIP entry list.
	if included, terr := zipCarriesTitleMember(path); terr != nil {
		res.TitleNote = "title member check: " + terr.Error()
	} else if included {
		res.TitleIncluded = true
		res.TitleRuntime = titleResourceRuntime()
	}
	return res, nil
}

// inspectionCarriesTitle reports whether a reconstructed archive contains the
// title-branding member.
func inspectionCarriesTitle(ins *build.Inspection) bool {
	if ins == nil {
		return false
	}
	for _, m := range ins.Members {
		if strings.EqualFold(m.Path, texpack.TitleMember) {
			return true
		}
	}
	return false
}

// zipCarriesTitleMember opens a produced ZIP, reads the rebuilt archive it
// carries and reports whether that archive contains the title member.
func zipCarriesTitleMember(path string) (bool, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return false, err
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || !strings.HasSuffix(strings.ToUpper(f.Name), ".CPK") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return false, err
		}
		raw, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return false, err
		}
		ins, err := build.InspectBytes(raw)
		if err != nil {
			return false, err
		}
		if inspectionCarriesTitle(ins) {
			return true, nil
		}
	}
	return false, nil
}

// titleResourceRuntime returns the recorded runtime status of the title
// resource, or "" when the descriptor is missing.
func titleResourceRuntime() string {
	_, _, _, rt, _ := TitleResource()
	return rt
}

// OutputArtifacts lists the archives that exist in the output directory.
func (s *Service) OutputArtifacts() []packaging.Artifact {
	var out []packaging.Artifact
	for _, t := range []packaging.Target{packaging.TargetAtmosphere, packaging.TargetEmulator} {
		p := filepath.Join(s.OutputDirOrDefault(), packaging.ArchiveName(t, s.ReleaseLabel()))
		if !fileIsFile(p) {
			continue
		}
		fi, err := os.Stat(p)
		if err != nil {
			continue
		}
		a := packaging.Artifact{Target: t, Label: t.Label(), Path: p, Size: fi.Size()}
		if sum, _, err := hashutil.File(p); err == nil {
			a.SHA256 = sum
		}
		if entries, err := packaging.ReadEntries(p); err == nil {
			a.Entries = entries
		}
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Target < out[j].Target })
	return out
}
