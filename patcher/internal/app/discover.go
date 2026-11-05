package app

import (
	"context"
	"fmt"
	"os"

	"13s-patcher/internal/extraction"
	"13s-patcher/internal/gameprofile"
	"13s-patcher/internal/localsources"
	"13s-patcher/internal/translations"
)

// DiscoverSources locates and identifies the local containers.
func (s *Service) DiscoverSources() (*localsources.Report, error) {
	explicit := map[string]string{}
	if v := s.resolve(s.Cfg.RomsBase); v != "" {
		explicit["base"] = v
	}
	if v := s.resolve(s.Cfg.RomsUpdate); v != "" {
		explicit["update"] = v
	}
	if v := s.resolve(s.Cfg.RomsExtra); v != "" {
		explicit["extra"] = v
	}
	dirs := s.RomsDirs()
	// In user mode the working directory is also scanned for containers; it is
	// never treated as a read-only root, so an output there stays possible.
	if !s.InProject() {
		dirs = append(dirs, s.WorkDir)
	}
	return localsources.Discover(localsources.Request{Dirs: dedupe(dirs), Explicit: explicit})
}

// extractor builds the decode helper: an explicitly configured command wins,
// otherwise a helper discovered on PATH is driven directly, so no command
// template has to be written by hand.
func (s *Service) extractor() extraction.Extractor {
	if len(s.Cfg.ExtractorCommand) > 0 {
		return &extraction.CommandExtractor{Argv: append([]string(nil), s.Cfg.ExtractorCommand...)}
	}
	if tools := extraction.DiscoverTools(); len(tools) > 0 {
		return &extraction.ToolExtractor{Tool: tools[0]}
	}
	return nil
}

// ExtractionAdvice explains what the user must supply when the build inputs
// cannot be resolved: the key files first, then a container helper.
func (s *Service) ExtractionAdvice() string {
	if st, err := s.VerifyExtraction(context.Background()); err == nil && st != nil {
		return ""
	}
	keys := s.KeysPresence()
	if len(keys.Present) == 0 {
		return extraction.KeysNotice(s.KeysDir())
	}
	if s.extractor() == nil {
		return "Switch keys were found, but no container helper is available.\n\n" + extraction.ToolHelp()
	}
	return ""
}

// overlayOverride returns an explicitly configured overlay archive, but only
// when its size matches the accepted revision. Anything else (for example a
// container path) is ignored here so a misconfiguration cannot silently become
// the build input.
func (s *Service) overlayOverride() string {
	p := s.SourceArchive()
	if p == "" {
		return ""
	}
	fi, err := os.Stat(p)
	if err != nil || fi.IsDir() || fi.Size() != gameprofile.OverlaySize {
		return ""
	}
	return p
}

// extractionHook, when set (tests only), adjusts the extraction request before
// it is issued. Production runs leave it nil.
var extractionHook func(*extraction.Request)

// extractionRequest assembles the extraction request for the current settings.
func (s *Service) extractionRequest(ctx context.Context, verifyOnly bool) *extraction.Request {
	req := &extraction.Request{
		CacheDir:     s.CacheDir(),
		SearchRoots:  s.SearchRoots(),
		OverlayPath:  s.overlayOverride(),
		BaseBanksDir: s.resolve(s.Cfg.BaseBanksDir),
		BaseMembers:  s.RequiredBaseMembers(),
		KeysDir:      s.KeysDir(),
		Extractor:    s.extractor(),
		MinBankCount: 1,
		VerifyOnly:   verifyOnly,
		Ctx:          ctx,
		Logf:         s.Logf,
	}
	if !verifyOnly {
		if rep, err := s.DiscoverSources(); err == nil {
			if a := rep.Has("base"); a != nil {
				req.BaseContainer = a.Path
			}
			if a := rep.Has("update"); a != nil {
				req.UpdateContainer = a.Path
			}
		} else {
			return req
		}
	}
	if extractionHook != nil {
		extractionHook(req)
	}
	return req
}

// ResolveExtraction returns verified build inputs, populating the cache when
// needed.
func (s *Service) ResolveExtraction(ctx context.Context) (*extraction.State, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	return extraction.Resolve(s.extractionRequest(ctx, false))
}

// VerifyExtraction checks an existing verified cache without populating it.
func (s *Service) VerifyExtraction(ctx context.Context) (*extraction.State, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	return extraction.Resolve(s.extractionRequest(ctx, true))
}

// LoadTranslations loads the public dataset. A missing or invalid dataset fails
// closed: it is never reconstructed from any private source.
func (s *Service) LoadTranslations() (*translations.Dataset, error) {
	dir := s.TranslationsDir()
	ds, err := translations.Load(dir)
	if err != nil {
		return nil, fmt.Errorf("translation dataset missing/invalid: %s: %w", dir, err)
	}
	return ds, nil
}

// AutoConfigure fills empty settings from what can be discovered and persists
// them. It is the non-interactive part of first-run setup: it never invents
// paths, and it never reads key contents. It returns the list of fields it set.
func (s *Service) AutoConfigure(ctx context.Context) ([]string, error) {
	var set []string
	changed := false
	assign := func(field *string, value, label string) {
		if *field != "" || value == "" {
			return
		}
		*field = value
		set = append(set, label)
		changed = true
	}

	if rep, err := s.DiscoverSources(); err == nil {
		if a := rep.Has("base"); a != nil {
			assign(&s.Cfg.RomsBase, a.Path, "roms_base")
		}
		if a := rep.Has("update"); a != nil {
			assign(&s.Cfg.RomsUpdate, a.Path, "roms_update")
		}
		if a := rep.Has("extra"); a != nil {
			assign(&s.Cfg.RomsExtra, a.Path, "roms_extra")
		}
	}
	if _, err := os.Stat(s.KeysDir()); err == nil {
		assign(&s.Cfg.KeysDir, s.KeysDir(), "keys_dir")
	}
	if _, err := s.LoadTranslations(); err == nil {
		assign(&s.Cfg.TranslationsDir, s.TranslationsDir(), "translations_dir")
	}
	if st, err := s.ResolveExtraction(ctx); err == nil {
		assign(&s.Cfg.CacheDir, st.CacheDir, "cache_dir")
	}

	if err := s.ensureDirs(); err != nil {
		return set, err
	}
	if changed || !s.Cfg.Exists() {
		// Discoveries are durable, but one-off CLI overrides are not: persist
		// only what was discovered and keep the flags ephemeral for this run.
		if err := s.savePersisted(); err != nil {
			return set, err
		}
		set = append(set, "settings written to "+s.CfgPath)
	}
	return set, nil
}

// ensureDirs creates the private state directories. It is only called from
// setup/auto-configuration, never from a read-only status query.
func (s *Service) ensureDirs() error {
	for _, d := range []string{s.StateDir(), s.CacheDir(), s.ReportsDir(), s.DerivedDir()} {
		if d == "" {
			continue
		}
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	return nil
}
