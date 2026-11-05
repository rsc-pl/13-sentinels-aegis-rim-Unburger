// Command dataset-hygiene scans a translation dataset directory for internal
// markers. It is a standalone helper for dataset authors; the repository-wide
// public-hygiene scan applies the same field-aware rule to the shipped dataset.
//
// usage: dataset-hygiene -dir <dataset-dir>
package main

import (
	"flag"
	"fmt"
	"os"

	"13s-patcher/internal/hygiene"
)

func main() {
	dir := flag.String("dir", "", "dataset directory to scan")
	flag.Parse()
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "usage: dataset-hygiene -dir <dataset-dir>")
		os.Exit(2)
	}
	if fi, err := os.Stat(*dir); err != nil || !fi.IsDir() {
		fmt.Fprintf(os.Stderr, "dataset-hygiene: %s is not a directory\n", *dir)
		os.Exit(2)
	}
	findings, err := hygiene.ScanDataset(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	if len(findings) > 0 {
		fmt.Printf("DATASET HYGIENE FAIL: %d finding(s)\n", len(findings))
		for _, f := range findings {
			fmt.Println("    " + f.String())
		}
		os.Exit(1)
	}
	fmt.Printf("dataset hygiene check PASSED (%s)\n", *dir)
}
