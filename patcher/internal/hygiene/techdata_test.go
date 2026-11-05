package hygiene

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"13s-patcher/internal/extraction"
	"13s-patcher/internal/packaging"
)

// TestTechDataAndGeneratedTextsAreClean covers the patch's own technical data
// and the strings the tool generates into reports, help and notices.
func TestTechDataAndGeneratedTextsAreClean(t *testing.T) {
	ms := markers()
	check := func(label, text string) {
		for _, f := range scanBytes(label, []byte(text), ms) {
			t.Errorf("%s: %s", label, f)
		}
	}

	// the embedded descriptor document (binary assets are skipped by the
	// scanner, exactly as the tree scan does)
	dir := filepath.Join("..", "techdata", "data")
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if e.IsDir() || strings.HasSuffix(e.Name(), ".png") || strings.HasSuffix(e.Name(), ".ips") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range scanBytes(e.Name(), raw, ms) {
			t.Errorf("tech data: %s", f)
		}
	}

	// the keys notice must contain no forbidden marker
	check("keys notice", extraction.KeysNotice("/tmp/keys"))
	check("tool help", extraction.ToolHelp())

	// a generated packaging report
	checks := []packaging.Check{{Target: "atmosphere", ID: "layout.entries", Pass: true, Detail: "extra=[] missing=[]"}}
	_ = checks
	report := "13-Sentinels-Patch-Atmosphere.zip 4,733,704 bytes sha256 abc\n" +
		"atmosphere/contents/01003FC01670C000/romfs/ROBO_PATCH_US.CPK\n" +
		"atmosphere/exefs_patches/UnburgerMod/" + strings.ToUpper("4b632573ca5c160eabb70519fb622718956d70ea") + ".ips\n"
	check("packaging report", report)
}
