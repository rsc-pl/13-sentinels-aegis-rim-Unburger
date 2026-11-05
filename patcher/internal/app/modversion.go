package app

import (
	"strings"

	"13s-patcher/internal/modversion"
)

// ModVersion returns the Unburger Mod version read from the repository-root
// version.txt. It never invents a value: a missing or invalid file returns an
// actionable error instead.
func (s *Service) ModVersion() (string, error) {
	return modversion.Resolve(s.WorkDir, s.Loc.Root)
}

// ModVersionStatus reports the mod version together with a short diagnostic
// when it cannot be loaded. Exactly one of the two return values is set.
func (s *Service) ModVersionStatus() (version, problem string) {
	v, err := modversion.Resolve(s.WorkDir, s.Loc.Root)
	if err != nil {
		return "", err.Error()
	}
	return v, ""
}

// ModVersionDisplay renders the branded dashboard label. When the version
// cannot be loaded it renders a diagnostic marker, never a fabricated number.
func (s *Service) ModVersionDisplay() string {
	v, err := modversion.Resolve(s.WorkDir, s.Loc.Root)
	if err != nil {
		return modversion.Display("")
	}
	return modversion.Display(v)
}

// ReleaseLabel is the release label used in output file names and packaging
// reports: the explicit configured release_version when set, otherwise the mod
// version from version.txt. It returns "" only when neither is available, which
// keeps the historical unlabelled file names in a tree without version.txt.
func (s *Service) ReleaseLabel() string {
	if r := strings.TrimSpace(s.Cfg.ReleaseVersion); r != "" {
		return r
	}
	if v, err := s.ModVersion(); err == nil {
		return v
	}
	return ""
}
