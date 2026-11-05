package app

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"13s-patcher/internal/build"
	"13s-patcher/internal/cpk"
	"13s-patcher/internal/extraction"
	"13s-patcher/internal/fmsb"
	"13s-patcher/internal/gameprofile"
	"13s-patcher/internal/hashutil"
	"13s-patcher/internal/markup"
	"13s-patcher/internal/patchdata"
	"13s-patcher/internal/translations"
)

// DerivedStats summarises a planned derived bundle.
type DerivedStats struct {
	Members         int      `json:"members"`
	Operations      int      `json:"operations"`
	EditedBanks     int      `json:"edited_banks"`
	AddedBanks      int      `json:"added_banks"`
	TokenExceptions int      `json:"token_exceptions"`
	MemberLogical   []string `json:"-"`
}

// DerivedBundle is a written derived bundle.
type DerivedBundle struct {
	Dir            string `json:"dir"`
	BundleID       string `json:"bundle_id"`
	BundleVersion  string `json:"bundle_version"`
	ManifestSHA256 string `json:"manifest_sha256"`
	Operations     int    `json:"operations"`
	Stats          *DerivedStats
}

// PlanDerivedBundle turns the public dataset into a patch-data bundle input
// without writing anything. It resolves every record against the effective
// overlay archive (or the base bank directory for members the overlay does not
// carry) and verifies the baseline text and control tokens. It fails closed:
// any mismatch aborts.
func (s *Service) PlanDerivedBundle(ds *translations.Dataset, st *extraction.State) (*patchdata.ExportInput, *DerivedStats, error) {
	if ds == nil || st == nil {
		return nil, nil, fmt.Errorf("derive: dataset and resolved inputs are required")
	}
	raw, err := os.ReadFile(st.OverlayPath)
	if err != nil {
		return nil, nil, fmt.Errorf("derive: overlay archive: %w", err)
	}
	archive, err := cpk.Parse(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("derive: overlay archive: %w", err)
	}

	// group records by member, preserving the dataset's sorted order
	type group struct {
		member  string
		records []translations.Record
	}
	var groups []group
	index := map[string]int{}
	for _, r := range ds.Records {
		k := translations.BankKey(r.BankKey)
		gi, ok := index[k]
		if !ok {
			index[k] = len(groups)
			groups = append(groups, group{member: r.BankKey})
			gi = len(groups) - 1
		}
		groups[gi].records = append(groups[gi].records, r)
	}

	stats := &DerivedStats{}
	var ops []patchdata.ExportOperation
	for _, g := range groups {
		key := translations.BankKey(g.member)
		var logical []byte
		added := false
		if m := archive.MemberByPath(key); m != nil {
			logical, err = archive.Logical(m)
			if err != nil {
				return nil, nil, fmt.Errorf("derive: %s: decode: %w", g.member, err)
			}
		} else {
			if st.BaseBanksDir == "" {
				return nil, nil, fmt.Errorf("derive: member %q is not present in the overlay archive and no base bank directory is available", g.member)
			}
			added = true
			path, ferr := findBankFile(st.BaseBanksDir, g.member)
			if ferr != nil {
				return nil, nil, ferr
			}
			logical, err = os.ReadFile(path)
			if err != nil {
				return nil, nil, fmt.Errorf("derive: %s: base bank: %w", g.member, err)
			}
		}
		logicalSHA := hashutil.Bytes(logical)
		bank, err := fmsb.Parse(logical)
		if err != nil {
			return nil, nil, fmt.Errorf("derive: %s: not a text bank: %w", g.member, err)
		}
		for _, r := range g.records {
			if r.RecordIndex < 0 || r.RecordIndex >= len(bank.Strings) {
				return nil, nil, fmt.Errorf("derive: %s#%d: record index out of range (%d)", g.member, r.RecordIndex, len(bank.Strings))
			}
			orig := string(bank.Strings[r.RecordIndex])
			if orig == r.Text {
				return nil, nil, fmt.Errorf("derive: %s#%d: change is a no-op", g.member, r.RecordIndex)
			}
			ok, detail := markup.Equivalent(orig, r.Text)
			if !ok && !r.TokensChanged {
				return nil, nil, fmt.Errorf("derive: %s#%d: undeclared control-token change (%s)", g.member, r.RecordIndex, detail)
			}
			if !ok && r.TokensChanged {
				stats.TokenExceptions++
			}
			ops = append(ops, patchdata.ExportOperation{
				ID:                  opID(g.member, r.RecordIndex),
				Member:              g.member,
				RecordIndex:         r.RecordIndex,
				Original:            orig,
				Replacement:         r.Text,
				PreserveTokens:      true,
				AllowTokenChange:    r.TokensChanged,
				MemberLogicalSHA256: logicalSHA,
			})
		}
		stats.Members++
		stats.MemberLogical = append(stats.MemberLogical, fmt.Sprintf("%s %s", key, hashutil.Short(logicalSHA)))
		if added {
			stats.AddedBanks++
		} else {
			stats.EditedBanks++
		}
	}
	stats.Operations = len(ops)
	sort.Strings(stats.MemberLogical)

	in := &patchdata.ExportInput{
		BundleID:       "13s-fidelity-text",
		BundleVersion:  ds.Manifest.ChangesSHA256[:12],
		ReleaseVersion: ds.Manifest.ChangesSHA256[:12],
		Game: patchdata.GameInfo{
			Title: gameprofile.Title, TitleID: gameprofile.TitleIDBase,
			Region: gameprofile.Region, BaseVersion: gameprofile.BaseVersion,
		},
		FormatVersion: patchdata.SupportedFormatVersion,
		Sources: []patchdata.SourceSpec{{
			Role: build.RolePatchArchive, Name: "effective update overlay",
			Size: st.OverlaySize, SHA256: st.OverlaySHA256,
		}},
		Operations: ops,
		Output: patchdata.OutputSpec{
			MemberCountDelta:   stats.AddedBanks,
			PreserveUnmodified: true,
			Encoder:            encoderOrDefault(s.Cfg.Encoder),
		},
	}
	return in, stats, nil
}

// WriteDerivedBundle writes a planned bundle into the private state directory.
func (s *Service) WriteDerivedBundle(in *patchdata.ExportInput, ds *translations.Dataset) (*DerivedBundle, error) {
	dir := filepath.Join(s.DerivedDir(), ds.Manifest.ChangesSHA256[:12])
	if err := os.RemoveAll(dir); err != nil {
		return nil, err
	}
	b, err := patchdata.ExportBundle(dir, *in)
	if err != nil {
		return nil, err
	}
	return &DerivedBundle{
		Dir: dir, BundleID: b.Manifest.BundleID, BundleVersion: b.Manifest.BundleVersion,
		ManifestSHA256: b.ManifestSHA256, Operations: b.OperationCount(),
	}, nil
}

func opID(member string, index int) string {
	var b strings.Builder
	for _, r := range strings.ToLower(member) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String() + "-" + strconv.Itoa(index)
}

// findBankFile resolves a base bank file case-insensitively, so the on-disk
// capitalisation of the extracted bank directory never matters.
func findBankFile(dir, member string) (string, error) {
	want := strings.ToLower(build.BankFileBase(member))
	ents, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("derive: base bank directory: %w", err)
	}
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		if strings.ToLower(e.Name()) == want {
			return filepath.Join(dir, e.Name()), nil
		}
	}
	return "", fmt.Errorf("derive: base bank file for %q not found in %s", member, dir)
}

func encoderOrDefault(v string) string {
	if v == "" {
		return "chain"
	}
	return v
}
