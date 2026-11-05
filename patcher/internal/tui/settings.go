package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"13s-patcher/internal/app"
)

// settingsModel edits the persisted configuration. It has two explicit modes:
//
//   - navigation: arrow/tab keys move between fields, enter starts editing the
//     focused text field (or toggles the title-branding row), esc leaves the
//     pane (the dashboard returns to the overview), ctrl+s saves.
//   - editing: keystrokes go to the focused text field, esc and enter stop
//     editing, tab stops editing and moves on, ctrl+s saves.
//
// Only paths and option names are editable: key files are never read, and no
// secret is ever stored.
type settingsModel struct {
	svc     *app.Service
	path    string
	order   []string
	label   map[string]string
	field   map[string]textinput.Model
	focus   int
	edit    bool
	saved   bool
	dirty   bool
	discard bool

	// titleBranding is the effective state of the in-game title-screen footer.
	// It is persisted explicitly (never omitted) on save.
	titleBranding bool
	// titleConfirm is true while the one-time warning prompt awaits an explicit
	// enter; esc cancels and leaves the selection Disabled.
	titleConfirm bool
	// titleAcked records that the warning was accepted in this settings
	// instance, so it is shown once per session, not on every toggle or build.
	titleAcked bool
}

// titleBrandingRow is the settings row that toggles the in-game title-screen
// footer.
const titleBrandingRow = "title_branding"

// TitleBrandingWarning is the exact confirm-once warning shown before the
// title-screen modification is enabled. The 2026-10-19 confirmation covers the
// reference face/style, not the face and letter spacing shipped today.
const TitleBrandingWarning = "The in-game title footer replaces the shipped copyright line on the title screen with \"Unburger Mod v<version>\". The 2026-10-19 Eden run confirmed a reference composition, not the face and title_footer.toml now shipped; a build of the shipped or a restyled composition is runtime-unverified until it is tested."

func isToggleRow(name string) bool { return name == titleBrandingRow }

func newSettingsModel(svc *app.Service) *settingsModel {
	s := &settingsModel{
		svc:  svc,
		path: svc.CfgPath,
		order: []string{
			"target", "roms_base", "roms_update", "keys_dir", "translations_dir",
			"cache_dir", "output_dir", "output_name", "encoder", "extractor_command",
			"plate_base", titleBrandingRow,
		},
		label: map[string]string{
			"target":            "target (atmosphere|emulator|both)",
			"plate_base":        "presentation plate override (optional)",
			"extractor_command": "extractor command",
			titleBrandingRow:    "in-game title branding",
		},
		field: map[string]textinput.Model{},
		// A missing title_branding key leaves the resource off; the pointer is
		// only ever true when the settings file said so explicitly.
		titleBranding: svc.Cfg.TitleBrandingEnabled(),
	}
	initial := map[string]string{
		"target":            svc.Cfg.Target,
		"roms_base":         svc.Cfg.RomsBase,
		"roms_update":       svc.Cfg.RomsUpdate,
		"keys_dir":          svc.Cfg.KeysDir,
		"translations_dir":  svc.Cfg.TranslationsDir,
		"cache_dir":         svc.Cfg.CacheDir,
		"output_dir":        svc.Cfg.OutputDir,
		"output_name":       svc.Cfg.OutputName,
		"encoder":           svc.Cfg.Encoder,
		"extractor_command": strings.Join(svc.Cfg.ExtractorCommand, " "),
		"plate_base":        svc.Cfg.PlateBase,
	}
	for _, name := range s.order {
		if isToggleRow(name) {
			continue
		}
		ti := textinput.New()
		ti.CharLimit = 4096
		ti.SetValue(initial[name])
		ti.Placeholder = name
		ti.Blur()
		s.field[name] = ti
	}
	return s
}

// editing reports whether keystrokes currently go to a text field.
func (s *settingsModel) editing() bool { return s.edit }

// EditFocused starts editing the focused field. On the title-branding row it
// toggles the selection instead (with the one-time confirmation on enable).
func (s *settingsModel) EditFocused() tea.Cmd {
	if s.edit {
		return nil
	}
	if isToggleRow(s.order[s.focus]) {
		return s.toggleTitleBranding()
	}
	s.edit = true
	ti := s.field[s.order[s.focus]]
	ti.Focus()
	s.field[s.order[s.focus]] = ti
	return textinput.Blink
}

// stopEditing leaves the editing mode without clearing the typed buffer.
func (s *settingsModel) stopEditing() {
	if !s.edit {
		return
	}
	s.edit = false
	ti := s.field[s.order[s.focus]]
	ti.Blur()
	s.field[s.order[s.focus]] = ti
}

// Update forwards non-key messages to the focused input.
func (s *settingsModel) Update(msg tea.Msg) (settingsModel, tea.Cmd) {
	if !s.edit {
		return *s, nil
	}
	var cmd tea.Cmd
	ti := s.field[s.order[s.focus]]
	ti, cmd = ti.Update(msg)
	s.field[s.order[s.focus]] = ti
	return *s, cmd
}

// toggleTitleBranding flips the in-game title branding selection. Enabling it
// for the first time in this settings instance arms the one-time warning
// instead of switching immediately; the caller must confirm with enter or
// cancel with esc.
func (s *settingsModel) toggleTitleBranding() tea.Cmd {
	if s.titleBranding {
		s.titleBranding = false
		s.dirty = true
		return nil
	}
	if s.titleAcked {
		s.titleBranding = true
		s.dirty = true
		return nil
	}
	s.titleConfirm = true
	return nil
}

// UpdateKey handles a key: it returns handled=true when it consumed the key.
// An unhandled key falls through to the dashboard (this is how esc leaves the
// pane when no field is being edited).
func (s *settingsModel) UpdateKey(msg tea.KeyPressMsg) (bool, tea.Cmd) {
	key := msg.String()
	// A pending title-branding confirmation owns the keyboard until it is
	// answered: enter confirms, esc cancels and leaves the selection Disabled.
	if s.titleConfirm {
		switch key {
		case "enter":
			s.titleConfirm = false
			s.titleAcked = true
			s.titleBranding = true
			// Persist the confirmed selection immediately, without flushing any
			// text-field buffer that is still being edited.
			on := true
			s.svc.Cfg.TitleBranding = &on
			if err := s.svc.Save(); err == nil {
				s.saved = true
			}
		case "esc":
			s.titleConfirm = false
			s.titleBranding = false
		}
		return true, nil
	}
	switch key {
	case "ctrl+s":
		s.save()
		return true, nil
	}
	if s.edit {
		switch key {
		case "esc", "enter":
			s.stopEditing()
			return true, nil
		case "tab":
			s.stopEditing()
			s.move(1)
			return true, nil
		case "shift+tab":
			s.stopEditing()
			s.move(-1)
			return true, nil
		}
		var cmd tea.Cmd
		ti := s.field[s.order[s.focus]]
		ti, cmd = ti.Update(msg)
		s.field[s.order[s.focus]] = ti
		s.dirty = true
		return true, cmd
	}
	// navigation mode: movement and entering edit mode are consumed, everything
	// else (including esc) is left to the dashboard.
	titleRow := isToggleRow(s.order[s.focus])
	switch key {
	case "enter":
		if titleRow {
			return true, s.toggleTitleBranding()
		}
		cmd := s.EditFocused()
		return true, cmd
	case " ":
		if titleRow {
			return true, s.toggleTitleBranding()
		}
		return false, nil
	case "space":
		if titleRow {
			return true, s.toggleTitleBranding()
		}
		return false, nil
	case "tab", "down":
		s.move(1)
		return true, nil
	case "shift+tab", "up":
		s.move(-1)
		return true, nil
	}
	return false, nil
}

func (s *settingsModel) move(d int) {
	s.focus = (s.focus + d + len(s.order)) % len(s.order)
}

func (s *settingsModel) save() {
	trim := func(name string) string { return strings.TrimSpace(s.field[name].Value()) }
	s.svc.Cfg.Target = trim("target")
	s.svc.Cfg.RomsBase = trim("roms_base")
	s.svc.Cfg.RomsUpdate = trim("roms_update")
	s.svc.Cfg.KeysDir = trim("keys_dir")
	s.svc.Cfg.TranslationsDir = trim("translations_dir")
	s.svc.Cfg.CacheDir = trim("cache_dir")
	s.svc.Cfg.OutputDir = trim("output_dir")
	s.svc.Cfg.OutputName = trim("output_name")
	s.svc.Cfg.Encoder = trim("encoder")
	s.svc.Cfg.PlateBase = trim("plate_base")
	if cmd := trim("extractor_command"); cmd == "" {
		s.svc.Cfg.ExtractorCommand = nil
	} else {
		s.svc.Cfg.ExtractorCommand = strings.Fields(cmd)
	}
	// Persist the title-branding selection explicitly (a non-nil pointer), so
	// the settings file records true or false rather than omitting the key.
	on := s.titleBranding
	s.svc.Cfg.TitleBranding = &on
	if err := s.svc.Save(); err == nil {
		s.saved = true
		s.dirty = false
	}
}

func (s *settingsModel) View(width int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", styleDim.Render("saved to "+s.path))
	for i, name := range s.order {
		cursor := "  "
		if i == s.focus {
			cursor = "> "
		}
		label := name
		if l, ok := s.label[name]; ok {
			label = l
		}
		if isToggleRow(name) {
			value, vs := "Disabled", styleDim
			if s.titleBranding {
				value, vs = "Enabled", styleOK
			}
			b.WriteString(cursor + styleText.Render(label+": ") + vs.Render(value) + "\n")
			if i == s.focus && s.titleConfirm {
				for _, l := range wrapText(TitleBrandingWarning, width-4) {
					b.WriteString("    " + styleWarn.Render(l) + "\n")
				}
				b.WriteString("    " + styleDim.Render("enter confirms · esc cancels (stays Disabled)") + "\n")
			}
			continue
		}
		mark := ""
		if i == s.focus && s.edit {
			mark = styleKey.Render("  [editing]")
		}
		b.WriteString(cursor + styleText.Render(label) + mark + "\n")
		ti := s.field[name]
		b.WriteString("    " + ti.View() + "\n")
	}
	state := "navigation"
	if s.edit {
		state = "editing"
	}
	if s.dirty {
		state += " · unsaved changes (ctrl+s saves)"
	}
	b.WriteString("\n" + styleDim.Render("mode: "+state+"   ·   enter edit/toggle · space toggle · esc stop editing · esc again back · tab move · ctrl+s save"))
	return b.String()
}
