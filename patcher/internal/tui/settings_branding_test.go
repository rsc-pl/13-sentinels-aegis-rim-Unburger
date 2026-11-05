package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"13s-patcher/internal/app"
	"13s-patcher/internal/config"
	"13s-patcher/internal/fixture"
	"13s-patcher/internal/packaging"
)

// titleTestService builds a service over a fixture that carries a version.txt,
// so the title-branding panel can resolve a real footer label.
func titleTestService(t *testing.T, version string) (*app.Service, *fixture.Fixture) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f, err := fixture.Write(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if version != "" {
		if err := os.WriteFile(filepath.Join(f.Dir, "version.txt"), []byte(version), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return testService(t, f), f
}

func focusTitleRow(s *settingsModel) {
	for i, n := range s.order {
		if n == titleBrandingRow {
			s.focus = i
			return
		}
	}
}

func readConfigJSON(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// TestSettingsTitleBrandingDefaultsEnabled pins the new default: a settings
// file without the key shows Enabled, because the footer is part of a normal
// build.
func TestSettingsTitleBrandingDefaultsEnabled(t *testing.T) {
	svc, _ := titleTestService(t, "0.1.0")
	m := newModel(svc)
	m.setFocusByMode(viewSettings)
	if !m.settings.titleBranding {
		t.Fatal("an unset title_branding must start Enabled")
	}
	if v := m.settings.View(80); !strings.Contains(v, "in-game title branding: ") || !strings.Contains(v, "Enabled") {
		t.Fatalf("the enabled state is not shown:\n%s", v)
	}
}

// TestTitleBrandingWarningExact pins the exact one-time warning wording.
func TestTitleBrandingWarningExact(t *testing.T) {
	const want = "The in-game title footer replaces the shipped copyright line on the title screen with \"Unburger Mod v<version>\". The 2026-10-19 Eden run confirmed a reference composition, not the face and title_footer.toml now shipped; a build of the shipped or a restyled composition is runtime-unverified until it is tested."
	if TitleBrandingWarning != want {
		t.Fatalf("the warning text changed:\n got %q\nwant %q", TitleBrandingWarning, want)
	}
}

// TestSettingsTitleBrandingConfirmWarning pins the one-time warning and that
// esc cancels it, leaving the selection Disabled. The confirm only appears when
// re-enabling a member the user had turned off.
func TestSettingsTitleBrandingConfirmWarning(t *testing.T) {
	svc, _ := titleTestService(t, "0.1.0")
	off := false
	svc.Cfg.TitleBranding = &off
	m := newModel(svc)
	m.setFocusByMode(viewSettings)
	focusTitleRow(m.settings)

	m.Update(tea.KeyPressMsg{Code: tea.KeySpace})
	if !m.settings.titleConfirm {
		t.Fatal("enabling must first arm the confirmation prompt")
	}
	if m.settings.titleBranding {
		t.Fatal("the selection must not switch before the confirmation")
	}
	view := m.settings.View(120)
	if !strings.Contains(view, "The in-game title footer replaces the shipped copyright line") ||
		!strings.Contains(view, "runtime-unverified") {
		t.Fatalf("the warning text is missing:\n%s", view)
	}
	// esc cancels and stays Disabled.
	m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.settings.titleConfirm || m.settings.titleBranding {
		t.Fatalf("esc must cancel and leave Disabled (confirm=%v enabled=%v)", m.settings.titleConfirm, m.settings.titleBranding)
	}
	// enter confirms.
	m.Update(tea.KeyPressMsg{Code: tea.KeySpace})
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.settings.titleConfirm || !m.settings.titleBranding {
		t.Fatalf("enter must confirm and enable (confirm=%v enabled=%v)", m.settings.titleConfirm, m.settings.titleBranding)
	}
}

// TestSettingsTitleBrandingConfirmPersistsImmediately proves a confirmed
// enable is written without requiring ctrl+s.
func TestSettingsTitleBrandingConfirmPersistsImmediately(t *testing.T) {
	svc, f := titleTestService(t, "0.1.0")
	off := false
	svc.Cfg.TitleBranding = &off
	m := newModel(svc)
	m.setFocusByMode(viewSettings)
	focusTitleRow(m.settings)
	m.Update(tea.KeyPressMsg{Code: tea.KeySpace})
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	loaded, err := config.Load(filepath.Join(f.Dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.TitleBrandingEnabled() {
		t.Fatalf("the confirmed enable must be persisted:\n%s", readConfigJSON(t, filepath.Join(f.Dir, "config.json")))
	}
}

// TestSettingsTitleBrandingTogglePersistsTrue proves ctrl+s writes an explicit
// title_branding=true when the member is re-enabled from an explicit false.
func TestSettingsTitleBrandingTogglePersistsTrue(t *testing.T) {
	svc, f := titleTestService(t, "0.1.0")
	off := false
	svc.Cfg.TitleBranding = &off
	m := newModel(svc)
	m.setFocusByMode(viewSettings)
	focusTitleRow(m.settings)

	m.Update(tea.KeyPressMsg{Code: tea.KeySpace})
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if !strings.Contains(readConfigJSON(t, filepath.Join(f.Dir, "config.json")), `"title_branding": true`) {
		t.Fatalf("title_branding=true was not persisted:\n%s", readConfigJSON(t, filepath.Join(f.Dir, "config.json")))
	}
	loaded, err := config.Load(svc.CfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.TitleBrandingEnabled() {
		t.Fatal("the persisted settings must enable the resource")
	}
}

// TestSettingsTitleBrandingTogglePersistsExplicitFalse proves disabling writes
// an explicit title_branding=false (not an omitted key).
func TestSettingsTitleBrandingTogglePersistsExplicitFalse(t *testing.T) {
	svc, f := titleTestService(t, "0.1.0")
	on := true
	svc.Cfg.TitleBranding = &on
	m := newModel(svc)
	m.setFocusByMode(viewSettings)
	if !m.settings.titleBranding {
		t.Fatal("an explicit true must load as Enabled")
	}
	focusTitleRow(m.settings)
	m.Update(tea.KeyPressMsg{Code: tea.KeySpace})
	m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})

	raw := readConfigJSON(t, filepath.Join(f.Dir, "config.json"))
	if !strings.Contains(raw, `"title_branding": false`) {
		t.Fatalf("an explicit false was not persisted:\n%s", raw)
	}
	var probe map[string]any
	if err := json.Unmarshal([]byte(raw), &probe); err != nil {
		t.Fatal(err)
	}
	if _, ok := probe["title_branding"]; !ok {
		t.Fatal("the key must be present, not omitted")
	}
}

// TestSettingsTitleBrandingReopenRestores proves a saved selection is restored
// when the tool is reopened.
func TestSettingsTitleBrandingReopenRestores(t *testing.T) {
	svc, f := titleTestService(t, "0.1.0")
	m := newModel(svc)
	m.setFocusByMode(viewSettings)
	focusTitleRow(m.settings)
	// Persist the explicit enabled selection.
	m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})

	svc2, err := app.New(app.Options{
		ConfigPath:    filepath.Join(f.Dir, "config.json"),
		WorkDir:       f.Dir,
		SourceArchive: f.SourceArchive,
		PatchDataDir:  f.BundleDir,
		OutputDir:     f.OutputDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !svc2.Cfg.TitleBrandingEnabled() {
		t.Fatal("reopening must restore the enabled selection")
	}
	m2 := newModel(svc2)
	m2.setFocusByMode(viewSettings)
	if !m2.settings.titleBranding {
		t.Fatal("the Settings row must reopen Enabled")
	}
	if v := m2.settings.View(80); !strings.Contains(v, "in-game title branding: ") || !strings.Contains(v, "Enabled") {
		t.Fatalf("the enabled state is not shown:\n%s", v)
	}
}

// TestSettingsTitleBrandingMissingKeyDefaultsEnabled proves an absent key is
// not confused with an explicit false: it defaults to Enabled.
func TestSettingsTitleBrandingMissingKeyDefaultsEnabled(t *testing.T) {
	svc, f := titleTestService(t, "0.1.0")
	// Overwrite the settings file without the key and reload.
	if err := os.WriteFile(svc.CfgPath, []byte(`{"target":"both"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	svc3, err := app.New(app.Options{
		ConfigPath:    filepath.Join(f.Dir, "config.json"),
		WorkDir:       f.Dir,
		SourceArchive: f.SourceArchive,
		PatchDataDir:  f.BundleDir,
		OutputDir:     f.OutputDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if svc3.Cfg.TitleBranding != nil {
		t.Fatal("a missing key must load as nil")
	}
	if !svc3.Cfg.TitleBrandingEnabled() {
		t.Fatal("a missing key must default to Enabled")
	}
	m := newModel(svc3)
	m.setFocusByMode(viewSettings)
	if !m.settings.titleBranding {
		t.Fatal("a missing key must render Enabled")
	}
}

// TestBuildScreenShowsTitleBranding proves the Build pane states the effective
// selection, the footer label from version.txt and the runtime state.
func TestBuildScreenShowsTitleBranding(t *testing.T) {
	svc, _ := titleTestService(t, "2.3.4")
	m := newModel(svc)
	m.Update(tea.WindowSizeMsg{Width: 110, Height: 40})
	m.Update(statusLoadedMsg{st: svc.Status()})
	m.setFocusByMode(viewBuild)
	m.refreshContent()
	body := m.content.View()
	if !strings.Contains(body, "in-game title branding") || !strings.Contains(body, "Enabled") {
		t.Fatalf("enabled selection missing from the build pane:\n%s", body)
	}
	if !strings.Contains(body, "Unburger Mod v2.3.4") {
		t.Fatalf("footer label missing:\n%s", body)
	}
	if !strings.Contains(body, "UNVERIFIED (shipped face/style") {
		t.Fatalf("the shipped composition must read as unverified:\n%s", body)
	}
	if strings.Contains(body, "confirmed (reference composition)") {
		t.Fatalf("the shipped composition must not claim the reference confirmation:\n%s", body)
	}

	// Disable through the Build screen: immediate, no confirmation.
	m.Update(tea.KeyPressMsg{Code: 'T'})
	if m.titleBrandingEnabled() {
		t.Fatal("T must disable an enabled selection immediately")
	}
	if !strings.Contains(m.content.View(), "Disabled") || !strings.Contains(m.content.View(), "not included") {
		t.Fatalf("the disabled pane must state the footer is not included:\n%s", m.content.View())
	}

	// Re-enable through the Build screen with the confirm-once warning.
	m.Update(tea.KeyPressMsg{Code: 'T'})
	if !m.titleConfirm {
		t.Fatal("T must arm the confirmation prompt")
	}
	confirmBody := m.renderBody()
	if !strings.Contains(confirmBody, "The in-game title footer replaces the shipped copyright line") ||
		!strings.Contains(confirmBody, "runtime-unverified") {
		t.Fatalf("the warning is missing from the build confirm:\n%s", confirmBody)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.titleBrandingEnabled() || m.titleConfirm {
		t.Fatalf("enter must enable the resource (enabled=%v confirm=%v)", m.titleBrandingEnabled(), m.titleConfirm)
	}
	if !strings.Contains(m.content.View(), "in-game title branding") || !strings.Contains(m.content.View(), "Enabled") {
		t.Fatalf("enabled selection missing:\n%s", m.content.View())
	}
	if !strings.Contains(m.content.View(), "Unburger Mod v2.3.4") {
		t.Fatalf("footer label missing:\n%s", m.content.View())
	}
}

// TestBuildSummaryStatesBranding proves the post-build summary states the
// effective state and, when enabled, the member and runtime warning.
func TestBuildSummaryStatesBranding(t *testing.T) {
	offSummary := summaryBuild(&app.BuildResult{
		Packaging: &packaging.Report{Target: packaging.TargetBoth},
		Components: app.PatchComponents{
			TitleBranding: app.TitleBrandingStatus{Enabled: false, Member: "GUI/title.ftx"},
			AddedResources: []app.ResourceStatus{
				{Member: "GUI/title.ftx", Kind: "compose_title", Detail: "withheld: title branding off"},
			},
		},
	})
	if !strings.Contains(offSummary, "in-game title branding: Disabled") {
		t.Fatalf("disabled state missing:\n%s", offSummary)
	}
	if !strings.Contains(offSummary, "withheld GUI/title.ftx") {
		t.Fatalf("the withheld member must be recorded as withheld, not added:\n%s", offSummary)
	}
	if strings.Contains(offSummary, "added GUI/title.ftx") {
		t.Fatalf("a disabled build summary must not claim the title member was added:\n%s", offSummary)
	}
	onSummary := summaryBuild(&app.BuildResult{
		Packaging: &packaging.Report{Target: packaging.TargetBoth},
		Components: app.PatchComponents{
			TitleBranding: app.TitleBrandingStatus{
				Enabled: true, Member: "GUI/title.ftx", Included: true,
				Footer: "Unburger Mod v0.1.0", RuntimeStatus: "RUNTIME UNVERIFIED",
			},
			AddedResources: []app.ResourceStatus{
				{Member: "GUI/title.ftx", Kind: "compose_title", Present: true, Detail: "composed"},
			},
		},
	})
	for _, want := range []string{"in-game title branding: Enabled", "Unburger Mod v0.1.0", "GUI/title.ftx included", "RUNTIME UNVERIFIED"} {
		if !strings.Contains(onSummary, want) {
			t.Fatalf("enabled summary missing %q:\n%s", want, onSummary)
		}
	}
}
