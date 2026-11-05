package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"13s-patcher/internal/texpack"
)

// shippedAsset copies one of the module's shipped title assets into dir.
func copyShipped(t *testing.T, name, dir string) string {
	t.Helper()
	src := filepath.Join("..", "..", "assets", "fonts", name)
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("the shipped %s must be readable: %v", name, err)
	}
	dst := filepath.Join(dir, name)
	if err := os.WriteFile(dst, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return dst
}

// titleBuildInfo composes the title member through buildResources and returns
// its logical digest, the face digest and the cache identity.
func titleBuildInfo(t *testing.T, svc *Service) (logical, fontSHA, identity string) {
	t.Helper()
	_, status, err := svc.buildResources()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range status {
		if s.Member == texpack.TitleMember {
			if !s.Present {
				t.Fatalf("the title member was not composed: %+v", s)
			}
			return s.LogicalSHA256, s.FontSHA256, s.CacheIdentity
		}
	}
	t.Fatal("the title member status is missing")
	return "", "", ""
}

// TestTitleFooterLiveAssetsRebuild proves the live-input contract end to end:
// the face and the style are read from disk on every build, editing either
// changes the very next build with no recompile or restart, and unchanged
// inputs stay byte-identical.
func TestTitleFooterLiveAssetsRebuild(t *testing.T) {
	svc, _ := integratedService(t)
	titleVersionRoot(t, svc, "1.0.0")
	dir := t.TempDir()
	stylePath := copyShipped(t, "title_footer.toml", dir)
	copyShipped(t, "unburger.otf", dir)
	svc.TitleAssetsDir = dir

	sha1, font1, id1 := titleBuildInfo(t, svc)
	sha1b, _, id1b := titleBuildInfo(t, svc)
	if sha1 != sha1b || id1 != id1b {
		t.Fatalf("unchanged inputs must be byte-identical: %s/%s vs %s/%s", sha1, id1, sha1b, id1b)
	}

	// Edit the style file: the next build must change without a recompile.
	raw, err := os.ReadFile(stylePath)
	if err != nil {
		t.Fatal(err)
	}
	start := bytes.Index(raw, []byte("letter_spacing = "))
	if start < 0 {
		t.Fatal("the shipped style must contain the letter_spacing line this test edits")
	}
	end := bytes.IndexByte(raw[start:], '\n')
	if end < 0 {
		end = len(raw) - start
	}
	line := append([]byte(nil), raw[start:start+end]...)
	edited := bytes.Replace(raw, line, []byte("letter_spacing = 0.30"), 1)
	if bytes.Equal(edited, raw) {
		t.Fatalf("the shipped style's letter_spacing line (%q) was not replaced", line)
	}
	if err := os.WriteFile(stylePath, edited, 0o644); err != nil {
		t.Fatal(err)
	}
	sha2, _, id2 := titleBuildInfo(t, svc)
	if sha2 == sha1 {
		t.Fatal("editing title_footer.toml must change the next build")
	}
	if id2 == id1 {
		t.Fatal("editing title_footer.toml must change the cache identity")
	}

	// Replace the face file: the next build reads the new bytes (the digest is
	// re-derived from disk), again without a recompile.
	fontPath := filepath.Join(dir, "unburger.otf")
	fb, err := os.ReadFile(fontPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fontPath, append(append([]byte(nil), fb...), 0, 0), 0o644); err != nil {
		t.Fatal(err)
	}
	_, font3, id3 := titleBuildInfo(t, svc)
	if font3 == font1 {
		t.Fatal("replacing the .otf must change the face digest read at build time")
	}
	if id3 == id2 {
		t.Fatal("replacing the .otf must change the cache identity")
	}
}

// TestTitleFooterMissingFontFailsClearly proves a missing face fails with the
// expected path named, never a silent embedded fallback.
func TestTitleFooterMissingFontFailsClearly(t *testing.T) {
	svc, _ := integratedService(t)
	titleVersionRoot(t, svc, "1.0.0")
	svc.TitleAssetsDir = t.TempDir() // empty: no face, no style
	_, _, err := svc.buildResources()
	if err == nil {
		t.Fatal("a missing display face must fail the build")
	}
	for _, want := range []string{"title footer font not found", "unburger.otf", "patcher/assets/fonts"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the error must name %q: %v", want, err)
		}
	}
}

// TestTitleFooterInvalidStyleFailsClearly proves an unknown key, a wrong type
// and an out-of-range value all fail with a clear message.
func TestTitleFooterInvalidStyleFailsClearly(t *testing.T) {
	cases := []struct {
		name string
		toml string
		want string
	}{
		{"unknown key", "font_colour = \"#ffffff\"\n", "unknown key"},
		{"wrong type", "font_size = \"big\"\n", "must be a number"},
		{"out of range", "text_opacity = 3.0\n", "outside the allowed range"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := integratedService(t)
			titleVersionRoot(t, svc, "1.0.0")
			dir := t.TempDir()
			copyShipped(t, "unburger.otf", dir)
			if err := os.WriteFile(filepath.Join(dir, "title_footer.toml"), []byte(tc.toml), 0o644); err != nil {
				t.Fatal(err)
			}
			svc.TitleAssetsDir = dir
			_, _, err := svc.buildResources()
			if err == nil {
				t.Fatalf("style %q must fail the build", tc.toml)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("the error must mention %q: %v", tc.want, err)
			}
		})
	}
}

// TestTitleFooterOverflowFailsNotClips proves a style whose label cannot fit the
// footer rectangle fails the build with a fit error rather than clipping.
func TestTitleFooterOverflowFailsNotClips(t *testing.T) {
	svc, _ := integratedService(t)
	titleVersionRoot(t, svc, "1.0.0")
	dir := t.TempDir()
	copyShipped(t, "unburger.otf", dir)
	if err := os.WriteFile(filepath.Join(dir, "title_footer.toml"), []byte("font_size = 400\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	svc.TitleAssetsDir = dir
	_, _, err := svc.buildResources()
	if err == nil {
		t.Fatal("an oversized label must fail the build")
	}
	if !strings.Contains(err.Error(), "does not fit") {
		t.Fatalf("the overflow must fail with a fit error: %v", err)
	}
}
