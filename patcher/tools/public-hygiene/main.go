// Command public-hygiene is the single entry point for the public-repository
// hygiene checks. It reuses the 13s-patcher/internal/hygiene package; it is not a
// second scanner.
//
// Full mode scans the public tree (contents, file names and shipped binary
// metadata), the vendored tree for project-specific markers, the Git index for
// accidentally tracked private files, and the ignore boundary for private
// instruction files. Changed mode checks only the listed (or Git-detected)
// files. Release mode checks a staged release directory.
//
// usage:
//
//	public-hygiene -root <repo> [-changed [files...]] [-fix] [-json] [-require-git]
//	public-hygiene -root <staged-release> -release
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"13s-patcher/internal/hygiene"
)

type report struct {
	OK       bool              `json:"ok"`
	Root     string            `json:"root"`
	Mode     string            `json:"mode"`
	Files    int               `json:"files,omitempty"`
	Count    int               `json:"count"`
	Findings []hygiene.Finding `json:"findings"`
	Fixed    []string          `json:"fixed,omitempty"`
	Notes    []string          `json:"notes,omitempty"`
}

func main() {
	root := flag.String("root", "", "repository root (default: detected from the current directory)")
	changed := flag.Bool("changed", false, "scan only the listed files, or the Git changed/untracked set")
	fix := flag.Bool("fix", false, "apply safe auto-corrections (gofmt, trailing whitespace) in changed mode, then rescan")
	release := flag.Bool("release", false, "scan a staged release directory")
	jsonOut := flag.Bool("json", false, "emit machine-readable JSON")
	requireGit := flag.Bool("require-git", false, "fail when the Git-backed checks cannot run")
	flag.Parse()
	files := flag.Args()

	if *root == "" {
		detected, err := detectRoot()
		if err != nil {
			fmt.Fprintln(os.Stderr, "public-hygiene:", err)
			os.Exit(2)
		}
		*root = detected
	}
	abs, err := filepath.Abs(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "public-hygiene:", err)
		os.Exit(2)
	}
	*root = abs

	if *fix && !*changed && len(files) == 0 {
		fmt.Fprintln(os.Stderr, "public-hygiene: -fix requires -changed or an explicit file list")
		os.Exit(2)
	}
	if *release && (*changed || *fix) {
		fmt.Fprintln(os.Stderr, "public-hygiene: -release cannot be combined with -changed or -fix")
		os.Exit(2)
	}

	rep := report{Root: *root, Mode: "full"}
	var notes []string
	if *release {
		rep.Mode = "release"
		rep.Findings, err = hygiene.CheckReleaseTree(*root)
	} else if *changed {
		rep.Mode = "changed"
		if len(files) == 0 {
			files, err = hygiene.ChangedFiles(*root)
			if err != nil {
				fmt.Fprintln(os.Stderr, "public-hygiene: changed-file detection unavailable:", err)
				os.Exit(2)
			}
		}
		rep.Files = len(files)
		rep.Findings, err = scanFiles(*root, files)
		if *fix && err == nil {
			fixed, ferr := hygiene.FixSafe(*root, files)
			if ferr != nil {
				err = ferr
			} else {
				rep.Fixed = fixed
				// Re-scan after the deterministic fixes and repeat until stable.
				for i := 0; i < 3 && len(fixed) > 0; i++ {
					rep.Findings, err = scanFiles(*root, files)
					if err != nil || len(rep.Findings) == 0 {
						break
					}
					fixed, ferr = hygiene.FixSafe(*root, files)
					if ferr != nil {
						err = ferr
						break
					}
					rep.Fixed = append(rep.Fixed, fixed...)
					if len(fixed) == 0 {
						break
					}
				}
			}
		}
	} else {
		rep.Findings, err = hygiene.Scan(*root)
		if err == nil {
			vendor, verr := hygiene.ScanVendor(*root)
			if verr != nil {
				err = verr
			} else {
				rep.Findings = append(rep.Findings, vendor...)
			}
		}
		if err == nil {
			tracked, terr := hygiene.ScanTracked(*root)
			if terr != nil {
				if errors.Is(terr, hygiene.ErrNotGit) && !*requireGit {
					notes = append(notes, "Git checks skipped: not a Git repository")
				} else {
					err = terr
				}
			} else {
				rep.Findings = append(rep.Findings, tracked...)
			}
		}
		if err == nil {
			boundary, berr := hygiene.CheckPrivateBoundary(*root)
			if berr != nil {
				if errors.Is(berr, hygiene.ErrNotGit) && !*requireGit {
					notes = append(notes, "private-instruction boundary checks skipped: not a Git repository")
				} else {
					err = berr
				}
			} else {
				rep.Findings = append(rep.Findings, boundary...)
			}
		}
		hygiene.SortFindings(rep.Findings)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "public-hygiene:", err)
		os.Exit(2)
	}
	rep.Notes = notes
	rep.Count = len(rep.Findings)
	rep.OK = rep.Count == 0

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			fmt.Fprintln(os.Stderr, "public-hygiene:", err)
			os.Exit(2)
		}
	} else {
		for _, n := range rep.Notes {
			fmt.Fprintln(os.Stderr, "note:", n)
		}
		for _, f := range rep.Findings {
			fmt.Println(f.String())
		}
		if len(rep.Fixed) > 0 {
			fmt.Printf("auto-corrected %d file(s): %s\n", len(rep.Fixed), strings.Join(rep.Fixed, ", "))
		}
		if rep.OK {
			fmt.Printf("public hygiene PASSED (%s mode, %s)\n", rep.Mode, rep.Root)
		} else {
			fmt.Printf("public hygiene FAILED (%s mode, %s): %d finding(s)\n", rep.Mode, rep.Root, rep.Count)
		}
	}
	if !rep.OK {
		os.Exit(1)
	}
}

func scanFiles(root string, files []string) ([]hygiene.Finding, error) {
	var out []hygiene.Finding
	for _, rel := range files {
		f, err := hygiene.ScanFile(root, rel)
		out = append(out, f...)
		if err != nil {
			return out, err
		}
	}
	hygiene.SortFindings(out)
	return out, nil
}

func detectRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "patcher", "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("cannot detect repository root; pass -root")
		}
		dir = parent
	}
}
