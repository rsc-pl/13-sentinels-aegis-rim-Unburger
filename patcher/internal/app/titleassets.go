package app

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"13s-patcher/internal/hashutil"
	"13s-patcher/internal/modversion"
	"13s-patcher/internal/texpack"
)

// titleFooterConfigName is the live style file, next to the display face.
const titleFooterConfigName = "title_footer.toml"

// TitleAssets carries the live title-footer inputs for one build: the display
// face read from disk now, and the parsed style. Nothing here is embedded or
// cached between builds, so replacing the .otf or editing title_footer.toml
// changes the very next build without recompiling the tool or restarting the
// dashboard.
type TitleAssets struct {
	// FontBytes are the display face's bytes, read from disk this build.
	FontBytes []byte
	// FontPath is the absolute path the bytes were read from.
	FontPath string
	// FontSHA256 is the digest of the bytes actually read.
	FontSHA256 string
	// Style is the parsed footer style.
	Style texpack.TitleFooterStyle
	// ConfigPath is the style file read ("" when the built-in defaults apply).
	ConfigPath string
	// ConfigFound reports whether a style file was present.
	ConfigFound bool
}

// CacheIdentity is the provable identity of a rendered footer: the renderer
// version, the digest of the font bytes actually used, the normalised style and
// the label. Two builds with the same identity must produce byte-identical
// member output.
func (a *TitleAssets) CacheIdentity(label string) string {
	h := sha256.New()
	fmt.Fprintf(h, "renderer=%d\n", texpack.TitleFooterRendererVersion)
	fmt.Fprintf(h, "font_sha256=%s\n", a.FontSHA256)
	fmt.Fprintf(h, "style:\n%s", a.Style.Canonical())
	fmt.Fprintf(h, "label=%s\n", label)
	return hex.EncodeToString(h.Sum(nil))
}

// StyleSource names the effective style for reports: the config path when a file
// was read, otherwise the built-in defaults.
func (a *TitleAssets) StyleSource() string {
	if a.ConfigFound {
		return a.ConfigPath
	}
	return "built-in defaults (" + titleFooterConfigName + " absent)"
}

// Composition names which known composition these live inputs describe:
// CompositionReference is the runtime-confirmed reference (2026-10-19),
// CompositionShipped is the repository's shipped face + title_footer.toml, and
// CompositionModified is any other face/style. Only CompositionReference is
// runtime-verified; the other two have no game run behind them.
const (
	CompositionReference = "reference"
	CompositionShipped   = "shipped"
	CompositionModified  = "modified"
)

// RuntimeConfirmed reports whether this build is the historical,
// runtime-confirmed reference composition: the built-in style (letter spacing
// 0) and the face recorded on 2026-10-19. The shipped face and the shipped
// title_footer.toml are different inputs, so a normal build is not confirmed.
func (a *TitleAssets) RuntimeConfirmed() bool {
	return a.Style.IsDefault() && a.FontSHA256 == texpack.RuntimeConfirmedFontSHA256
}

// ShippedIdentity reports whether this build uses exactly the shipped display
// face and the shipped title_footer.toml style. It is a release-identity check,
// not a runtime claim: the shipped composition has not been run in the game.
func (a *TitleAssets) ShippedIdentity() bool {
	return a.Style.IsShipped() && a.FontSHA256 == texpack.ShippedFontSHA256
}

// Composition classifies the live inputs for reports and UI. A build that is
// neither the confirmed reference nor the shipped composition was modified by
// the user and is runtime-unverified too.
func (a *TitleAssets) Composition() string {
	switch {
	case a.RuntimeConfirmed():
		return CompositionReference
	case a.ShippedIdentity():
		return CompositionShipped
	default:
		return CompositionModified
	}
}

// titleAssetDirs lists the directories a display face and its style file may
// live in, most authoritative first. It resolves relative to the actual project
// root (found the same way version.txt is), never to the shell working
// directory, and falls back to an upward walk from the executable so a built
// binary run from anywhere still finds the project assets.
func (s *Service) titleAssetDirs() []string {
	var out []string
	if s.TitleAssetsDir != "" {
		out = append(out, s.TitleAssetsDir)
	}
	if root := modversion.FindRoot(s.WorkDir, s.Loc.Root); root != "" {
		out = append(out, filepath.Join(root, "patcher", "assets", "fonts"))
		out = append(out, filepath.Join(root, "assets", "fonts"))
	}
	if exe, err := os.Executable(); err == nil {
		out = appendAssetWalkUps(out, filepath.Dir(exe), "fonts")
	}
	out = appendAssetWalkUps(out, s.WorkDir, "fonts")
	return dedupe(out)
}

// appendAssetWalkUps adds <ancestor>/patcher/assets/<sub> and
// <ancestor>/assets/<sub> for dir and every ancestor.
func appendAssetWalkUps(out []string, dir, sub string) []string {
	if strings.TrimSpace(dir) == "" {
		return out
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return out
	}
	for {
		out = append(out, filepath.Join(abs, "patcher", "assets", sub))
		out = append(out, filepath.Join(abs, "assets", sub))
		parent := filepath.Dir(abs)
		if parent == abs {
			return out
		}
		abs = parent
	}
}

// resolveTitleAssetDir returns the first directory that actually holds the
// display face or a style file. When none does it returns the primary expected
// directory (the project's patcher/assets/fonts) so the error names a concrete,
// actionable path.
func (s *Service) resolveTitleAssetDir() string {
	dirs := s.titleAssetDirs()
	for _, d := range dirs {
		if fileIsFile(filepath.Join(d, texpack.BundledFontName)) || fileIsFile(filepath.Join(d, titleFooterConfigName)) {
			return d
		}
	}
	// A style file may name a non-default face; look for any *.otf/*.ttf too
	// before giving up on existence checks.
	for _, d := range dirs {
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			if matches, _ := filepath.Glob(filepath.Join(d, "*.otf")); len(matches) > 0 {
				return d
			}
		}
	}
	if len(dirs) > 0 {
		return dirs[0]
	}
	return filepath.Join(s.WorkDir, "patcher", "assets", "fonts")
}

// LoadTitleAssets reads the display face and the style fresh from disk. It is
// called on every title-branding build: nothing is cached across calls.
//
// A missing or unreadable display face fails with the expected path named; an
// invalid style file fails with its line and problem. Neither ever falls back to
// an embedded face or an ignored key.
func (s *Service) LoadTitleAssets() (*TitleAssets, error) {
	dir := s.resolveTitleAssetDir()

	style := texpack.DefaultTitleFooterStyle()
	configPath := filepath.Join(dir, titleFooterConfigName)
	configFound := false
	raw, err := os.ReadFile(configPath)
	switch {
	case err == nil:
		st, perr := texpack.ParseTitleFooterStyle(raw)
		if perr != nil {
			return nil, perr
		}
		style = st
		configFound = true
	case os.IsNotExist(err):
		// The built-in defaults apply; this is the shipped behaviour.
	default:
		return nil, fmt.Errorf("title footer style: cannot read %s: %w", configPath, err)
	}

	fontPath := filepath.Join(dir, style.FontFile)
	fontBytes, ferr := os.ReadFile(fontPath)
	if ferr != nil {
		if os.IsNotExist(ferr) {
			return nil, fmt.Errorf(
				"title footer font not found: expected %s (patcher/assets/fonts/%s relative to the project root); place the display face there or set font_file in %s",
				fontPath, style.FontFile, titleFooterConfigName)
		}
		return nil, fmt.Errorf("title footer font: cannot read %s: %w", fontPath, ferr)
	}
	return &TitleAssets{
		FontBytes:   fontBytes,
		FontPath:    fontPath,
		FontSHA256:  hashutil.Bytes(fontBytes),
		Style:       style,
		ConfigPath:  configPath,
		ConfigFound: configFound,
	}, nil
}
