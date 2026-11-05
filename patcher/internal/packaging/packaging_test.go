package packaging

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testBuildID = "0123456789ABCDEF0123456789ABCDEF01234567"

func testRequest(t *testing.T) Request {
	t.Helper()
	return Request{
		ArchiveBytes: []byte("CPK " + strings.Repeat("\x00", 600)),
		PatchBytes:   []byte("PATCHEOF"),
		BuildID:      testBuildID,
		TitleID:      "01003FC01670C000",
		OutputDir:    t.TempDir(),
		Overwrite:    true,
	}
}

func entries(t *testing.T, path string) map[string][]byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b := new(bytes.Buffer)
		if _, err := b.ReadFrom(rc); err != nil {
			t.Fatal(err)
		}
		rc.Close()
		out[f.Name] = b.Bytes()
	}
	return out
}

func TestAtmosphereLayout(t *testing.T) {
	req := testRequest(t)
	art, err := Build(req, TargetAtmosphere)
	if err != nil {
		t.Fatal(err)
	}
	if !art.Validated {
		t.Fatal("artifact not validated")
	}
	got := entries(t, art.Path)
	wantArchive := "atmosphere/contents/" + req.TitleID + "/romfs/" + RomfsArchiveName
	wantPatch := AtmosphereRoot + "/" + AtmospherePatchDir + "/" + testBuildID + ".ips"
	if len(got) != 2 {
		t.Fatalf("entries %v", keys(got))
	}
	if !bytes.Equal(got[wantArchive], req.ArchiveBytes) {
		t.Fatal("archive payload mismatch")
	}
	if !bytes.Equal(got[wantPatch], req.PatchBytes) {
		t.Fatal("patch payload mismatch")
	}
}

func TestEmulatorLayoutHasNoCfwNesting(t *testing.T) {
	req := testRequest(t)
	art, err := Build(req, TargetEmulator)
	if err != nil {
		t.Fatal(err)
	}
	got := entries(t, art.Path)
	for name := range got {
		if strings.HasPrefix(name, AtmosphereRoot+"/") {
			t.Fatalf("emulator archive must not contain an atmosphere/ tree: %s", name)
		}
		if strings.Contains(name, "/romfs/") && strings.HasSuffix(name, ".ips") {
			t.Fatalf("executable patch must not sit inside romfs/: %s", name)
		}
	}
	if !bytes.Equal(got[ModFolder+"/romfs/"+RomfsArchiveName], req.ArchiveBytes) {
		t.Fatal("archive payload mismatch")
	}
	if !bytes.Equal(got[ModFolder+"/exefs/"+testBuildID+".ips"], req.PatchBytes) {
		t.Fatal("patch payload mismatch")
	}
}

func TestPackagingIsDeterministic(t *testing.T) {
	req := testRequest(t)
	a, err := Build(req, TargetAtmosphere)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Build(req, TargetAtmosphere)
	if err != nil {
		t.Fatal(err)
	}
	if a.SHA256 != b.SHA256 {
		t.Fatalf("archives differ between runs: %s vs %s", a.SHA256, b.SHA256)
	}
}

func TestBuildRefusesOverwrite(t *testing.T) {
	req := testRequest(t)
	if _, err := Build(req, TargetAtmosphere); err != nil {
		t.Fatal(err)
	}
	req.Overwrite = false
	if _, err := Build(req, TargetAtmosphere); err == nil {
		t.Fatal("a second build must not replace the archive without overwrite")
	}
}

func TestValidationRejectsExtraEntries(t *testing.T) {
	req := testRequest(t)
	art, err := Build(req, TargetEmulator)
	if err != nil {
		t.Fatal(err)
	}
	// rebuild the archive with an extra file
	got := entries(t, art.Path)
	got["extra/notes.txt"] = []byte("hello")
	raw, err := writeZip(got)
	if err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(t.TempDir(), "bad.zip")
	if err := os.WriteFile(bad, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	checks, err := ValidateFile(bad, TargetEmulator, req)
	if err != nil {
		t.Fatal(err)
	}
	ok := true
	for _, c := range checks {
		if !c.Pass {
			ok = false
		}
	}
	if ok {
		t.Fatal("validation accepted an archive with an extra entry")
	}
}

func TestValidationRejectsContainerPayload(t *testing.T) {
	req := testRequest(t)
	got := map[string][]byte{
		ModFolder + "/romfs/" + RomfsArchiveName:     req.ArchiveBytes,
		ModFolder + "/exefs/" + testBuildID + ".ips": req.PatchBytes,
		ModFolder + "/game.nsp":                      []byte("not a container"),
	}
	raw, err := writeZip(got)
	if err != nil {
		t.Fatal(err)
	}
	checks, err := validateBytes(raw, TargetEmulator, req)
	if err != nil {
		t.Fatal(err)
	}
	failed := false
	for _, c := range checks {
		if !c.Pass {
			failed = true
		}
	}
	if !failed {
		t.Fatal("validation accepted a container payload")
	}
}

func keys(m map[string][]byte) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestBrandedLayoutNames pins the public layout names: the emulator mod folder
// is the game title without its colon plus the mod name (portable, no invalid
// punctuation), and the Atmosphère ExeFS patch directory is the branded
// UnburgerMod variant.
func TestBrandedLayoutNames(t *testing.T) {
	if ModFolder != "13 Sentinels Aegis Rim - Unburger Mod" {
		t.Fatalf("ModFolder = %q", ModFolder)
	}
	for _, bad := range []string{":", "/", `\`, "*", "?", `"`, "<", ">", "|"} {
		if strings.Contains(ModFolder, bad) {
			t.Fatalf("ModFolder %q contains the invalid character %q", ModFolder, bad)
		}
	}
	if AtmospherePatchDir != "exefs_patches/UnburgerMod" {
		t.Fatalf("AtmospherePatchDir = %q", AtmospherePatchDir)
	}
	if strings.Count(AtmospherePatchDir, "/") != 1 {
		t.Fatalf("AtmospherePatchDir must be exactly one directory deep, got %q", AtmospherePatchDir)
	}
}

// TestAtmosphereLayoutKeepsBrandedPatchDir proves the renamed patch directory is
// what the produced archive actually carries, with no extra nesting.
func TestAtmosphereLayoutKeepsBrandedPatchDir(t *testing.T) {
	req := testRequest(t)
	art, err := Build(req, TargetAtmosphere)
	if err != nil {
		t.Fatal(err)
	}
	got := entries(t, art.Path)
	patch := "atmosphere/exefs_patches/UnburgerMod/" + testBuildID + ".ips"
	if _, ok := got[patch]; !ok {
		t.Fatalf("produced archive is missing the branded patch path %q: %v", patch, keys(got))
	}
	for name := range got {
		if strings.HasPrefix(name, "atmosphere/atmosphere/") {
			t.Fatalf("archive is wrapped in an extra directory: %s", name)
		}
	}
}
