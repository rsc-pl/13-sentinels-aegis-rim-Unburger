package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"13s-patcher/internal/hashutil"
	"13s-patcher/internal/packaging"
	"13s-patcher/internal/techdata"
	"13s-patcher/internal/translations"
)

// DestinationKind classifies one target's output archive before a build.
type DestinationKind string

// Destination classifications.
const (
	// DestMissing means no archive exists yet.
	DestMissing DestinationKind = "missing"
	// DestIdentical means the existing archive is valid and matches the last
	// recorded build of the current dataset, so it may safely be reused.
	DestIdentical DestinationKind = "identical-valid"
	// DestDifferent means the existing archive is valid but does not match the
	// current expected output (a different dataset or an older build).
	DestDifferent DestinationKind = "different"
	// DestUnverifiable means the existing archive could not be validated as one
	// this tool produced.
	DestUnverifiable DestinationKind = "unverifiable"
)

// DestinationState describes one target's output archive before a build. The
// identity fields are always reported; the classification is the tool's honest
// best effort, never a fabricated claim about content it did not verify.
type DestinationState struct {
	Target packaging.Target `json:"target"`
	Label  string           `json:"label"`
	Path   string           `json:"path"`
	Exists bool             `json:"exists"`
	Size   int64            `json:"size,omitempty"`
	SHA256 string           `json:"sha256,omitempty"`
	Kind   DestinationKind  `json:"kind"`
	// Valid is the layout-check result for an existing archive.
	Valid bool `json:"valid"`
	// Recorded is true when the archive matched the last recorded build.
	Recorded bool `json:"recorded,omitempty"`
	// Detail is a short human-readable explanation.
	Detail string `json:"detail,omitempty"`
}

type recordedTarget struct {
	Target     string             `json:"target"`
	Artifact   packaging.Artifact `json:"artifact"`
	Components PatchComponents    `json:"components"`
}

// InspectDestinations reports the state of every output archive a build for the
// given target would touch. It never writes anything.
func (s *Service) InspectDestinations(ctx context.Context, target packaging.Target) ([]DestinationState, error) {
	if target == "" {
		target = s.Target()
	}
	req := packaging.Request{
		PatchBytes: techdata.ExecutablePatchBytes(),
		BuildID:    techdata.BuildIDUpper(),
		TitleID:    techdata.TitleID(),
		Release:    s.ReleaseLabel(),
		OutputDir:  s.OutputDirOrDefault(),
	}
	datasetSHA := ""
	if ds, err := translations.Load(s.TranslationsDir()); err == nil {
		datasetSHA = ds.Manifest.ChangesSHA256
	}
	var out []DestinationState
	for _, t := range target.Targets() {
		if err := ctx.Err(); err != nil {
			return out, ErrCancelled
		}
		p := packaging.ArchivePath(req, t)
		st := DestinationState{Target: t, Label: t.Label(), Path: p, Kind: DestMissing}
		fi, err := os.Stat(p)
		if err != nil || fi.IsDir() {
			out = append(out, st)
			continue
		}
		st.Exists = true
		st.Size = fi.Size()
		if sum, _, err := hashutil.File(p); err == nil {
			st.SHA256 = sum
		}
		checks, verr := packaging.ValidateFile(p, t, req)
		if verr != nil {
			st.Kind = DestUnverifiable
			st.Detail = "layout could not be verified: " + verr.Error()
			out = append(out, st)
			continue
		}
		st.Valid = true
		for _, c := range checks {
			if !c.Pass {
				st.Valid = false
			}
		}
		if !st.Valid {
			st.Kind = DestUnverifiable
			st.Detail = "layout checks failed"
			out = append(out, st)
			continue
		}
		rec, ok := s.loadRecordedTarget(t)
		switch {
		case !ok:
			st.Kind = DestDifferent
			st.Detail = "valid archive, but no recorded build to compare against"
		case rec.Artifact.SHA256 != st.SHA256:
			st.Kind = DestDifferent
			st.Detail = "valid archive, differs from the last recorded build"
		case datasetSHA != "" && rec.Components.TranslationsSHA256 != "" && rec.Components.TranslationsSHA256 != datasetSHA:
			st.Kind = DestDifferent
			st.Detail = "valid archive, built from a different translation dataset"
		default:
			st.Kind = DestIdentical
			st.Recorded = true
			st.Detail = "matches the last recorded build"
		}
		out = append(out, st)
	}
	return out, nil
}

func (s *Service) loadRecordedTarget(t packaging.Target) (recordedTarget, bool) {
	path := filepath.Join(s.ReportsDir(), "report-"+string(t)+".json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return recordedTarget{}, false
	}
	var rt recordedTarget
	if err := json.Unmarshal(raw, &rt); err != nil {
		return recordedTarget{}, false
	}
	if rt.Artifact.SHA256 == "" {
		return recordedTarget{}, false
	}
	return rt, true
}
