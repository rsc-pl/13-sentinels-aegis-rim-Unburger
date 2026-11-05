// Command bundle-export is a maintainer utility (not part of the end-user CLI).
// It converts a neutral authoring document into a distributable patch-data
// bundle: it hashes every replacement, writes the content files and emits the
// manifest. See docs/EXPORTING.md.
//
// It never reads anything but the input document it is given.
//
// usage: bundle-export -in authoring.json -out bundle-dir
package main

import (
	"flag"
	"fmt"
	"os"

	"13s-patcher/internal/patchdata"
)

func main() {
	in := flag.String("in", "", "authoring document (JSON)")
	out := flag.String("out", "", "destination bundle directory")
	flag.Parse()
	if *in == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "usage: bundle-export -in authoring.json -out bundle-dir")
		os.Exit(2)
	}
	raw, err := os.ReadFile(*in)
	if err != nil {
		fail(err)
	}
	doc, err := patchdata.DecodeExportInput(raw)
	if err != nil {
		fail(err)
	}
	b, err := patchdata.ExportBundle(*out, doc)
	if err != nil {
		fail(err)
	}
	fmt.Printf("wrote bundle %s %s -> %s\n", b.Manifest.BundleID, b.Manifest.BundleVersion, *out)
	fmt.Printf("  operations  %d\n", b.OperationCount())
	fmt.Printf("  manifest    %s\n", b.ManifestSHA256)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
