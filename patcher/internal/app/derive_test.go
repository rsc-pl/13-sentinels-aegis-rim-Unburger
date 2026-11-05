package app

import (
	"os"
	"path/filepath"
	"testing"

	"13s-patcher/internal/config"
	"13s-patcher/internal/extraction"
	"13s-patcher/internal/hashutil"
	"13s-patcher/internal/synth"
	"13s-patcher/internal/translations"
)

// syntheticState builds an overlay archive carrying one text bank and a matching
// resolved extraction state, so PlanDerivedBundle can be exercised directly.
func syntheticState(t *testing.T, items []string) *extraction.State {
	t.Helper()
	dir := t.TempDir()
	overlay, err := synth.Archive([]synth.Member{
		{Dir: "fms", Name: "a.fms", ID: 0, Logical: synth.Bank(items)},
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "overlay.cpk")
	if err := os.WriteFile(path, overlay, 0o644); err != nil {
		t.Fatal(err)
	}
	return &extraction.State{
		OverlayPath:   path,
		OverlaySHA256: hashutil.Bytes(overlay),
		OverlaySize:   int64(len(overlay)),
	}
}

// datasetWith wraps records in a dataset with a valid manifest.
func datasetWith(recs []translations.Record) *translations.Dataset {
	man := translations.BuildManifest(translations.ExpectedGame, translations.SourceIdentity{}, recs)
	return &translations.Dataset{Manifest: man, Records: recs}
}

// TestPlanDerivedBundleConsumerGates proves the consumer-level gates the loader
// cannot check on its own (they need the target bank): out-of-range index, a
// no-op change, and an undeclared control-token change are refused; a declared
// token change is allowed.
func TestPlanDerivedBundleConsumerGates(t *testing.T) {
	svc := &Service{WorkDir: t.TempDir(), Loc: config.Location{Mode: config.ModeUser}}
	items := []string{"Alpha", "@#(1)Begin@/b"}

	cases := []struct {
		name    string
		rec     translations.Record
		wantErr bool
	}{
		{"out of range", translations.Record{BankKey: "fms/a.fms", RecordIndex: 9, Text: "x"}, true},
		{"no-op", translations.Record{BankKey: "fms/a.fms", RecordIndex: 0, Text: "Alpha"}, true},
		{"undeclared token change", translations.Record{BankKey: "fms/a.fms", RecordIndex: 1, Text: "Start"}, true},
		{"declared token change", translations.Record{BankKey: "fms/a.fms", RecordIndex: 1, Text: "Start", TokensChanged: true}, false},
		{"unknown bank", translations.Record{BankKey: "fms/missing.fms", RecordIndex: 0, Text: "x"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := syntheticState(t, items)
			_, _, err := svc.PlanDerivedBundle(datasetWith([]translations.Record{tc.rec}), st)
			if tc.wantErr && err == nil {
				t.Fatal("expected the plan to be refused")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected the plan to be allowed: %v", err)
			}
		})
	}
}
