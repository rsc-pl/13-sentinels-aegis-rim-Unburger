package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"13s-patcher/internal/app"
	"13s-patcher/internal/build"
	"13s-patcher/internal/hashutil"
	"13s-patcher/internal/modversion"
	"13s-patcher/internal/packaging"
	"13s-patcher/internal/techdata"
)

func cmdSetup(args []string) int {
	var c common
	fs := flag.NewFlagSet("setup", flag.ExitOnError)
	c.bind(fs)
	_ = fs.Parse(args)
	svc, err := c.service()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	svc.Logf = func(line string) { fmt.Fprintln(os.Stderr, "  "+line) }
	set, err := svc.AutoConfigure(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	rep, _ := svc.DiscoverSources()
	if err := emit(c.jsonOut, rep, func(w io.Writer) {
		fmt.Fprintf(w, "storage mode   %s\n", svc.Loc.Mode)
		if svc.InProject() {
			fmt.Fprintf(w, "project root   %s\n", svc.Loc.Root)
		}
		fmt.Fprintf(w, "settings       %s\n", svc.CfgPath)
		if len(set) == 0 {
			fmt.Fprintln(w, "configured     nothing new")
		} else {
			for _, s := range set {
				fmt.Fprintf(w, "configured     %s\n", s)
			}
		}
		fmt.Fprintf(w, "cache          %s\n", svc.CacheDir())
		fmt.Fprintf(w, "output         %s\n", svc.OutputDirOrDefault())
		if rep == nil {
			return
		}
		fmt.Fprintf(w, "containers\n")
		for _, a := range rep.Found {
			state := "unrecognised"
			if a.Identified {
				state = "identified"
			}
			fmt.Fprintf(w, "  %-8s %-12s %s\n", a.Role, state, a.Path)
		}
		if len(rep.Found) == 0 {
			fmt.Fprintln(w, "  none found")
		}
		if len(rep.Missing) > 0 {
			fmt.Fprintf(w, "still missing  %v\n", rep.Missing)
		}
	}); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}

func cmdSources(args []string) int {
	var c common
	fs := flag.NewFlagSet("sources", flag.ExitOnError)
	c.bind(fs)
	_ = fs.Parse(args)
	svc, err := c.service()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	rep, err := svc.DiscoverSources()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if err := emit(c.jsonOut, rep, func(w io.Writer) {
		fmt.Fprintf(w, "search directories\n")
		for _, d := range rep.Dirs {
			fmt.Fprintf(w, "  %s\n", d)
		}
		fmt.Fprintf(w, "\nfound\n")
		for _, a := range rep.Found {
			state := "unrecognised"
			if a.Identified {
				state = "identified · " + a.TitleID
				if a.DisplayVersion != "" {
					state += " · " + a.DisplayVersion
				}
			}
			fmt.Fprintf(w, "  %-8s %-28s %s\n", a.Role, state, a.Path)
		}
		if len(rep.Found) == 0 {
			fmt.Fprintln(w, "  none")
		}
		fmt.Fprintf(w, "\nmissing required: %v\n", rep.Missing)
		for _, n := range rep.Notes {
			fmt.Fprintf(w, "note: %s\n", n)
		}
	}); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if len(rep.Missing) > 0 {
		return 1
	}
	return 0
}

func cmdStatus(args []string, plainNoTTY bool) int {
	var c common
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	c.bind(fs)
	_ = fs.Parse(args)
	svc, err := c.service()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	st := svc.Status()
	if err := emit(c.jsonOut, st, func(w io.Writer) { printStatus(w, st) }); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}

func cmdDoctor(args []string) int {
	var c common
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	c.bind(fs)
	_ = fs.Parse(args)
	svc, err := c.service()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	rep := svc.Doctor()
	if err := emit(c.jsonOut, rep, func(w io.Writer) {
		fmt.Fprintf(w, "%s %s · environment report\n\n", rep.Tool, rep.Version)
		for _, ch := range rep.Checks {
			fmt.Fprintf(w, "  %-5s %-22s %s\n", ch.Status, ch.Title, ch.Detail)
		}
		fmt.Fprintf(w, "\n%d passed, %d failed, %d warnings\n", rep.Passed, rep.Failed, rep.Warned)
	}); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if rep.Failed > 0 {
		return 1
	}
	return 0
}

func cmdPreflight(args []string) int {
	var c common
	fs := flag.NewFlagSet("preflight", flag.ExitOnError)
	c.bind(fs)
	_ = fs.Parse(args)
	svc, err := c.service()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	target, terr := c.target(svc)
	if terr != nil {
		fmt.Fprintln(os.Stderr, "error:", terr)
		return 2
	}
	rep, err := svc.Preflight(context.Background(), target)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if err := emit(c.jsonOut, rep, func(w io.Writer) { printPreflight(w, rep) }); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if !rep.Ready {
		return 1
	}
	return 0
}

func cmdBuild(args []string) int {
	var c common
	var force bool
	fs := flag.NewFlagSet("build", flag.ExitOnError)
	c.bind(fs)
	fs.BoolVar(&force, "force", false, "replace existing output files")
	_ = fs.Parse(args)
	svc, err := c.service()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	target, terr := c.target(svc)
	if terr != nil {
		fmt.Fprintln(os.Stderr, "error:", terr)
		return 2
	}
	svc.Logf = func(line string) { fmt.Fprintln(os.Stderr, "  "+line) }
	if c.jsonOut == "" {
		svc.Progress = newStagePrinter(os.Stderr)
	}
	res, err := svc.Build(context.Background(), force, target)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if err := emit(c.jsonOut, buildJSON(res), func(w io.Writer) { printBuild(w, res) }); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}

func cmdPackage(args []string) int {
	var c common
	var force bool
	var archive string
	fs := flag.NewFlagSet("package", flag.ExitOnError)
	c.bind(fs)
	fs.BoolVar(&force, "force", false, "replace existing output archives")
	fs.StringVar(&archive, "archive", "", "rebuilt archive to package (default: the configured output)")
	_ = fs.Parse(args)
	svc, err := c.service()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	target, terr := c.target(svc)
	if terr != nil {
		fmt.Fprintln(os.Stderr, "error:", terr)
		return 2
	}
	rep, err := svc.Package(context.Background(), archive, target, force)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if err := emit(c.jsonOut, rep, func(w io.Writer) { printPackaging(w, rep) }); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	for _, a := range rep.Artifacts {
		if !a.Validated {
			return 1
		}
	}
	return 0
}

func cmdValidate(args []string) int {
	var c common
	fs := flag.NewFlagSet("validate", flag.ExitOnError)
	c.bind(fs)
	_ = fs.Parse(args)
	svc, err := c.service()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	target, terr := c.target(svc)
	if terr != nil {
		fmt.Fprintln(os.Stderr, "error:", terr)
		return 2
	}
	svc.Logf = func(line string) { fmt.Fprintln(os.Stderr, "  "+line) }
	if c.jsonOut == "" {
		svc.Progress = newStagePrinter(os.Stderr)
	}
	res, err := svc.Validate(context.Background(), target)
	if err != nil && res == nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if err := emit(c.jsonOut, res, func(w io.Writer) { printValidate(w, res) }); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if !res.Ready {
		return 1
	}
	return 0
}

func cmdInspect(args []string) int {
	var c common
	var archive string
	fs := flag.NewFlagSet("inspect", flag.ExitOnError)
	c.bind(fs)
	fs.StringVar(&archive, "archive", "", "archive to inspect (default: the configured output)")
	_ = fs.Parse(args)
	svc, err := c.service()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	target, terr := c.target(svc)
	if terr != nil {
		fmt.Fprintln(os.Stderr, "error:", terr)
		return 2
	}
	res, err := svc.InspectOutput(context.Background(), archive, target)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if err := emit(c.jsonOut, res, func(w io.Writer) { printInspect(w, res) }); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if !res.Ready {
		return 1
	}
	return 0
}

// ---------------------------------------------------------------------------
// text renderers
// ---------------------------------------------------------------------------

func printStatus(w io.Writer, st *app.Status) {
	fmt.Fprintf(w, "%s %s\n", st.Tool, st.Version)
	switch {
	case st.ModVersion != "":
		fmt.Fprintf(w, "%s\n", modversion.Display(st.ModVersion))
	case st.ModVersionError != "":
		fmt.Fprintf(w, "Unburger Mod unknown — %s\n", st.ModVersionError)
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "mode         %s", st.Mode)
	if st.ProjectRoot != "" {
		fmt.Fprintf(w, " · %s", st.ProjectRoot)
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "config       %s%s\n", st.ConfigPath, existsSuffix(st.ConfigExists))
	fmt.Fprintf(w, "workdir      %s\n", st.WorkDir)
	fmt.Fprintf(w, "target       %s\n", st.Target)
	if st.Translations.Loaded {
		fmt.Fprintf(w, "translations %s · %d record(s) · %d bank(s) · %s · schema %s\n",
			st.Translations.Dir, st.Translations.Records, st.Translations.Banks, hashutil.Short(st.Translations.ChangesSHA256), st.Translations.Schema)
	} else {
		msg := st.Translations.Error
		if msg == "" {
			msg = "not loaded"
		}
		fmt.Fprintf(w, "translations %s\n", msg)
	}
	if st.Cache.Ready {
		fmt.Fprintf(w, "cache        %s · %d base bank(s) · overlay %s\n",
			st.Cache.Dir, st.Cache.BankCount, hashutil.Short(st.Cache.OverlaySHA))
	} else {
		fmt.Fprintf(w, "cache        not populated (%s)\n", st.Cache.Dir)
	}
	if st.Sources != nil {
		identified := 0
		for _, a := range st.Sources.Found {
			if a.Identified {
				identified++
			}
		}
		fmt.Fprintf(w, "containers   %d identified", identified)
		if len(st.Sources.Missing) > 0 {
			fmt.Fprintf(w, " · missing %v", st.Sources.Missing)
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintf(w, "keys         %d/%d present in %s\n", len(st.Keys.Present), len(st.Keys.Present)+len(st.Keys.Missing), st.Keys.Dir)
	src := "not resolved"
	switch {
	case st.Source.Path == "":
		src = "not resolved"
	case st.Source.Supported:
		src = st.Source.Path + " · supported"
	case st.Source.Error != "":
		src = st.Source.Path + " · " + st.Source.Error
	default:
		src = st.Source.Path
	}
	fmt.Fprintf(w, "input        %s\n", src)
	if st.PatchData.Loaded {
		fmt.Fprintf(w, "patch data   %s\n", st.PatchData.Dir)
		fmt.Fprintf(w, "             %s %s · %d operations · manifest %s\n",
			st.PatchData.BundleID, st.PatchData.BundleVersion, st.PatchData.Operations, hashutil.Short(st.PatchData.ManifestSHA256))
	} else if st.PatchData.Error != "" {
		fmt.Fprintf(w, "patch data   %s\n", st.PatchData.Error)
	}
	fmt.Fprintf(w, "output dir   %s\n", st.OutputDir)
	if len(st.Outputs) > 0 {
		for _, a := range st.Outputs {
			fmt.Fprintf(w, "             %s · %s bytes · %s\n", filepathBase(a.Path), fmtInt(a.Size), hashutil.Short(a.SHA256))
		}
	} else {
		fmt.Fprintf(w, "             (no archives yet)\n")
	}
	if st.LastBuild != nil {
		fmt.Fprintf(w, "last build   %s · %s · %d operations · verified=%v\n",
			filepathBase(st.LastBuild.OutputPath), hashutil.Short(st.LastBuild.OutputSHA256), st.LastBuild.Operations, st.LastBuild.Verified)
	} else {
		fmt.Fprintf(w, "last build   none\n")
	}
	if st.LastValidation != "" {
		fmt.Fprintf(w, "validation   %s\n", st.LastValidation)
	} else {
		fmt.Fprintf(w, "validation   not run\n")
	}
	if len(st.Warnings) > 0 {
		fmt.Fprintln(w, "\nwarnings")
		for _, wl := range st.Warnings {
			fmt.Fprintf(w, "  - %s\n", wl)
		}
	}
}

func printPreflight(w io.Writer, rep *app.PreflightReport) {
	fmt.Fprintf(w, "preflight · %s · target %s\n\n", rep.ConfigPath, rep.Target)
	for _, s := range rep.Steps {
		fmt.Fprintf(w, "  %-5s %-28s %s\n", s.Status, s.Title, s.Detail)
	}
	fmt.Fprintf(w, "\n%d passed, %d failed, %d warnings · ready=%v\n", rep.Passed, rep.Failed, rep.Warned, rep.Ready)
	if rep.OutputPath != "" {
		fmt.Fprintf(w, "intermediate: %s\n", rep.OutputPath)
	}
	for _, a := range rep.Outputs {
		fmt.Fprintf(w, "output: %s\n", a)
	}
}

func printBuild(w io.Writer, res *app.BuildResult) {
	cpk := res.Archive
	fmt.Fprintf(w, "built %s\n", cpk.OutputPath)
	fmt.Fprintf(w, "  bundle    %s %s (manifest %s)\n", cpk.BundleID, cpk.BundleVersion, hashutil.Short(cpk.ManifestSHA256))
	fmt.Fprintf(w, "  source    %s · %s\n", cpk.SourceArchive, hashutil.Short(cpk.SourceSHA256))
	fmt.Fprintf(w, "  archive   %s bytes · sha256 %s\n", fmtInt(cpk.OutputSize), cpk.OutputSHA256)
	fmt.Fprintf(w, "  operations %d across %d members · added %d · verified=%v\n",
		cpk.Operations, cpk.MembersEdited, cpk.Report.BanksAdded, cpk.Report.Verified)
	fmt.Fprintf(w, "  unmodified members byte-identical: %v\n", cpk.Report.UnmodifiedIntact)
	fmt.Fprintf(w, "components\n")
	fmt.Fprintf(w, "  translations   %d record(s) · %s\n", res.Components.TranslationsRecords, hashutil.Short(res.Components.TranslationsSHA256))
	fmt.Fprintf(w, "  executable     %d record(s) · %s bytes · %s\n", res.Components.ExecutablePatchRecs, fmtInt(res.Components.ExecutablePatchSize), hashutil.Short(res.Components.ExecutablePatchSHA))
	fmt.Fprintf(w, "  build id       %s\n", res.Components.BuildID)
	fmt.Fprintf(w, "  title id       %s\n", res.Components.TitleID)
	for _, r := range res.Components.AddedResources {
		if r.Present {
			fmt.Fprintf(w, "  added          %s (%s · %s)\n", r.Member, r.StateLabel(), hashutil.Short(r.LogicalSHA256))
			continue
		}
		fmt.Fprintf(w, "  added          %s (%s)\n", r.Member, r.StateLabel())
	}
	printTitleBranding(w, res.Components.TitleBranding)
	if res.Components.Incomplete {
		fmt.Fprintf(w, "  PARTIAL BUILD  omitted by allow_missing_resources=true: %s\n", strings.Join(res.Components.Omitted, ", "))
	}
	fmt.Fprintf(w, "packaging (target %s · deterministic=%v)\n", res.Packaging.Target, res.Packaging.Determinism)
	fmt.Fprint(w, packaging.DescribeArtifacts(res.Artifacts))
}

// printTitleBranding states the effective in-game title-branding selection. A
// disabled build never reads as if the member were included.
func printTitleBranding(w io.Writer, tb app.TitleBrandingStatus) {
	if !tb.Enabled {
		fmt.Fprintf(w, "  title branding disabled (title_branding=false) · %s not included\n", tb.Member)
		return
	}
	footer := tb.Footer
	if footer == "" {
		footer = modversion.Display("")
	}
	state := "NOT included"
	if tb.Included {
		state = "included"
		if lbl := tb.StorageLabel(); lbl != "" {
			state += " + " + lbl
		}
	}
	fmt.Fprintf(w, "  title branding enabled · %s %s · footer %s\n", tb.Member, state, footer)
	if tb.Included {
		if tb.RuntimeVerified {
			fmt.Fprintf(w, "    runtime confirmed (reference composition, Eden 2026-10-19)\n")
		} else {
			fmt.Fprintf(w, "    runtime UNVERIFIED (composition differs from the 2026-10-19 reference)\n")
		}
		fmt.Fprintf(w, "    %s\n", tb.RuntimeStatus)
		if tb.RendererVersion != 0 {
			fmt.Fprintf(w, "    renderer v%d · font %s · identity %s\n", tb.RendererVersion, hashutil.Short(tb.FontSHA256), hashutil.Short(tb.CacheIdentity))
		}
	}
	if tb.FooterError != "" {
		fmt.Fprintf(w, "    version.txt: %s\n", tb.FooterError)
	}
}

func printPackaging(w io.Writer, rep *packaging.Report) {
	fmt.Fprintf(w, "packaged (target %s)\n", rep.Target)
	fmt.Fprintf(w, "  title id  %s\n  build id  %s\n", rep.TitleID, rep.BuildID)
	fmt.Fprintf(w, "  archive   %s · %s bytes · %s\n", rep.Archive.Name, fmtInt(rep.Archive.Size), hashutil.Short(rep.Archive.SHA256))
	fmt.Fprintf(w, "  patch     %s · %s bytes · %s\n", rep.Patch.Name, fmtInt(rep.Patch.Size), hashutil.Short(rep.Patch.SHA256))
	fmt.Fprint(w, packaging.DescribeArtifacts(rep.Artifacts))
	for _, c := range rep.Checks {
		st := "PASS"
		if !c.Pass {
			st = "FAIL"
		}
		fmt.Fprintf(w, "  %-5s %-28s %s\n", st, c.ID, c.Detail)
	}
}

func printValidate(w io.Writer, rep *app.ValidateResult) {
	fmt.Fprintf(w, "validate · target %s\n\n", rep.Target)
	if rep.Error != "" {
		fmt.Fprintf(w, "validation error: %s\n", rep.Error)
	}
	fmt.Fprintf(w, "components\n")
	fmt.Fprintf(w, "  translations   %d record(s) · %s\n", rep.Components.TranslationsRecords, hashutil.Short(rep.Components.TranslationsSHA256))
	fmt.Fprintf(w, "  executable     %d record(s) · %s bytes · %s\n", rep.Components.ExecutablePatchRecs, fmtInt(rep.Components.ExecutablePatchSize), hashutil.Short(rep.Components.ExecutablePatchSHA))
	for _, r := range rep.Components.AddedResources {
		fmt.Fprintf(w, "  added          %s (%s)\n", r.Member, r.StateLabel())
	}
	printTitleBranding(w, rep.Components.TitleBranding)
	if rep.Components.Incomplete {
		fmt.Fprintf(w, "  PARTIAL BUILD  omitted by allow_missing_resources=true: %s\n", strings.Join(rep.Components.Omitted, ", "))
	}
	fail := 0
	for _, c := range rep.Checks {
		st := "PASS"
		if !c.Pass {
			st = "FAIL"
			fail++
		}
		fmt.Fprintf(w, "  %-5s %-28s %s\n", st, c.ID, c.Detail)
	}
	fmt.Fprintf(w, "\nvalidation ready=%v (%d failing check(s))\n", rep.Ready, fail)
	if rep.ReportPath != "" {
		fmt.Fprintf(w, "report: %s\n", rep.ReportPath)
	}
}

func printInspect(w io.Writer, res *app.InspectResult) {
	fmt.Fprintf(w, "output %s\n", res.Path)
	if res.CPK != nil {
		ins := res.CPK
		fmt.Fprintf(w, "  size %s · sha256 %s\n", fmtInt(ins.Size), ins.SHA256)
		fmt.Fprintf(w, "  members %d (%d compressed)\n", ins.MemberCount, ins.CompressedCount)
		printInspectTitle(w, res)
		for _, ch := range ins.Checks {
			st := "PASS"
			if !ch.Pass {
				st = "FAIL"
			}
			fmt.Fprintf(w, "  %-5s %-28s %s\n", st, ch.Title, ch.Detail)
		}
		return
	}
	fmt.Fprintf(w, "  target %s · sha256 %s\n", res.Target, hashutil.Short(res.ArchiveSHA))
	printInspectTitle(w, res)
	for _, e := range res.Entries {
		fmt.Fprintf(w, "  %-64s %10d  %s\n", e.Path, e.Size, hashutil.Short(e.SHA256))
	}
	for _, c := range res.Checks {
		st := "PASS"
		if !c.Pass {
			st = "FAIL"
		}
		fmt.Fprintf(w, "  %-5s %-28s %s\n", st, c.ID, c.Detail)
	}
}

// printInspectTitle states whether the inspected archive carries the
// title-branding member, detected from the bytes alone.
func printInspectTitle(w io.Writer, res *app.InspectResult) {
	fmt.Fprintf(w, "  title member included: %v\n", res.TitleIncluded)
	if res.TitleIncluded {
		fmt.Fprintf(w, "    runtime %s\n", res.TitleRuntime)
	}
	if res.TitleNote != "" {
		fmt.Fprintf(w, "    %s\n", res.TitleNote)
	}
}

func buildJSON(res *app.BuildResult) any {
	mod := ""
	if res.Packaging != nil {
		mod = res.Packaging.Release
	}
	return map[string]any{
		"target":      res.Packaging.Target,
		"tool":        build.ToolName,
		"version":     build.VersionString(),
		"mod_version": mod,
		"when_utc":    res.WhenUTC,
		"output_dir":  res.OutputDir,
		"archive":     res.Archive.Report,
		"components":  res.Components,
		"packaging":   res.Packaging,
		"artifacts":   res.Artifacts,
	}
}

func existsSuffix(b bool) string {
	if b {
		return " (exists)"
	}
	return ""
}

func filepathBase(p string) string {
	i := strings.LastIndexAny(p, `/\`)
	if i < 0 {
		return p
	}
	return p[i+1:]
}

func fmtInt(n int64) string { return fmt.Sprintf("%d", n) }

// describeTargets lists the supported target names for help output.
func describeTargets() string {
	names := []string{string(packaging.TargetAtmosphere), string(packaging.TargetEmulator), string(packaging.TargetBoth)}
	sort.Strings(names)
	return strings.Join(names, "|")
}

var _ = techdata.TitleID
