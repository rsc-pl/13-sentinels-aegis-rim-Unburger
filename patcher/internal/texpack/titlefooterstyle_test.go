package texpack

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"13s-patcher/internal/fixture"
)

// shippedConfigBytes reads the live style file the production path reads.
func shippedConfigBytes(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "assets", "fonts", "title_footer.toml"))
	if err != nil {
		t.Fatalf("the shipped title_footer.toml must be readable: %v", err)
	}
	return b
}

// TestShippedConfigMatchesShippedStyle proves the shipped configuration file
// parses to exactly the shipped style (built-in defaults plus the shipped
// letter spacing), and that it is NOT the runtime-confirmed reference style.
func TestShippedConfigMatchesShippedStyle(t *testing.T) {
	st, err := ParseTitleFooterStyle(shippedConfigBytes(t))
	if err != nil {
		t.Fatalf("the shipped title_footer.toml must parse: %v", err)
	}
	if !st.IsShipped() {
		t.Fatalf("the shipped config must reproduce the shipped style:\n%s\nwant\n%s",
			st.Canonical(), ShippedTitleFooterStyle().Canonical())
	}
	if st.IsDefault() {
		t.Fatal("the shipped config must not be reported as the runtime-confirmed reference style")
	}
	if st.LetterSpacing != ShippedLetterSpacing {
		t.Fatalf("the shipped letter spacing is %v, want the pinned %v", st.LetterSpacing, ShippedLetterSpacing)
	}
}

// TestParseTitleFooterStyleRejectsInvalid proves the loader fails closed on
// unknown keys, wrong types, out-of-range values and unsupported tables.
func TestParseTitleFooterStyleRejectsInvalid(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"unknown key", "font_colour = \"#ffffff\"\n", "unknown key"},
		{"duplicate key", "font_size = 0\nfont_size = 4\n", "duplicate key"},
		{"wrong type string for number", "font_size = \"big\"\n", "must be a number"},
		{"negative size", "font_size = -3\n", "outside the allowed range"},
		{"opacity over one", "text_opacity = 1.5\n", "outside the allowed range"},
		{"negative opacity", "outline_opacity = -0.1\n", "outside the allowed range"},
		{"bad colour", "text_color = \"white\"\n", "#rrggbb"},
		{"short colour", "text_color = \"#fff\"\n", "#rrggbb"},
		{"bad outline width", "outline_width = 99\n", "outside the allowed range"},
		{"offset out of range", "offset_x = 9000\n", "outside the allowed range"},
		{"shadow unsupported", "shadow_enabled = true\n", "not supported"},
		{"section header", "[title]\nfont_size = 4\n", "tables are not supported"},
		{"missing equals", "font_size 4\n", "expected `key = value`"},
		{"nan", "font_size = NaN\n", "finite"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseTitleFooterStyle([]byte(tc.input))
			if err == nil {
				t.Fatalf("input %q must be refused", tc.input)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error for %q must mention %q: %v", tc.input, tc.want, err)
			}
		})
	}
}

// TestParseTitleFooterStyleAcceptsValue proves a full, valid override parses and
// is not reported as the default.
func TestParseTitleFooterStyleAcceptsValue(t *testing.T) {
	in := `
# a full override
font_file = "unburger.otf"
font_size = 42.5
letter_spacing = 0.25
text_color = "#ff00aa"
text_opacity = 0.8
outline_width = 2
outline_color = "#001122"
outline_opacity = 0.3
offset_x = -4
offset_y = 6
background_color = "#112233"
background_opacity = 0.5
shadow_enabled = false
`
	st, err := ParseTitleFooterStyle([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if st.IsDefault() {
		t.Fatal("a full override must not equal the default")
	}
	if st.FontSize != 42.5 || st.TextColor != [3]uint8{0xff, 0x00, 0xaa} || st.OffsetX != -4 || st.BackgroundOpacity != 0.5 {
		t.Fatalf("parsed style is wrong: %+v", st)
	}
}

// TestComposeTitleFooterLiveFont proves the composition is a function of the
// font bytes passed in: two valid faces with different advances render
// differently, and both still verify as footer-only edits. It is the static
// proof behind "replacing the .otf changes the next build".
func TestComposeTitleFooterLiveFont(t *testing.T) {
	base := fixture.SynthTitleFTX()
	label := "Unburger Mod v0.1.0"
	orig := testFontBytes(t)
	tweaked := tweakFontAdvances(t, orig)

	a, err := ComposeTitleFooter(base, label, orig, DefaultTitleFooterStyle())
	if err != nil {
		t.Fatal(err)
	}
	b, err := ComposeTitleFooter(base, label, tweaked, DefaultTitleFooterStyle())
	if err != nil {
		t.Fatalf("the tweaked face must still render: %v", err)
	}
	if string(a) == string(b) {
		t.Fatal("a different display face must change the composed footer")
	}
	if err := VerifyTitleFooter(base, b); err != nil {
		t.Fatalf("the tweaked-face composition must still be a footer-only edit: %v", err)
	}
}

// tweakFontAdvances widens every horizontal advance in a valid face, so the
// label's total advance (and therefore the auto-fit size and the rendered
// pixels) changes while the face stays parseable.
func tweakFontAdvances(t *testing.T, raw []byte) []byte {
	t.Helper()
	out := append([]byte(nil), raw...)
	f, err := parseFont(out)
	if err != nil {
		t.Fatalf("the shipped face must parse: %v", err)
	}
	if f.hmtxAt == 0 || f.numHMetrics <= 0 {
		t.Fatal("the shipped face has no hmtx metrics to tweak")
	}
	for i := 0; i < f.numHMetrics; i++ {
		o := f.hmtxAt + 4*i
		if o+2 > len(out) {
			t.Fatal("hmtx entry is out of range")
		}
		adv := binary.BigEndian.Uint16(out[o : o+2])
		if adv < 65000 {
			binary.BigEndian.PutUint16(out[o:o+2], adv+50)
		}
	}
	return out
}

// TestComposeTitleFooterNoFontFails proves a missing face is refused rather
// than silently substituted.
func TestComposeTitleFooterNoFontFails(t *testing.T) {
	if _, err := ComposeTitleFooter(fixture.SynthTitleFTX(), "Unburger Mod v0.1.0", nil, DefaultTitleFooterStyle()); err == nil {
		t.Fatal("a missing display face must be refused")
	}
	if _, err := ComposeTitleFooter(fixture.SynthTitleFTX(), "Unburger Mod v0.1.0", []byte("not a font"), DefaultTitleFooterStyle()); err == nil {
		t.Fatal("an invalid display face must be refused")
	}
}

// TestStyleColorsAndOpacity proves the style drives the ink colour, the outline
// colour/opacity and the (optional) backing, and that the default backing stays
// fully transparent.
func TestStyleColorsAndOpacity(t *testing.T) {
	face := parseTestFace(t)
	rect := titleFooterRect()

	// Ink only: red, no outline, transparent backing.
	inkStyle := DefaultTitleFooterStyle()
	inkStyle.TextColor = [3]uint8{255, 0, 0}
	inkStyle.OutlineOpacity = 0
	canvas, err := composeTitleCanvas("Unburger Mod v0.1.0", face, inkStyle)
	if err != nil {
		t.Fatal(err)
	}
	redInk := 0
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			o := canvas.PixOffset(x, y)
			if canvas.Pix[o] == 255 && canvas.Pix[o+1] == 0 && canvas.Pix[o+2] == 0 && canvas.Pix[o+3] == 255 {
				redInk++
			}
		}
	}
	if redInk == 0 {
		t.Fatal("the red ink colour was not applied")
	}

	// Outline only: invisible ink over a green outline.
	haloStyle := DefaultTitleFooterStyle()
	haloStyle.TextOpacity = 0
	haloStyle.OutlineColor = [3]uint8{0, 255, 0}
	haloStyle.OutlineOpacity = 1
	canvas2, err := composeTitleCanvas("Unburger Mod v0.1.0", face, haloStyle)
	if err != nil {
		t.Fatal(err)
	}
	greenHalo := 0
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			o := canvas2.PixOffset(x, y)
			if canvas2.Pix[o] == 0 && canvas2.Pix[o+1] == 255 && canvas2.Pix[o+2] == 0 && canvas2.Pix[o+3] == 255 {
				greenHalo++
			}
		}
	}
	if greenHalo == 0 {
		t.Fatal("the green outline colour was not applied")
	}

	// Opaque backing: every pixel of the footer rectangle is now the backing
	// colour until ink/outline are drawn over it.
	bgStyle := DefaultTitleFooterStyle()
	bgStyle.BackgroundColor = [3]uint8{0x11, 0x22, 0x33}
	bgStyle.BackgroundOpacity = 1
	canvas3, err := composeTitleCanvas("Mod", face, bgStyle)
	if err != nil {
		t.Fatal(err)
	}
	// The footer rectangle's far corner carries no ink.
	o := canvas3.PixOffset(rect.Max.X-1, rect.Max.Y-1)
	if canvas3.Pix[o] != 0x11 || canvas3.Pix[o+1] != 0x22 || canvas3.Pix[o+2] != 0x33 || canvas3.Pix[o+3] != 255 {
		t.Fatalf("the backing was not applied: %v", canvas3.Pix[o:o+4])
	}

	// The default backing stays fully transparent outside the ink/outline.
	def, err := composeTitleCanvas("Mod", face, DefaultTitleFooterStyle())
	if err != nil {
		t.Fatal(err)
	}
	od := def.PixOffset(rect.Max.X-1, rect.Max.Y-1)
	if def.Pix[od+3] != 0 {
		t.Fatalf("the default backing must be transparent, got alpha %d", def.Pix[od+3])
	}
}

// TestStyleFontSizeAndOffsetOverflowFails proves an oversized or shifted label
// fails with a fit error rather than being clipped.
func TestStyleFontSizeAndOffsetOverflowFails(t *testing.T) {
	face := parseTestFace(t)
	big := DefaultTitleFooterStyle()
	big.FontSize = 400
	if _, err := composeTitleCanvas("Unburger Mod v0.1.0", face, big); err == nil {
		t.Fatal("an oversized font_size must fail rather than clip")
	} else if !strings.Contains(err.Error(), "does not fit") {
		t.Fatalf("the overflow error must name the fit limit: %v", err)
	}
	shifted := DefaultTitleFooterStyle()
	shifted.OffsetX = 500
	if _, err := composeTitleCanvas("Unburger Mod v0.1.0", face, shifted); err == nil {
		t.Fatal("an out-of-rectangle offset must fail rather than clip")
	} else if !strings.Contains(err.Error(), "does not fit") {
		t.Fatalf("the overflow error must name the fit limit: %v", err)
	}
}
