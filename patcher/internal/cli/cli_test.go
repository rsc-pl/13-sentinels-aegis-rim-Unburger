package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"13s-patcher/internal/cli"
	"13s-patcher/internal/fixture"
)

// isolate redirects the user config directory into a test temp dir so no real
// user configuration is read or written.
func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("APPDATA", t.TempDir())
	t.Setenv("PATCHER_TUI", "0")
}

// copyTitleAssets places the module's shipped display face and style under the
// fixture root, where the live title-footer resolution finds them (production
// resolves them from the project root).
func copyTitleAssets(t *testing.T, dir string) {
	t.Helper()
	dst := filepath.Join(dir, "patcher", "assets", "fonts")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"unburger.otf", "title_footer.toml"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "assets", "fonts", name))
		if err != nil {
			t.Fatalf("the shipped %s must be readable: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(dst, name), raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func commonArgs(t *testing.T, f *fixture.Fixture, jsonOut string) []string {
	t.Helper()
	if err := fixture.PlateBase(f.Dir); err != nil {
		t.Fatal(err)
	}
	if err := fixture.TitleBase(f.Dir); err != nil {
		t.Fatal(err)
	}
	copyTitleAssets(t, f.Dir)
	return []string{
		"-config", filepath.Join(f.Dir, "config.json"),
		"-workdir", f.Dir,
		"-source", f.SourceArchive,
		"-patch-data", f.BundleDir,
		"-output", f.OutputDir,
		"-json", jsonOut,
	}
}

// captureStdout runs fn with os.Stdout redirected to a pipe and returns the text.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()
	fn()
	w.Close()
	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			b.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}
	r.Close()
	return b.String()
}

func TestVersionJSON(t *testing.T) {
	isolate(t)
	out := filepath.Join(t.TempDir(), "v.json")
	if code := cli.Run([]string{"version", "-json", out}); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]string
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["tool"] != "13s-patcher" || doc["version"] == "" {
		t.Fatalf("unexpected version doc: %+v", doc)
	}
}

func TestStatusPreflightBuildValidateInspectJSON(t *testing.T) {
	isolate(t)
	f, err := fixture.Write(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()

	statusOut := filepath.Join(dir, "status.json")
	if code := cli.Run(append([]string{"status"}, commonArgs(t, f, statusOut)...)); code != 0 {
		t.Fatalf("status exit = %d", code)
	}
	var status struct {
		PatchData struct {
			Loaded     bool `json:"loaded"`
			Operations int  `json:"operations"`
		} `json:"patch_data"`
	}
	mustJSON(t, statusOut, &status)
	if !status.PatchData.Loaded || status.PatchData.Operations != f.Operations {
		t.Fatalf("status patch data wrong: %+v", status.PatchData)
	}

	preOut := filepath.Join(dir, "preflight.json")
	if code := cli.Run(append([]string{"preflight"}, commonArgs(t, f, preOut)...)); code != 0 {
		t.Fatalf("preflight exit = %d", code)
	}
	var pre struct {
		Ready  bool `json:"ready"`
		Failed int  `json:"failed"`
	}
	mustJSON(t, preOut, &pre)
	if !pre.Ready || pre.Failed != 0 {
		t.Fatalf("preflight not ready: %+v", pre)
	}

	buildOut := filepath.Join(dir, "build.json")
	if code := cli.Run(append([]string{"build"}, commonArgs(t, f, buildOut)...)); code != 0 {
		t.Fatalf("build exit = %d", code)
	}

	valOut := filepath.Join(dir, "validate.json")
	if code := cli.Run(append([]string{"validate"}, commonArgs(t, f, valOut)...)); code != 0 {
		t.Fatalf("validate exit = %d", code)
	}
	var val struct {
		Ready bool `json:"ready"`
	}
	mustJSON(t, valOut, &val)
	if !val.Ready {
		t.Fatal("validation not ready")
	}

	insOut := filepath.Join(dir, "inspect.json")
	if code := cli.Run(append([]string{"inspect"}, commonArgs(t, f, insOut)...)); code != 0 {
		t.Fatalf("inspect exit = %d", code)
	}
	var ins struct {
		Ready   bool `json:"ready"`
		Entries []struct {
			Path string `json:"path"`
			Size int64  `json:"size"`
		} `json:"entries"`
	}
	mustJSON(t, insOut, &ins)
	if !ins.Ready || len(ins.Entries) == 0 {
		t.Fatalf("inspect wrong: %+v", ins)
	}
}

func TestDoctorJSON(t *testing.T) {
	isolate(t)
	f, err := fixture.Write(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "doctor.json")
	if code := cli.Run(append([]string{"doctor"}, commonArgs(t, f, out)...)); code != 0 {
		t.Fatalf("doctor exit = %d", code)
	}
	var rep struct {
		Failed int `json:"failed"`
	}
	mustJSON(t, out, &rep)
	if rep.Failed != 0 {
		t.Fatalf("doctor failures: %+v", rep)
	}
}

func TestUnknownCommandAndHelp(t *testing.T) {
	isolate(t)
	if code := cli.Run([]string{"definitely-not-a-command"}); code != 2 {
		t.Fatalf("unknown command exit = %d, want 2", code)
	}
	txt := captureStdout(t, func() {
		if code := cli.Run([]string{"help"}); code != 0 {
			t.Fatalf("help exit = %d", code)
		}
	})
	if !strings.Contains(txt, "13s-patcher") || !strings.Contains(txt, "preflight") {
		t.Fatalf("help text unexpected: %q", txt)
	}
}

func TestNoArgsFallsBackToPlainStatus(t *testing.T) {
	isolate(t)
	work := t.TempDir()
	// Run from a work directory that has none of the private inputs.
	old, _ := os.Getwd()
	if err := os.Chdir(work); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)
	txt := captureStdout(t, func() {
		if code := cli.Run(nil); code != 0 {
			t.Fatalf("no-arg exit = %d", code)
		}
	})
	if !strings.Contains(txt, "13s-patcher") {
		t.Fatalf("plain status missing banner: %q", txt)
	}
}

func TestJSONToStdout(t *testing.T) {
	isolate(t)
	txt := captureStdout(t, func() {
		if code := cli.Run([]string{"version", "-json", "-"}); code != 0 {
			t.Fatalf("exit = %d", code)
		}
	})
	if !strings.Contains(txt, "\"tool\"") {
		t.Fatalf("stdout JSON unexpected: %q", txt)
	}
}

// TestBuildRefusesOverwriteWithoutForce proves the CLI keeps the --force
// semantics: a repeated build fails safely without it and succeeds with it.
func TestBuildRefusesOverwriteWithoutForce(t *testing.T) {
	isolate(t)
	f, err := fixture.Write(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	args := commonArgs(t, f, "")
	captureStdout(t, func() {
		if code := cli.Run(append([]string{"build"}, args...)); code != 0 {
			t.Fatalf("first build exit = %d", code)
		}
	})
	var refused int
	captureStdout(t, func() {
		refused = cli.Run(append([]string{"build"}, args...))
	})
	if refused == 0 {
		t.Fatal("a second build without -force must fail")
	}
	captureStdout(t, func() {
		if code := cli.Run(append([]string{"build", "-force"}, args...)); code != 0 {
			t.Fatalf("forced build exit = %d", code)
		}
	})
}

func mustJSON(t *testing.T, path string, v any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}
