package texpack

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------------------
// Title-footer style
// ---------------------------------------------------------------------------
//
// The footer label is drawn from two live, user-editable inputs:
//
//   - the display face (assets/fonts/<font_file>), read from disk on every
//     build so replacing the .otf changes the next build without recompiling;
//   - the style (assets/fonts/title_footer.toml), read from disk on every build.
//
// This file holds the style value, its documented defaults and the strict,
// schema-limited loader for the small configuration file. The loader is
// deliberately NOT a general-purpose TOML parser: it understands only the
// documented keys, and it fails closed on any unknown key, wrong type,
// duplicate key, section header or out-of-range value. A loose parser would
// carry a typo silently into a rendered title screen.

// TitleFooterRendererVersion identifies the renderer implementation that
// produced a footer. It is part of the cache-identity hash so a renderer change
// is provable from a build report even when the visible label is unchanged.
const TitleFooterRendererVersion = 2

// BundledFontName is the default display face file name inside the font
// directory. It is the file shipped with the project.
const BundledFontName = "unburger.otf"

// ShippedFontSHA256 is the digest of the display face shipped in
// assets/fonts/unburger.otf. It is a pinned release-identity value, not a
// second source of the font bytes: the font itself is always read from disk.
// A build with this face and the shipped style is the shipped composition.
const ShippedFontSHA256 = "cf1e64aadfeb7d198a28765cd52fa42aaef9c7938a9791745535008399c0fae1"

// RuntimeConfirmedFontSHA256 is the digest of the display face used by the
// runtime-confirmed reference build of 2026-10-19. Those bytes are no longer
// shipped; the value is kept as the historical reference the live inputs are
// compared against. Matching it (with the built-in default style, i.e.
// letter_spacing 0) is the only composition reported as runtime-confirmed.
const RuntimeConfirmedFontSHA256 = "3c32dc0fd1fd5b18264727363bdd0b4589a6d1b9ba16a779d49ff2b5bf1b4c72"

// ShippedLetterSpacing is the letter_spacing in the shipped title_footer.toml.
// It differs from the built-in default (0) used by the 2026-10-19 reference
// composition, so the shipped composition is not the runtime-confirmed one.
const ShippedLetterSpacing = 0.2

// TitleFooterStyle is the parsed style for the in-game footer label. Zero
// values are never used implicitly: StartTitleFooterStyle and the loader
// document and enforce every field's valid range.
type TitleFooterStyle struct {
	// FontFile is the display face file name, resolved inside the same
	// directory as the style file. Default "unburger.otf".
	FontFile string
	// FontSize is the em size in pixels. 0 means "auto": the largest em that
	// fits the footer rectangle. Range 0..512.
	FontSize float64
	// LetterSpacing is extra advance between glyphs, in em units. Range
	// -0.5..2.0. Default 0.
	LetterSpacing float64
	// TextColor is the glyph ink colour (RGB). Default #ffffff.
	TextColor [3]uint8
	// TextOpacity is the glyph ink opacity. Range 0..1. Default 1.
	TextOpacity float64
	// OutlineWidth is the outline dilation radius in pixels. Range 0..8.
	// Default 1.
	OutlineWidth int
	// OutlineColor is the outline colour (RGB). Default #000000.
	OutlineColor [3]uint8
	// OutlineOpacity is the outline opacity. Range 0..1. Default 0.5, i.e. an
	// alpha byte of 128.
	OutlineOpacity float64
	// OffsetX / OffsetY shift the centred label, in pixels. Range -512..512.
	// Default 0,0. A shift that pushes the label outside the footer rectangle
	// fails the build rather than clipping.
	OffsetX, OffsetY int
	// BackgroundColor is the backing colour (RGB) used only when
	// BackgroundOpacity is greater than zero. Default #000000.
	BackgroundColor [3]uint8
	// BackgroundOpacity is the backing opacity. Range 0..1. Default 0, i.e. a
	// fully transparent backing.
	BackgroundOpacity float64
	// ShadowEnabled is reserved. The renderer has no shadow pass, so the only
	// accepted value is false; true is refused rather than silently ignored.
	ShadowEnabled bool
}

// DefaultTitleFooterStyle returns the built-in style. It is the style of the
// runtime-confirmed reference composition: a transparent canvas, opaque white
// ink (alpha = coverage*255), a black outline of dilation radius 1 at alpha 128
// (opacity 0.5, alpha = coverage*128), centred in the footer rectangle at the
// largest em that fits, with no letter spacing, no offset and no background.
// The shipped title_footer.toml uses the same values except letter_spacing 0.2,
// and the shipped face differs from the confirmed one; the shipped composition
// is therefore reported as runtime-unverified until it is tested.
func DefaultTitleFooterStyle() TitleFooterStyle {
	return TitleFooterStyle{
		FontFile:          BundledFontName,
		FontSize:          0,
		LetterSpacing:     0,
		TextColor:         [3]uint8{255, 255, 255},
		TextOpacity:       1,
		OutlineWidth:      1,
		OutlineColor:      [3]uint8{0, 0, 0},
		OutlineOpacity:    0.5,
		OffsetX:           0,
		OffsetY:           0,
		BackgroundColor:   [3]uint8{0, 0, 0},
		BackgroundOpacity: 0,
		ShadowEnabled:     false,
	}
}

// ShippedTitleFooterStyle returns the style the repository ships: the built-in
// default with the shipped title_footer.toml letter spacing. It describes the
// release composition, not a runtime-confirmed one.
func ShippedTitleFooterStyle() TitleFooterStyle {
	s := DefaultTitleFooterStyle()
	s.LetterSpacing = ShippedLetterSpacing
	return s
}

// IsDefault reports whether the style equals the built-in default after
// normalisation. That is the style of the runtime-confirmed reference
// composition, not the shipped title_footer.toml (which sets letter spacing).
func (s TitleFooterStyle) IsDefault() bool {
	return s.Canonical() == DefaultTitleFooterStyle().Canonical()
}

// IsShipped reports whether the style equals the shipped configuration after
// normalisation. A shipped build is a release composition, not a runtime claim.
func (s TitleFooterStyle) IsShipped() bool {
	return s.Canonical() == ShippedTitleFooterStyle().Canonical()
}

// Canonical renders the normalised style as deterministic text. It is the input
// to the cache-identity hash and is stable across platforms.
func (s TitleFooterStyle) Canonical() string {
	var b strings.Builder
	fmt.Fprintf(&b, "font_file=%s\n", s.FontFile)
	fmt.Fprintf(&b, "font_size=%.6f\n", s.FontSize)
	fmt.Fprintf(&b, "letter_spacing=%.6f\n", s.LetterSpacing)
	fmt.Fprintf(&b, "text_color=%s\n", colorString(s.TextColor))
	fmt.Fprintf(&b, "text_opacity=%.6f\n", s.TextOpacity)
	fmt.Fprintf(&b, "outline_width=%d\n", s.OutlineWidth)
	fmt.Fprintf(&b, "outline_color=%s\n", colorString(s.OutlineColor))
	fmt.Fprintf(&b, "outline_opacity=%.6f\n", s.OutlineOpacity)
	fmt.Fprintf(&b, "offset_x=%d\n", s.OffsetX)
	fmt.Fprintf(&b, "offset_y=%d\n", s.OffsetY)
	fmt.Fprintf(&b, "background_color=%s\n", colorString(s.BackgroundColor))
	fmt.Fprintf(&b, "background_opacity=%.6f\n", s.BackgroundOpacity)
	fmt.Fprintf(&b, "shadow_enabled=%t\n", s.ShadowEnabled)
	return b.String()
}

// colorString renders an RGB triple as a lower-case #rrggbb literal.
func colorString(c [3]uint8) string { return fmt.Sprintf("#%02x%02x%02x", c[0], c[1], c[2]) }

// styleKey is one accepted configuration key and how it is parsed.
type styleKey struct {
	name string
	set  func(*TitleFooterStyle, string) error
}

// styleKeys is the complete, closed set of accepted keys. An unknown key is a
// hard error, never a warning.
func styleKeys() []styleKey {
	return []styleKey{
		{"font_file", func(s *TitleFooterStyle, v string) error {
			if strings.TrimSpace(v) == "" {
				return errors.New("font_file must not be empty")
			}
			s.FontFile = v
			return nil
		}},
		{"font_size", func(s *TitleFooterStyle, v string) error {
			return setFloat(v, 0, 512, "font_size", &s.FontSize)
		}},
		{"letter_spacing", func(s *TitleFooterStyle, v string) error {
			return setFloat(v, -0.5, 2, "letter_spacing", &s.LetterSpacing)
		}},
		{"text_color", func(s *TitleFooterStyle, v string) error {
			return setColor(v, "text_color", &s.TextColor)
		}},
		{"text_opacity", func(s *TitleFooterStyle, v string) error {
			return setFloat(v, 0, 1, "text_opacity", &s.TextOpacity)
		}},
		{"outline_width", func(s *TitleFooterStyle, v string) error {
			return setInt(v, 0, 8, "outline_width", &s.OutlineWidth)
		}},
		{"outline_color", func(s *TitleFooterStyle, v string) error {
			return setColor(v, "outline_color", &s.OutlineColor)
		}},
		{"outline_opacity", func(s *TitleFooterStyle, v string) error {
			return setFloat(v, 0, 1, "outline_opacity", &s.OutlineOpacity)
		}},
		{"offset_x", func(s *TitleFooterStyle, v string) error {
			return setInt(v, -512, 512, "offset_x", &s.OffsetX)
		}},
		{"offset_y", func(s *TitleFooterStyle, v string) error {
			return setInt(v, -512, 512, "offset_y", &s.OffsetY)
		}},
		{"background_color", func(s *TitleFooterStyle, v string) error {
			return setColor(v, "background_color", &s.BackgroundColor)
		}},
		{"background_opacity", func(s *TitleFooterStyle, v string) error {
			return setFloat(v, 0, 1, "background_opacity", &s.BackgroundOpacity)
		}},
		{"shadow_enabled", func(s *TitleFooterStyle, v string) error {
			b, err := strconv.ParseBool(strings.TrimSpace(v))
			if err != nil {
				return fmt.Errorf("shadow_enabled must be true or false, got %q", v)
			}
			if b {
				return errors.New("shadow_enabled = true is not supported: the footer renderer has no shadow pass")
			}
			s.ShadowEnabled = false
			return nil
		}},
	}
}

// ParseTitleFooterStyle parses the small title_footer.toml configuration. It is
// strict and schema-limited: it accepts only the documented keys, one per line,
// with an optional trailing `#` comment. Blank lines and whole-line comments are
// ignored. Any unknown key, duplicate key, section header, malformed line,
// wrong value type or out-of-range value is an error.
func ParseTitleFooterStyle(data []byte) (TitleFooterStyle, error) {
	s := DefaultTitleFooterStyle()
	byName := map[string]styleKey{}
	for _, k := range styleKeys() {
		byName[k.name] = k
	}
	seen := map[string]bool{}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	for i, raw := range lines {
		line := strings.TrimSpace(stripComment(raw))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") {
			return s, fmt.Errorf("title_footer.toml line %d: configuration tables are not supported", i+1)
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			return s, fmt.Errorf("title_footer.toml line %d: expected `key = value`, got %q", i+1, line)
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		if key == "" {
			return s, fmt.Errorf("title_footer.toml line %d: empty key", i+1)
		}
		k, ok := byName[key]
		if !ok {
			return s, fmt.Errorf("title_footer.toml line %d: unknown key %q", i+1, key)
		}
		if seen[key] {
			return s, fmt.Errorf("title_footer.toml line %d: duplicate key %q", i+1, key)
		}
		seen[key] = true
		if err := k.set(&s, unquote(val)); err != nil {
			return s, fmt.Errorf("title_footer.toml line %d: %w", i+1, err)
		}
	}
	return s, nil
}

// stripComment removes a trailing `#` comment that is not inside a quoted
// string.
func stripComment(line string) string {
	inStr := false
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '"':
			inStr = !inStr
		case '#':
			if !inStr {
				return line[:i]
			}
		}
	}
	return line
}

// unquote removes one layer of surrounding double quotes, if present, and
// unescapes \" and \\.
func unquote(v string) string {
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		inner := v[1 : len(v)-1]
		inner = strings.ReplaceAll(inner, `\"`, `"`)
		inner = strings.ReplaceAll(inner, `\\`, `\`)
		return inner
	}
	return v
}

// looksQuoted reports whether a raw (comment-stripped) value was written as a
// TOML string literal.
func looksQuoted(v string) bool {
	v = strings.TrimSpace(v)
	return len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"'
}

func setFloat(v string, lo, hi float64, name string, dst *float64) error {
	if looksQuoted(v) {
		return fmt.Errorf("%s must be a number, got a quoted string %q", name, v)
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil {
		return fmt.Errorf("%s must be a number, got %q", name, v)
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return fmt.Errorf("%s must be a finite number, got %q", name, v)
	}
	if f < lo || f > hi {
		return fmt.Errorf("%s is %g, outside the allowed range %g..%g", name, f, lo, hi)
	}
	*dst = f
	return nil
}

func setInt(v string, lo, hi int, name string, dst *int) error {
	if looksQuoted(v) {
		return fmt.Errorf("%s must be an integer, got a quoted string %q", name, v)
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		// Accept an exact integral float spelling such as 1.0.
		if f, ferr := strconv.ParseFloat(strings.TrimSpace(v), 64); ferr == nil && f == math.Trunc(f) {
			n = int(f)
		} else {
			return fmt.Errorf("%s must be an integer, got %q", name, v)
		}
	}
	if n < lo || n > hi {
		return fmt.Errorf("%s is %d, outside the allowed range %d..%d", name, n, lo, hi)
	}
	*dst = n
	return nil
}

func setColor(v string, name string, dst *[3]uint8) error {
	s := strings.TrimSpace(v)
	if len(s) != 7 || s[0] != '#' {
		return fmt.Errorf("%s must be a #rrggbb colour, got %q", name, v)
	}
	var out [3]uint8
	for i := 0; i < 3; i++ {
		b, err := strconv.ParseUint(s[1+2*i:3+2*i], 16, 8)
		if err != nil {
			return fmt.Errorf("%s must be a #rrggbb colour, got %q", name, v)
		}
		out[i] = uint8(b)
	}
	*dst = out
	return nil
}
