package extraction_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"13s-patcher/internal/extraction"
	"13s-patcher/internal/hashutil"
	"13s-patcher/internal/synth"
)

// fixtureExtractor copies a prepared overlay and bank set into the destination.
type fixtureExtractor struct {
	overlay []byte
	banks   map[string][]byte
	fail    bool
}

func (f *fixtureExtractor) Describe() string { return "fixture extractor" }
func (f *fixtureExtractor) Available() error {
	if f.fail {
		return errors.New("extractor unavailable")
	}
	return nil
}
func (f *fixtureExtractor) Extract(ctx context.Context, req extraction.ExtractRequest) error {
	if err := f.Available(); err != nil {
		return err
	}
	for _, a := range req.Artifacts {
		switch a {
		case extraction.ArtifactOverlay:
			dir := filepath.Join(req.DestDir, extraction.OverlayDir)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(dir, "produced.cpk"), f.overlay, 0o644); err != nil {
				return err
			}
		case extraction.ArtifactBaseBanks:
			dir := filepath.Join(req.DestDir, extraction.BanksSubdir)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
			for name, raw := range f.banks {
				if err := os.WriteFile(filepath.Join(dir, name), raw, 0o644); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func fixture(t *testing.T) *fixtureExtractor {
	t.Helper()
	overlay, err := synth.Archive([]synth.Member{
		{Dir: "fms", Name: "scripttext.fms", ID: 1, Logical: synth.Bank([]string{"a", "b"})},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &fixtureExtractor{
		overlay: overlay,
		banks: map[string][]byte{
			"Fms__SysMsg.fms": synth.Bank([]string{"one", "two"}),
		},
	}
}

func request(t *testing.T, f *fixtureExtractor, cacheDir string) *extraction.Request {
	t.Helper()
	return &extraction.Request{
		CacheDir:            cacheDir,
		Extractor:           f,
		ExpectOverlaySize:   int64(len(f.overlay)),
		ExpectOverlaySHA256: hashutil.Bytes(f.overlay),
		MinBankCount:        1,
		Ctx:                 context.Background(),
	}
}

func TestResolvePopulatesAndReusesCache(t *testing.T) {
	f := fixture(t)
	cacheDir := filepath.Join(t.TempDir(), "cache")

	req := request(t, f, cacheDir)
	st, err := extraction.Resolve(req)
	if err != nil {
		t.Fatal(err)
	}
	if st.BankCount != 1 || st.OverlayName != "produced.cpk" {
		t.Fatalf("unexpected state after populating: %+v", st)
	}
	if !strings.Contains(strings.Join(st.Notes, "; "), "populated") {
		t.Fatalf("expected a population note, got %v", st.Notes)
	}
	if _, err := os.Stat(filepath.Join(cacheDir, extraction.CacheStateFile)); err != nil {
		t.Fatal(err)
	}

	// second run: no extractor at all, must reuse the verified cache
	reuse := request(t, f, cacheDir)
	reuse.Extractor = nil
	st2, err := extraction.Resolve(reuse)
	if err != nil {
		t.Fatal(err)
	}
	if !st2.FromCache {
		t.Fatalf("expected a cache reuse, got %+v", st2)
	}
	if !strings.Contains(strings.Join(st2.Notes, "; "), "reused verified cache") {
		t.Fatalf("expected a reuse note, got %v", st2.Notes)
	}
	if p, name, ok := extraction.ReadCachedOverlay(cacheDir); !ok || name != "produced.cpk" || !strings.HasSuffix(p, "produced.cpk") {
		t.Fatalf("cached overlay lookup = %q, %q, %v", p, name, ok)
	}
}

func TestVerifyOnlyNeverPopulates(t *testing.T) {
	f := fixture(t)
	cacheDir := filepath.Join(t.TempDir(), "cache")
	req := request(t, f, cacheDir)
	req.VerifyOnly = true
	if _, err := extraction.Resolve(req); err == nil {
		t.Fatal("VerifyOnly must fail on an empty cache")
	}
	if _, err := os.Stat(filepath.Join(cacheDir, extraction.CacheStateFile)); err == nil {
		t.Fatal("VerifyOnly must not write a cache state")
	}
}

func TestCacheTamperIsDetected(t *testing.T) {
	f := fixture(t)
	cacheDir := filepath.Join(t.TempDir(), "cache")
	if _, err := extraction.Resolve(request(t, f, cacheDir)); err != nil {
		t.Fatal(err)
	}
	bank := filepath.Join(cacheDir, extraction.BanksSubdir, "Fms__SysMsg.fms")
	if err := os.WriteFile(bank, synth.Bank([]string{"tampered"}), 0o644); err != nil {
		t.Fatal(err)
	}
	req := request(t, f, cacheDir)
	req.VerifyOnly = true
	if _, err := extraction.Resolve(req); err == nil {
		t.Fatal("a tampered bank must be refused")
	}
}

func TestCacheDetectsWrongOverlayDigest(t *testing.T) {
	f := fixture(t)
	cacheDir := filepath.Join(t.TempDir(), "cache")
	if _, err := extraction.Resolve(request(t, f, cacheDir)); err != nil {
		t.Fatal(err)
	}
	overlay := filepath.Join(cacheDir, extraction.OverlayDir, "produced.cpk")
	if err := os.WriteFile(overlay, []byte("replaced"), 0o644); err != nil {
		t.Fatal(err)
	}
	req := request(t, f, cacheDir)
	req.VerifyOnly = true
	if _, err := extraction.Resolve(req); err == nil {
		t.Fatal("a replaced overlay must be refused")
	}
}

func TestMissingEverythingReportsClearDiagnostic(t *testing.T) {
	cacheDir := filepath.Join(t.TempDir(), "cache")
	req := &extraction.Request{CacheDir: cacheDir, MinBankCount: 1}
	_, err := extraction.Resolve(req)
	if err == nil {
		t.Fatal("expected a failure")
	}
	msg := err.Error()
	if !strings.Contains(msg, "extraction is required") {
		t.Fatalf("diagnostic should explain what is needed: %v", err)
	}
}

func TestUnavailableExtractorFailsClosed(t *testing.T) {
	f := fixture(t)
	f.fail = true
	cacheDir := filepath.Join(t.TempDir(), "cache")
	_, err := extraction.Resolve(request(t, f, cacheDir))
	if err == nil {
		t.Fatal("expected a failure")
	}
	if !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("expected an availability diagnostic: %v", err)
	}
}

func TestResolveReusesExistingExtractionWithoutCopyingByHand(t *testing.T) {
	f := fixture(t)
	// an existing extraction somewhere else: overlay + banks discovered by hash
	src := t.TempDir()
	overlayDir := filepath.Join(src, "somewhere")
	if err := os.MkdirAll(overlayDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(overlayDir, "existing.cpk"), f.overlay, 0o644); err != nil {
		t.Fatal(err)
	}
	banksDir := filepath.Join(src, "banks")
	if err := os.MkdirAll(banksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(banksDir, "Fms__SysMsg.fms"), f.banks["Fms__SysMsg.fms"], 0o644); err != nil {
		t.Fatal(err)
	}
	cacheDir := filepath.Join(t.TempDir(), "cache")
	req := request(t, f, cacheDir)
	req.Extractor = nil
	req.SearchRoots = []string{src}
	st, err := extraction.Resolve(req)
	if err != nil {
		t.Fatal(err)
	}
	if st.OverlayName != "existing.cpk" {
		t.Fatalf("expected the existing overlay to be adopted, got %+v", st)
	}
}
