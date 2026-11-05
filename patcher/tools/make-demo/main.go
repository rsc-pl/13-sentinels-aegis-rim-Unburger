// Command make-demo writes a self-contained synthetic demo tree (a small
// game-style archive plus a matching patch-data bundle) so the pipeline can be
// tried end to end without any real game data.
//
// usage: make-demo -out examples/demo
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"13s-patcher/internal/fixture"
)

func main() {
	out := flag.String("out", "examples/demo", "destination directory")
	flag.Parse()
	abs, err := filepath.Abs(*out)
	if err != nil {
		fail(err)
	}
	f, err := fixture.WriteDemo(abs)
	if err != nil {
		fail(err)
	}
	// Also drop the synthetic base assets the added resources are composed
	// over, so the demo builds without any real game data.
	if err := fixture.PlateBase(abs); err != nil {
		fail(err)
	}
	if err := fixture.TitleBase(abs); err != nil {
		fail(err)
	}
	fmt.Printf("demo written to %s\n", abs)
	fmt.Printf("  source archive  %s\n", f.SourceArchive)
	fmt.Printf("  patch data      %s\n", f.BundleDir)
	fmt.Printf("  output dir      %s\n", f.OutputDir)
	fmt.Printf("try:  13s-patcher build -workdir %s -source %s -patch-data %s -output %s\n",
		abs, f.SourceArchive, f.BundleDir, f.OutputDir)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
