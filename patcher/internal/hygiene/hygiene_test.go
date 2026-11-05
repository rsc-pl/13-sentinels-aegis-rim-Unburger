package hygiene_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"13s-patcher/internal/hygiene"
)

// TestPublicTreeIsClean scans the complete public boundary: the repository
// root (source, docs, scripts, configs, fixtures and file names), the vendored
// tree for project-specific markers, the Git index and the private-instruction
// ignore boundary. It fails on any leak.
func TestPublicTreeIsClean(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	var findings []hygiene.Finding

	scan, err := hygiene.Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	findings = append(findings, scan...)

	vendor, err := hygiene.ScanVendor(root)
	if err != nil {
		t.Fatal(err)
	}
	findings = append(findings, vendor...)

	tracked, err := hygiene.ScanTracked(root)
	if err != nil && !errors.Is(err, hygiene.ErrNotGit) {
		t.Fatal(err)
	}
	findings = append(findings, tracked...)

	boundary, err := hygiene.CheckPrivateBoundary(root)
	if err != nil && !errors.Is(err, hygiene.ErrNotGit) {
		t.Fatal(err)
	}
	findings = append(findings, boundary...)

	if len(findings) > 0 {
		var b strings.Builder
		for _, f := range findings {
			b.WriteString(f.String())
			b.WriteByte('\n')
		}
		t.Fatalf("public tree contains %d forbidden marker(s):\n%s", len(findings), b.String())
	}
}

// TestScannerDetectsKnownMarkers proves the scanner actually fails on a leak.
func TestScannerDetectsKnownMarkers(t *testing.T) {
	dir := t.TempDir()
	leak := filepath.Join(dir, "leak.txt")
	// Build the offending text from fragments so this test file stays clean.
	line := strings.Join([]string{"mu", "se"}, "") + " " + strings.Join([]string{"chi", "se"}, "") + "/current\n"
	if err := os.WriteFile(leak, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	findings, err := hygiene.Scan(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) == 0 {
		t.Fatal("scanner did not detect a planted leak")
	}
}
