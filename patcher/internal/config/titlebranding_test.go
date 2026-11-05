package config

import "testing"

// TestTitleBrandingDefaultsOn pins the new default: the footer is part of a
// normal build, so a settings file that omits title_branding includes it. Only
// an explicit title_branding=false withholds it.
func TestTitleBrandingDefaultsOn(t *testing.T) {
	var c Config
	if !c.TitleBrandingEnabled() {
		t.Fatal("an unset title_branding must default to on")
	}
	on := true
	c.TitleBranding = &on
	if !c.TitleBrandingEnabled() {
		t.Fatal("an explicit title_branding=true must enable the resource")
	}
	off := false
	c.TitleBranding = &off
	if c.TitleBrandingEnabled() {
		t.Fatal("an explicit title_branding=false must withhold the resource")
	}
}

// TestTitleBrandingJSONRoundTrip proves the pointer survives a settings file
// load so an explicit value (either way) is honoured, and that the absence of
// the key is distinguishable from an explicit false.
func TestTitleBrandingJSONRoundTrip(t *testing.T) {
	dir := t.TempDir()
	on := true

	withKey := Config{Path: dir + "/with.json", TitleBranding: &on}
	if err := withKey.Save(""); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(withKey.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.TitleBrandingEnabled() {
		t.Fatal("an explicit true must survive a save/load round trip")
	}

	withoutKey := Config{Path: dir + "/without.json"}
	if err := withoutKey.Save(""); err != nil {
		t.Fatal(err)
	}
	loaded2, err := Load(withoutKey.Path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded2.TitleBranding != nil {
		t.Fatalf("a file without the key must load with a nil pointer, got %v", *loaded2.TitleBranding)
	}
	if !loaded2.TitleBrandingEnabled() {
		t.Fatal("a file without the key must default to on")
	}
}
