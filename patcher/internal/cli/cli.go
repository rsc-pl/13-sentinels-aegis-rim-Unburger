// Package cli implements the command-line interface. It drives the same
// application services as the TUI and never re-implements pipeline logic.
//
// Running the executable with no arguments opens the interactive TUI when stdin
// and stdout are terminals, and prints a plain-text status summary otherwise
// (pipes, CI, redirects).
package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"13s-patcher/internal/app"
	"13s-patcher/internal/build"
	"13s-patcher/internal/modversion"
	"13s-patcher/internal/packaging"
	"13s-patcher/internal/tui"
)

// common flags shared by all commands.
type common struct {
	config      string
	source      string
	patchData   string
	output      string
	encoder     string
	jsonOut     string
	workDir     string
	projectRoot string
	targetName  string
}

// target resolves the requested installation target, defaulting to the
// configured value and then to "both".
func (c *common) target(svc *app.Service) (packaging.Target, error) {
	if c.targetName == "" {
		return svc.Target(), nil
	}
	return packaging.ParseTarget(c.targetName)
}

func (c *common) bind(fs *flag.FlagSet) {
	fs.StringVar(&c.config, "config", "", "settings file (overrides auto-detection)")
	fs.StringVar(&c.source, "source", "", "explicit effective update overlay archive")
	fs.StringVar(&c.patchData, "patch-data", "", "patch-data bundle directory (advanced override)")
	fs.StringVar(&c.output, "output", "", "output directory")
	fs.StringVar(&c.encoder, "encoder", "", "codec encoder: chain (default) or old")
	fs.StringVar(&c.jsonOut, "json", "", "write a JSON document here ('-' for stdout)")
	fs.StringVar(&c.workDir, "workdir", "", "working directory (default: current directory)")
	fs.StringVar(&c.projectRoot, "project-root", "", "explicit project root (default: auto-detected)")
	fs.StringVar(&c.targetName, "target", "", "installation target: atmosphere, emulator or both")
}

func (c *common) service() (*app.Service, error) {
	return app.New(app.Options{
		ConfigPath:    c.config,
		WorkDir:       c.workDir,
		ProjectRoot:   c.projectRoot,
		SourceArchive: c.source,
		PatchDataDir:  c.patchData,
		OutputDir:     c.output,
		Encoder:       c.encoder,
	})
}

// emit writes either a JSON document or text.
func emit(jsonOut string, v any, printText func(w io.Writer)) error {
	if jsonOut == "" {
		printText(os.Stdout)
		return nil
	}
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if jsonOut == "-" {
		_, err = os.Stdout.Write(raw)
		return err
	}
	return os.WriteFile(jsonOut, raw, 0o644)
}

// Main runs the CLI and returns a process exit code.
func Main() int { return Run(os.Args[1:]) }

// Run dispatches a command line.
func Run(args []string) int {
	if len(args) == 0 {
		if tui.TerminalCapable() {
			return runTUI(nil)
		}
		return cmdStatus(nil, true)
	}
	switch args[0] {
	case "status":
		return cmdStatus(args[1:], false)
	case "doctor":
		return cmdDoctor(args[1:])
	case "setup":
		return cmdSetup(args[1:])
	case "sources":
		return cmdSources(args[1:])
	case "preflight":
		return cmdPreflight(args[1:])
	case "build":
		return cmdBuild(args[1:])
	case "package":
		return cmdPackage(args[1:])
	case "validate":
		return cmdValidate(args[1:])
	case "inspect":
		return cmdInspect(args[1:])
	case "tui", "dashboard":
		return runTUI(args[1:])
	case "version", "-v", "--version":
		return cmdVersion(args[1:])
	case "help", "-h", "--help":
		usage(os.Stdout)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", args[0])
		usage(os.Stderr)
		return 2
	}
}

func runTUI(args []string) int {
	var c common
	fs := flag.NewFlagSet("tui", flag.ExitOnError)
	c.bind(fs)
	_ = fs.Parse(args)
	svc, err := c.service()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if err := tui.Run(svc); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}

func cmdVersion(args []string) int {
	var c common
	fs := flag.NewFlagSet("version", flag.ExitOnError)
	c.bind(fs)
	_ = fs.Parse(args)
	type versionInfo struct {
		Tool       string `json:"tool"`
		Version    string `json:"version"`
		Commit     string `json:"commit,omitempty"`
		Built      string `json:"built,omitempty"`
		ModVersion string `json:"mod_version,omitempty"`
		ModError   string `json:"mod_version_error,omitempty"`
	}
	info := versionInfo{Tool: build.ToolName, Version: build.ToolVersion, Commit: build.Commit, Built: build.BuildDate}
	if mod, err := modversion.Resolve(c.workDir, c.projectRoot); err == nil {
		info.ModVersion = mod
	} else {
		info.ModError = err.Error()
	}
	_ = emit(c.jsonOut, info, func(w io.Writer) {
		fmt.Fprintf(w, "%s %s\n", build.ToolName, build.VersionString())
		if info.ModError != "" {
			fmt.Fprintf(w, "Unburger Mod unknown — %s\n", info.ModError)
			return
		}
		fmt.Fprintf(w, "%s\n", modversion.Display(info.ModVersion))
	})
	return 0
}

func usage(w io.Writer) {
	fmt.Fprint(w, `13s-patcher — 13 Sentinels Unburger Patch Builder

usage: 13s-patcher [command] [flags]
       13s-patcher                 (interactive dashboard on a terminal, plain status when piped)

commands
  status      show detected inputs, translation data, cache, output and last results
  doctor      check the environment, paths, storage mode and configuration
  setup       discover local inputs and write the settings once (first run)
  sources     locate and identify the local game containers
  preflight   validate inputs, translation data, cache and output without building
  build       rebuild the archive once, then produce the per-target archives
  package     re-produce the per-target archives from an existing built archive
  validate    preflight + build + package into a temporary directory + verify
  inspect     inspect a produced archive or the rebuilt archive
  version     print the tool version
  help        show this help

common flags
  -config FILE        settings file (default: project-private, else user config)
  -project-root DIR   explicit project root (default: auto-detected)
  -source FILE        explicit effective update overlay archive
  -patch-data DIR     patch-data bundle directory (advanced override)
  -output DIR         output directory
  -encoder NAME       codec encoder: chain (default) or old
  -workdir DIR        working directory (default: current directory)
  -json FILE          write a JSON document ('-' for stdout)
  -target NAME        installation target: atmosphere, emulator or both (default both)
  -force              (build/package) allow replacing existing output files

outputs
  one archive per installation target, written to the configured output directory
  (default: the project state directory's out/ folder):
    13-Sentinels-Patch-<release>-Atmosphere.zip   install at the root of the SD card
    13-Sentinels-Patch-<release>-Emulators.zip    install as a mod folder in the emulator
  <release> is the mod version from version.txt unless release_version is set.
  the rebuilt archive itself is an intermediate kept in the work cache, not an
  output; -force (build/package) replaces existing archives.
`)
}
