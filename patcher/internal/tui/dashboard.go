package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"13s-patcher/internal/app"
	"13s-patcher/internal/build"
	"13s-patcher/internal/hashutil"
	"13s-patcher/internal/modversion"
	"13s-patcher/internal/packaging"
	"13s-patcher/internal/progress"
)

// viewMode selects the content pane.
type viewMode int

const (
	viewOverview viewMode = iota
	viewPreflight
	viewBuild
	viewValidate
	viewInspect
	viewTask
	viewLogs
	viewSettings
)

type menuItem struct {
	id    string
	title string
	desc  string
	kind  string // view | task
	mode  viewMode
}

func menu() []menuItem {
	return []menuItem{
		{"overview", "Overview", "everything at a glance", "view", viewOverview},
		{"preflight", "Preflight", "check inputs and outputs before building", "task", viewPreflight},
		{"build", "Build Patch", "rebuild the archive once, then package per target", "task", viewBuild},
		{"validate", "Full Validation", "preflight + build + package + verify", "task", viewValidate},
		{"inspect", "Inspect Outputs", "re-open a produced archive and check its layout", "task", viewInspect},
		{"logs", "Logs / Reports", "browse generated reports", "view", viewLogs},
		{"settings", "Settings", "paths and options", "view", viewSettings},
	}
}

// overwriteChoice is the modal decision for an existing output archive.
type overwriteChoice int

const (
	choiceReplace overwriteChoice = iota
	choiceKeep
	choiceCancel
)

// ---- messages ----

type statusLoadedMsg struct {
	st  *app.Status
	err error
}
type logLineMsg string
type progressMsg progress.Event
type destinationsMsg struct {
	taskID string
	states []app.DestinationState
	err    error
}
type taskDoneMsg struct {
	name    string
	summary string
	err     error
}
type fileListMsg struct {
	entries []reportFile
	err     error
}
type fileContentMsg struct {
	path string
	text string
	err  error
}

type reportFile struct {
	Path string
	Size int64
	Mod  time.Time
}

// progressState is the live progress the dashboard renders. It is kept across
// views so switching panes never resets the picture of a running task.
type progressState struct {
	running   bool
	op        string
	stage     progress.Stage
	item      string
	note      string
	completed int64
	total     int64
	bytes     int64
	status    string
	start     time.Time
	overall   float64
	stageFrac float64
	warning   string
	order     []progress.Stage
	done      map[progress.Stage]bool
}

// ---- model ----

type model struct {
	svc    *app.Service
	width  int
	height int

	menu  []menuItem
	focus int

	status    *app.Status
	statusErr error

	// modVersion is the Unburger Mod version from the repository-root
	// version.txt; modVersionErr carries the diagnostic when it is missing or
	// invalid. A version is never invented: the sidebar and the overview show a
	// diagnostic instead.
	modVersion    string
	modVersionErr error

	taskTitle   string
	taskSummary string
	taskErr     error
	taskLog     []string
	busy        bool
	target      packaging.Target
	cancel      context.CancelFunc
	taskCh      chan tea.Msg

	logView  viewport.Model
	content  viewport.Model
	spin     spinner.Model
	spinTick int
	files    []reportFile
	fileIdx  int
	fileText string
	filePath string

	settings *settingsModel

	showHelp bool
	quit     bool

	prog progressState

	// overwrite modal
	modal        bool
	choice       overwriteChoice
	dests        []app.DestinationState
	modalTask    string
	approvedOver bool

	// titleBranding confirmation (Build screen shortcut). titleAck records that
	// the one-time warning was accepted in this session.
	titleConfirm bool
	titleAck     bool

	logFullscreen bool

	// computed layout
	bodyOuter int
	logOuter  int
	sideWidth int
}

func newModel(svc *app.Service) *model {
	m := &model{
		svc:       svc,
		menu:      menu(),
		target:    svc.Target(),
		spin:      spinner.New(spinner.WithSpinner(spinner.MiniDot), spinner.WithStyle(styleTitle)),
		logView:   viewport.New(viewport.WithWidth(80), viewport.WithHeight(8)),
		content:   viewport.New(viewport.WithWidth(80), viewport.WithHeight(20)),
		settings:  newSettingsModel(svc),
		width:     80,
		height:    24,
		prog:      progressState{order: progress.Order, done: map[progress.Stage]bool{}},
		sideWidth: 26,
	}
	m.layout()
	m.reloadModVersion()
	m.pushLog("13s-patcher " + build.VersionString() + " — dashboard started")
	if m.modVersionErr != nil {
		m.pushLog("mod version: " + m.modVersionErr.Error())
	} else {
		m.pushLog(modversion.Display(m.modVersion))
	}
	return m
}

// reloadModVersion re-reads version.txt so an edited file is reflected after a
// status refresh, without any source change or rebuild.
func (m *model) reloadModVersion() {
	m.modVersion, m.modVersionErr = m.svc.ModVersion()
}

func (m *model) pushLog(s string) {
	m.taskLog = append(m.taskLog, strings.TrimRight(s, "\n"))
	if len(m.taskLog) > 500 {
		m.taskLog = m.taskLog[len(m.taskLog)-500:]
	}
	// Follow the bottom unless the user has scrolled away; scrolling back to
	// the bottom resumes following.
	follow := m.logView.AtBottom() || m.logView.TotalLineCount() == 0
	m.logView.SetContent(strings.Join(m.taskLog, "\n"))
	if follow {
		m.logView.GotoBottom()
	}
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(m.spin.Tick, m.loadStatus())
}

func (m *model) loadStatus() tea.Cmd {
	svc := m.svc
	return func() tea.Msg {
		return statusLoadedMsg{st: svc.Status()}
	}
}

func waitFor(ch chan tea.Msg) tea.Cmd {
	return func() tea.Msg { return <-ch }
}

// rearm returns the next read from the running task's channel, when one exists.
func (m *model) rearm() tea.Cmd {
	if m.taskCh == nil {
		return nil
	}
	return waitFor(m.taskCh)
}

func (m *model) runTask(title string, fn func(ctx context.Context, log func(string)) (string, error)) tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.busy = true
	m.taskTitle = title
	m.taskSummary = ""
	m.taskErr = nil
	m.taskLog = nil
	m.prog = progressState{running: true, start: time.Now(), order: progress.Order, done: map[progress.Stage]bool{}}
	m.pushLog("=== " + title + " ===")
	ch := make(chan tea.Msg, 512)
	m.taskCh = ch
	svc := m.svc
	throttle := progress.NewThrottle(func(ev progress.Event) {
		select {
		case ch <- progressMsg(ev):
		default:
		}
	}, 60*time.Millisecond)
	go func() {
		defer close(ch)
		log := func(s string) {
			select {
			case ch <- logLineMsg(s):
			default:
			}
		}
		svc.Progress = throttle.Emit
		defer func() { svc.Progress = nil }()
		summary, err := fn(ctx, log)
		ch <- taskDoneMsg{name: title, summary: summary, err: err}
	}()
	return tea.Batch(waitFor(ch), m.spin.Tick)
}

// beginGuardedTask inspects the output archives before a build and opens the
// overwrite dialog when any of them already exists.
func (m *model) beginGuardedTask(id string) tea.Cmd {
	if id != "build" {
		return m.startTask(id)
	}
	m.modalTask = id
	return m.inspectDestinations(id)
}

func (m *model) inspectDestinations(taskID string) tea.Cmd {
	svc := m.svc
	target := m.target
	return func() tea.Msg {
		states, err := svc.InspectDestinations(context.Background(), target)
		return destinationsMsg{taskID: taskID, states: states, err: err}
	}
}

func (m *model) startTask(id string) tea.Cmd {
	svc := m.svc
	target := m.target
	switch id {
	case "preflight":
		return m.runTask("Preflight", func(ctx context.Context, log func(string)) (string, error) {
			svc.Logf = log
			defer func() { svc.Logf = nil }()
			rep, err := svc.Preflight(ctx, target)
			if err != nil {
				if ctx.Err() != nil {
					return "", app.ErrCancelled
				}
				return "", err
			}
			return summaryPreflight(rep), nil
		})
	case "build":
		overwrite := m.approvedOver
		return m.runTask("Build Patch", func(ctx context.Context, log func(string)) (string, error) {
			svc.Logf = log
			defer func() { svc.Logf = nil }()
			res, err := svc.Build(ctx, overwrite, target)
			if err != nil {
				if ctx.Err() != nil {
					return "", app.ErrCancelled
				}
				return "", err
			}
			logArtifacts(log, res)
			return summaryBuild(res), nil
		})
	case "keep":
		return m.runTask("Build Patch (keep existing)", func(ctx context.Context, log func(string)) (string, error) {
			svc.Logf = log
			defer func() { svc.Logf = nil }()
			res, err := svc.BuildKeepingExisting(ctx, target)
			if err != nil {
				if ctx.Err() != nil {
					return "", app.ErrCancelled
				}
				return "", err
			}
			logArtifacts(log, res)
			return summaryBuild(res), nil
		})
	case "validate":
		return m.runTask("Full Validation", func(ctx context.Context, log func(string)) (string, error) {
			svc.Logf = log
			defer func() { svc.Logf = nil }()
			rep, err := svc.Validate(ctx, target)
			if err != nil {
				if ctx.Err() != nil {
					return "", app.ErrCancelled
				}
				return "", err
			}
			return summaryValidate(rep), nil
		})
	case "setup":
		return m.runTask("Setup", func(ctx context.Context, log func(string)) (string, error) {
			svc.Logf = log
			defer func() { svc.Logf = nil }()
			set, err := svc.AutoConfigure(ctx)
			if err != nil {
				return "", err
			}
			var b strings.Builder
			if len(set) == 0 {
				b.WriteString("nothing new to configure\n")
			}
			for _, line := range set {
				fmt.Fprintf(&b, "configured %s\n", line)
			}
			b.WriteString("\nsettings: " + svc.CfgPath + "\n")
			return b.String(), nil
		})
	case "inspect":
		return m.runTask("Inspect Outputs", func(ctx context.Context, log func(string)) (string, error) {
			res, err := svc.InspectOutput(ctx, "", target)
			if err != nil {
				return "", err
			}
			var b strings.Builder
			fmt.Fprintf(&b, "output %s\n", res.Path)
			if res.CPK != nil {
				fmt.Fprintf(&b, "members=%d compressed=%d all_checks_ok=%v\n",
					res.CPK.MemberCount, res.CPK.CompressedCount, res.CPK.AllChecksOK)
			}
			fmt.Fprintf(&b, "title member included: %v\n", res.TitleIncluded)
			if res.TitleIncluded {
				fmt.Fprintf(&b, "runtime %s\n", res.TitleRuntime)
			}
			if res.TitleNote != "" {
				fmt.Fprintf(&b, "%s\n", res.TitleNote)
			}
			for _, e := range res.Entries {
				fmt.Fprintf(&b, "%s %10d %s\n", e.Path, e.Size, e.SHA256)
			}
			for _, ch := range res.Checks {
				st := "PASS"
				if !ch.Pass {
					st = "FAIL"
				}
				fmt.Fprintf(&b, "%s %-28s %s\n", st, ch.ID, ch.Detail)
			}
			if res.CPK != nil {
				for _, ch := range res.CPK.Checks {
					st := "PASS"
					if !ch.Pass {
						st = "FAIL"
					}
					fmt.Fprintf(&b, "%s %-28s %s\n", st, ch.Title, ch.Detail)
				}
			}
			return b.String(), nil
		})
	}
	return nil
}

// logArtifacts writes the full digests of every produced output to the log, so
// they remain available after the summary panel.
func logArtifacts(log func(string), res *app.BuildResult) {
	for _, a := range res.Artifacts {
		log(fmt.Sprintf("%s %s %d bytes sha256=%s validated=%v", a.Label, a.Path, a.Size, a.SHA256, a.Validated))
	}
	for _, t := range res.Reused {
		log(fmt.Sprintf("%s reused (kept existing)", t.Label()))
	}
}

func summaryPreflight(rep *app.PreflightReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "target %s\n", rep.Target)
	for _, s := range rep.Steps {
		fmt.Fprintf(&b, "%s %-26s %s\n", s.Status, s.Title, s.Detail)
	}
	fmt.Fprintf(&b, "\nready=%v · %d failed · %d warnings\n", rep.Ready, rep.Failed, rep.Warned)
	return b.String()
}

func summaryValidate(rep *app.ValidateResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "target %s\n", rep.Target)
	if rep.Error != "" {
		fmt.Fprintf(&b, "error: %s\n", rep.Error)
	}
	fmt.Fprintf(&b, "components: %d translation record(s) · executable patch %d record(s)\n",
		rep.Components.TranslationsRecords, rep.Components.ExecutablePatchRecs)
	tb := rep.Components.TitleBranding
	if tb.Enabled {
		footer := tb.Footer
		if footer == "" {
			footer = modversion.Display("")
		}
		member := ""
		if tb.Included {
			member = " · " + tb.Member + " included"
			if lbl := tb.StorageLabel(); lbl != "" {
				member += " + " + lbl
			}
		}
		fmt.Fprintf(&b, "in-game title branding: Enabled%s · footer %s · runtime %s\n", member, footer, titleRuntimeLabel(tb))
	} else {
		fmt.Fprintf(&b, "in-game title branding: Disabled\n")
	}
	for _, r := range rep.Components.AddedResources {
		fmt.Fprintf(&b, "added resource %s %s\n", r.Member, r.StateLabel())
	}
	fails := 0
	for _, c := range rep.Checks {
		st := "PASS"
		if !c.Pass {
			st = "FAIL"
			fails++
		}
		fmt.Fprintf(&b, "%s %-28s %s\n", st, c.ID, c.Detail)
	}
	fmt.Fprintf(&b, "\nvalidation ready=%v (%d failing)\n", rep.Ready, fails)
	return b.String()
}

// summaryBuild renders the post-build report the Build/Validation pane shows.
func summaryBuild(res *app.BuildResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "target %s\n", res.Packaging.Target)
	if res.Built && res.Archive != nil {
		cpk := res.Archive
		fmt.Fprintf(&b, "archive   %s\n", cpk.OutputPath)
		fmt.Fprintf(&b, "          %d bytes · sha256 %s\n", cpk.OutputSize, cpk.OutputSHA256)
		fmt.Fprintf(&b, "          %d operations · %d members edited · added %d · verified=%v · unmodified intact=%v\n",
			cpk.Operations, cpk.MembersEdited, cpk.Report.BanksAdded, cpk.Report.Verified, cpk.Report.UnmodifiedIntact)
	} else {
		b.WriteString(styleDim.Render("no rebuild — existing outputs reused") + "\n")
	}
	fmt.Fprintf(&b, "deterministic=%v\n", res.Packaging.Determinism)
	b.WriteString("outputs:\n")
	for _, a := range res.Artifacts {
		fmt.Fprintf(&b, "  %-10s %s\n", a.Label, a.Path)
		fmt.Fprintf(&b, "             %d bytes · sha256 %s · validated=%v\n", a.Size, hashutil.Short(a.SHA256), a.Validated)
	}
	for _, t := range res.Reused {
		fmt.Fprintf(&b, "  %-10s %s\n", t.Label(), styleDim.Render("reused (kept existing)"))
	}
	c := res.Components
	// The effective title-branding state is stated explicitly, so a build with
	// the member can never read like one without it.
	tb := c.TitleBranding
	if tb.Enabled {
		fmt.Fprintf(&b, "in-game title branding: Enabled\n")
		switch {
		case tb.Footer != "":
			fmt.Fprintf(&b, "title footer: %s\n", tb.Footer)
		case tb.FooterError != "":
			fmt.Fprintf(&b, "title footer: %s (%s)\n", modversion.Display(""), tb.FooterError)
		}
		if tb.Included {
			memberLabel := tb.Member + " included"
			if lbl := tb.StorageLabel(); lbl != "" {
				memberLabel += " + " + lbl
			}
			fmt.Fprintf(&b, "  %s · runtime %s\n", memberLabel, tb.RuntimeStatus)
		} else {
			fmt.Fprintf(&b, "  %s NOT included\n", tb.Member)
		}
	} else {
		fmt.Fprintf(&b, "in-game title branding: Disabled\n")
	}
	if c.TranslationsRecords > 0 {
		fmt.Fprintf(&b, "components: %d translation record(s) · %s · executable %d record(s)\n",
			c.TranslationsRecords, hashutil.Short(c.TranslationsSHA256), c.ExecutablePatchRecs)
	}
	for _, r := range c.AddedResources {
		if r.Present {
			fmt.Fprintf(&b, "  added %s (%s)\n", r.Member, r.StateLabel())
			continue
		}
		fmt.Fprintf(&b, "  withheld %s (%s)\n", r.Member, r.StateLabel())
	}
	return b.String()
}

// titleBrandingEnabled reports the effective title-branding selection from the
// persisted settings (a missing key is off).
func (m *model) titleBrandingEnabled() bool { return m.svc.Cfg.TitleBrandingEnabled() }

// titleBrandingValue is the human label of the effective selection.
func (m *model) titleBrandingValue() string {
	if m.titleBrandingEnabled() {
		return "Enabled"
	}
	return "Disabled"
}

// titleRuntimeLabel states whether the composed member is the historical
// runtime-confirmed reference composition, the shipped but unrun configuration,
// or a user-modified, runtime-unverified one.
func titleRuntimeLabel(tb app.TitleBrandingStatus) string {
	if tb.RuntimeVerified {
		return "confirmed (reference composition, Eden 2026-10-19)"
	}
	switch tb.Composition {
	case app.CompositionShipped:
		return "UNVERIFIED (shipped face/style, no game run)"
	case app.CompositionModified:
		return "UNVERIFIED (modified face/style)"
	default:
		return "UNVERIFIED"
	}
}

// titleBrandingLines renders the Build screen's title-branding panel: the
// effective selection, the footer label read from version.txt (never invented)
// and, when enabled, the runtime-unverified warning.
func (m *model) titleBrandingLines() []string {
	out := []string{kvLine("in-game title branding", m.titleBrandingValue())}
	if !m.titleBrandingEnabled() {
		out = append(out, kvLine("title footer", styleDim.Render("not included")))
		out = append(out, styleDim.Render("T enables it · enter runs the build"))
		return out
	}
	if v, err := m.svc.ModVersion(); err == nil {
		out = append(out, kvLine("title footer", modversion.Display(v)))
	} else {
		out = append(out, kvLine("title footer", styleWarn.Render(modversion.Display(""))+"  "+styleDim.Render(err.Error())))
	}
	if !m.busy {
		if _, _, terr := m.svc.ResolveTitleBase(); terr != nil {
			out = append(out, kvLine("base atlas", styleBad.Render(terr.Error())))
		}
		if assets, aerr := m.svc.LoadTitleAssets(); aerr != nil {
			out = append(out, kvLine("font/style", styleBad.Render(aerr.Error())))
		} else if assets.RuntimeConfirmed() {
			out = append(out, kvLine("runtime", styleOK.Render("confirmed (reference composition)")))
		} else if assets.ShippedIdentity() {
			out = append(out, kvLine("runtime", styleWarn.Render("UNVERIFIED (shipped face/style)")))
		} else {
			out = append(out, kvLine("runtime", styleWarn.Render("UNVERIFIED (modified face/style)")))
		}
	}
	out = append(out, styleDim.Render("T disables it · enter runs the build"))
	return out
}

// onBrandingScreen reports whether the focused pane shows the title-branding
// panel (the build-family panes).
func (m *model) onBrandingScreen() bool {
	switch m.menu[m.focus].mode {
	case viewPreflight, viewBuild, viewValidate, viewInspect:
		return true
	}
	return false
}

// toggleTitleBranding changes the selection from the Build screen. Enabling for
// the first time in this session arms the confirm-once warning; disabling is
// immediate.
func (m *model) toggleTitleBranding() {
	if m.titleBrandingEnabled() {
		m.applyTitleBranding(false)
		m.pushLog("in-game title branding disabled")
		return
	}
	if m.titleAck {
		m.applyTitleBranding(true)
		m.pushLog("in-game title branding enabled")
		return
	}
	m.titleConfirm = true
	m.refreshContent()
}

// applyTitleBranding persists the selection explicitly and keeps the Settings
// pane in sync.
func (m *model) applyTitleBranding(on bool) {
	v := on
	m.svc.Cfg.TitleBranding = &v
	if err := m.svc.Save(); err != nil {
		m.pushLog("settings: " + err.Error())
	} else {
		m.pushLog("settings saved to " + m.svc.CfgPath)
	}
	if m.settings != nil {
		m.settings.titleBranding = on
	}
	m.refreshContent()
}

// titleConfirmView renders the one-time warning shown before the in-game title
// footer is re-enabled. The warning is wrapped so the panel
// never truncates it.
func (m *model) titleConfirmView(width int) string {
	inner := width - 4
	if inner < 20 {
		inner = 20
	}
	var b strings.Builder
	for _, l := range wrapText(TitleBrandingWarning, inner) {
		b.WriteString(styleWarn.Render(l) + "\n")
	}
	b.WriteString("\n")
	label := modversion.Display("")
	if v, err := m.svc.ModVersion(); err == nil {
		label = modversion.Display(v)
	} else {
		b.WriteString(styleDim.Render("version.txt: "+err.Error()) + "\n")
	}
	fmt.Fprintf(&b, "Enabling adds %s to the built archive and renders:\n", texpackMember())
	fmt.Fprintf(&b, "  %s\n\n", label)
	b.WriteString(styleDim.Render("enter confirm · esc cancel (stays Disabled)") + "\n")
	return b.String()
}

// texpackMember names the archive member the title branding replaces.
func texpackMember() string { return "GUI/title.ftx" }

// ---- update ----

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return m, nil
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		m.spinTick++
		if m.busy {
			return m, cmd
		}
		return m, nil
	case statusLoadedMsg:
		m.status, m.statusErr = msg.st, msg.err
		m.reloadModVersion()
		if msg.err != nil {
			m.pushLog("status: " + msg.err.Error())
		}
		m.refreshContent()
		return m, nil
	case logLineMsg:
		m.pushLog(string(msg))
		return m, m.rearm()
	case progressMsg:
		m.applyProgress(progress.Event(msg))
		return m, m.rearm()
	case destinationsMsg:
		if msg.err != nil {
			m.pushLog("inspect outputs: " + msg.err.Error())
			m.approvedOver = true
			return m, m.startTask(msg.taskID)
		}
		if msg.taskID != "" {
			m.modalTask = msg.taskID
		}
		any := false
		for _, st := range msg.states {
			if st.Exists {
				any = true
			}
		}
		if !any {
			m.approvedOver = true
			return m, m.startTask(msg.taskID)
		}
		m.dests = msg.states
		m.modal = true
		m.choice = choiceCancel
		m.refreshContent()
		return m, nil
	case taskDoneMsg:
		m.busy = false
		m.cancel = nil
		m.prog.running = false
		m.approvedOver = false
		if msg.err != nil {
			m.taskErr = msg.err
			m.pushLog("!! " + stripForceHint(msg.err.Error()))
		}
		m.taskSummary = msg.summary
		m.refreshContent()
		m.content.GotoTop()
		return m, m.loadStatus()
	case fileListMsg:
		m.files = msg.entries
		if msg.err != nil {
			m.pushLog("files: " + msg.err.Error())
		}
		return m, nil
	case fileContentMsg:
		m.fileText, m.filePath = msg.text, msg.path
		m.refreshContent()
		return m, nil
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	if m.showSettings() && m.settings.editing() {
		var cmd tea.Cmd
		*m.settings, cmd = m.settings.Update(msg)
		return m, cmd
	}
	if !m.busy {
		var cmd tea.Cmd
		m.content, cmd = m.content.Update(msg)
		return m, cmd
	}
	return m, nil
}

// applyProgress folds a progress event into the dashboard state.
func (m *model) applyProgress(ev progress.Event) {
	m.prog.running = true
	if ev.Op != "" {
		m.prog.op = ev.Op
	}
	if ev.Stage != "" {
		m.prog.stage = ev.Stage
	}
	if ev.Item != "" {
		m.prog.item = ev.Item
	}
	if ev.Note != "" {
		m.prog.note = ev.Note
	}
	m.prog.status = ev.Status
	m.prog.completed, m.prog.total = ev.Completed, ev.Total
	m.prog.bytes = ev.Bytes
	m.prog.stageFrac = ev.Fraction()
	m.prog.overall = ev.Overall()
	if ev.Status == progress.StatusDone {
		m.prog.done[ev.Stage] = true
	}
	if ev.Warning != "" {
		m.prog.warning = ev.Warning
		m.pushLog("warn: " + ev.Warning)
	}
	if ev.Error != "" {
		m.pushLog("!! " + stripForceHint(ev.Error))
	}
	m.refreshContent()
}

func (m *model) showSettings() bool { return m.menu[m.focus].mode == viewSettings }
func (m *model) showLogs() bool     { return m.menu[m.focus].mode == viewLogs }

// ---- modal ----

func (m *model) handleModalKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc":
		m.modal = false
		m.pushLog("replace cancelled — existing outputs left untouched")
		m.refreshContent()
		return m, nil
	case "left", "shift+tab", "h":
		m.choice = (m.choice + 2) % 3
	case "right", "tab", "l":
		m.choice = (m.choice + 1) % 3
	case "r":
		m.choice = choiceReplace
	case "k":
		m.choice = choiceKeep
	case "c":
		m.choice = choiceCancel
	case "enter":
		return m.activateChoice()
	}
	m.refreshContent()
	return m, nil
}

func (m *model) activateChoice() (tea.Model, tea.Cmd) {
	choice := m.choice
	m.modal = false
	switch choice {
	case choiceReplace:
		m.approvedOver = true
		m.pushLog("replace approved — existing outputs will be overwritten")
		return m, m.startTask(m.modalTask)
	case choiceKeep:
		m.pushLog("keep approved — reusing valid outputs and building only what is missing")
		return m, m.startTask("keep")
	default:
		m.pushLog("cancelled — existing outputs left untouched")
		m.refreshContent()
		return m, nil
	}
}

func (m *model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if m.titleConfirm {
		switch key {
		case "enter":
			m.titleConfirm = false
			m.titleAck = true
			m.applyTitleBranding(true)
			m.pushLog("in-game title branding enabled")
		case "esc":
			m.titleConfirm = false
			m.pushLog("title branding stays Disabled")
			m.refreshContent()
		}
		return m, nil
	}
	if m.modal {
		return m.handleModalKey(key)
	}
	if m.showSettings() {
		if key == "ctrl+c" {
			return m.quitNow()
		}
		handled, cmd := m.settings.UpdateKey(msg)
		if handled {
			if m.settings.saved {
				m.pushLog("settings saved to " + m.svc.CfgPath)
				m.settings.saved = false
				m.target = m.svc.Target()
				m.refreshContent()
				return m, m.loadStatus()
			}
			return m, cmd
		}
	}
	if m.logFullscreen {
		switch key {
		case "up", "k":
			m.logView.ScrollUp(1)
		case "down", "j":
			m.logView.ScrollDown(1)
		case "pgup", "ctrl+u":
			m.logView.PageUp()
		case "pgdown", "ctrl+d":
			m.logView.PageDown()
		case "home":
			m.logView.GotoTop()
		case "end":
			m.logView.GotoBottom()
		case "L":
			m.toggleLogFull()
		case "esc":
			m.toggleLogFull()
		case "q", "ctrl+c":
			return m.quitNow()
		}
		return m, nil
	}
	switch key {
	case "ctrl+c", "q":
		if m.busy {
			m.cancel()
			return m, nil
		}
		return m.quitNow()
	case "esc":
		// a running task: esc cancels it (a distinct action from "back")
		if m.busy && m.cancel != nil {
			m.cancel()
			m.pushLog("cancel requested — no partial artifact is written")
			return m, nil
		}
		// idle: esc leaves the current pane and returns to the overview
		if m.menu[m.focus].mode != viewOverview {
			m.setFocusByMode(viewOverview)
			m.pushLog("back to overview")
			return m, m.onFocusChanged()
		}
		return m, nil
	case "?":
		m.showHelp = !m.showHelp
		m.refreshContent()
		return m, nil
	case "r":
		if m.busy {
			return m, nil
		}
		m.pushLog("refreshing status…")
		return m, m.loadStatus()
	case "T":
		if m.busy || m.showSettings() || !m.onBrandingScreen() {
			return m, nil
		}
		m.toggleTitleBranding()
		return m, nil
	case "s":
		if m.busy {
			return m, nil
		}
		return m, m.startTask("setup")
	case "L":
		m.toggleLogFull()
		return m, nil
	case "1", "2", "3":
		if m.busy {
			return m, nil
		}
		switch key {
		case "1":
			m.target = packaging.TargetAtmosphere
		case "2":
			m.target = packaging.TargetEmulator
		case "3":
			m.target = packaging.TargetBoth
		}
		m.pushLog("target: " + m.target.Label())
		m.refreshContent()
		return m, nil
	case "tab":
		m.focus = (m.focus + 1) % len(m.menu)
		return m, m.onFocusChanged()
	case "shift+tab":
		m.focus = (m.focus - 1 + len(m.menu)) % len(m.menu)
		return m, m.onFocusChanged()
	case "up", "k":
		if m.busy {
			return m, nil
		}
		if m.showLogs() && m.fileIdx > 0 {
			m.fileIdx--
			m.refreshContent()
			return m, nil
		}
		m.focus = (m.focus - 1 + len(m.menu)) % len(m.menu)
		return m, m.onFocusChanged()
	case "down", "j":
		if m.busy {
			return m, nil
		}
		if m.showLogs() && m.fileIdx < len(m.files)-1 {
			m.fileIdx++
			m.refreshContent()
			return m, nil
		}
		m.focus = (m.focus + 1) % len(m.menu)
		return m, m.onFocusChanged()
	case "enter":
		if m.busy {
			return m, nil
		}
		it := m.menu[m.focus]
		switch it.id {
		case "logs":
			return m, m.readSelectedFile()
		case "settings":
			return m, m.settings.EditFocused()
		default:
			if it.kind == "task" {
				return m, m.beginGuardedTask(it.id)
			}
		}
		return m, nil
	case "pgup", "ctrl+u":
		m.logView.PageUp()
		return m, nil
	case "pgdown", "ctrl+d":
		m.logView.PageDown()
		return m, nil
	}
	return m, nil
}

func (m *model) toggleLogFull() {
	m.logFullscreen = !m.logFullscreen
	m.layout()
	m.refreshContent()
}

func (m *model) quitNow() (tea.Model, tea.Cmd) {
	if m.busy && m.cancel != nil {
		m.cancel()
	}
	if m.settings != nil {
		m.settings.stopEditing()
	}
	m.quit = true
	return m, tea.Quit
}

func (m *model) setFocusByMode(mode viewMode) {
	for i, it := range m.menu {
		if it.mode == mode {
			m.focus = i
			return
		}
	}
}

func (m *model) onFocusChanged() tea.Cmd {
	m.refreshContent()
	if m.showLogs() && len(m.files) == 0 {
		return m.loadFiles()
	}
	return nil
}

func (m *model) loadFiles() tea.Cmd {
	svc := m.svc
	return func() tea.Msg {
		var out []reportFile
		for _, dir := range []string{filepath.Join(svc.WorkDir, "reports"), svc.OutputDirOrDefault(), svc.ReportsDir()} {
			ents, err := os.ReadDir(dir)
			if err != nil {
				continue
			}
			for _, e := range ents {
				if e.IsDir() {
					continue
				}
				fi, err := e.Info()
				if err != nil {
					continue
				}
				out = append(out, reportFile{Path: filepath.Join(dir, e.Name()), Size: fi.Size(), Mod: fi.ModTime()})
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Mod.After(out[j].Mod) })
		if len(out) > 200 {
			out = out[:200]
		}
		return fileListMsg{entries: out}
	}
}

func (m *model) readSelectedFile() tea.Cmd {
	if len(m.files) == 0 {
		return m.loadFiles()
	}
	p := m.files[m.fileIdx].Path
	return func() tea.Msg {
		raw, err := os.ReadFile(p)
		if err != nil {
			return fileContentMsg{path: p, err: err}
		}
		const maxBytes = 256 * 1024
		trunc := ""
		if len(raw) > maxBytes {
			raw = raw[len(raw)-maxBytes:]
			trunc = fmt.Sprintf("(showing the last %d bytes)\n", maxBytes)
		}
		return fileContentMsg{path: p, text: trunc + strings.ToValidUTF8(string(raw), "?")}
	}
}

// ---- view ----

func (m *model) View() tea.View {
	body := m.renderBody()
	v := tea.NewView(body)
	v.AltScreen = true
	v.WindowTitle = "13s-patcher — 13 Sentinels Unburger Patch Builder"
	return v
}

// layout resolves the actual terminal height into a header, a body, a log and
// a help line whose total never exceeds the terminal. The log keeps about a
// quarter of the remaining space during a task.
func (m *model) layout() {
	w, h := m.width, m.height
	if w < 1 {
		w = 80
	}
	if h < 1 {
		h = 24
	}
	const headerH, helpH = 2, 1
	avail := h - headerH - helpH
	if avail < 1 {
		avail = 1
	}
	if m.logFullscreen {
		m.bodyOuter = 0
		m.logOuter = avail
	} else {
		logOuter := avail / 4
		if logOuter < 3 {
			logOuter = 3
		}
		maxLog := avail - 3
		if maxLog < 1 {
			maxLog = 1
		}
		if logOuter > maxLog {
			logOuter = maxLog
		}
		bodyOuter := avail - logOuter
		if bodyOuter < 1 {
			bodyOuter = 1
			logOuter = avail - 1
			if logOuter < 0 {
				logOuter = 0
			}
		}
		m.bodyOuter = bodyOuter
		m.logOuter = logOuter
	}
	m.sideWidth = 26
	if w < 72 {
		m.sideWidth = 20
	}
	contentW := maxInt(16, w-m.sideWidth-3)
	m.content.SetWidth(contentW)
	m.content.SetHeight(maxInt(1, m.bodyOuter-3))
	m.logView.SetWidth(maxInt(16, w-3))
	m.logView.SetHeight(maxInt(1, m.logOuter-3))
}

func (m *model) renderBody() string {
	w := m.width
	if w < 40 {
		w = 40
	}
	parts := []string{fitHeight(m.renderHeader(w), 2)}
	if m.bodyOuter > 0 {
		side := m.renderSidebar(m.sideWidth)
		content := m.renderContent(maxInt(20, w-m.sideWidth-3))
		body := lipgloss.JoinHorizontal(lipgloss.Top, fitHeight(side, m.bodyOuter), fitHeight(content, m.bodyOuter))
		parts = append(parts, fitHeight(body, m.bodyOuter))
	}
	logTitle := "log"
	if m.logFullscreen {
		logTitle = "log (full screen — ↑/↓ PgUp/PgDn Home/End scroll · L or esc closes)"
	}
	logs := panel(logTitle, strings.Split(m.logView.View(), "\n"), w, false)
	parts = append(parts, fitHeight(logs, m.logOuter))
	parts = append(parts, fitHeight(m.renderHelp(w), 1))
	return stack(parts)
}

// fitHeight forces a rendered block to exactly n lines: it pads with blank
// lines or truncates.
func fitHeight(s string, n int) string {
	if n <= 0 {
		return ""
	}
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for len(lines) < n {
		lines = append(lines, "")
	}
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

func (m *model) renderHeader(w int) string {
	line := styleTitle.Render("13s-patcher") + styleDim.Render(" · 13 Sentinels Unburger Patch Builder")
	line2 := ""
	switch {
	case m.status != nil:
		line2 = styleDim.Render(m.status.WorkDir)
	case m.statusErr != nil:
		line2 = styleBad.Render("status error: " + m.statusErr.Error())
	default:
		line2 = styleDim.Render("loading…")
	}
	if m.busy {
		line2 += styleWarn.Render("   " + m.taskTitle + " … (Esc cancels)")
	} else if m.prog.running {
		line2 += styleWarn.Render("   " + m.taskTitle + " …")
	}
	line2 += styleKey.Render("   target: " + m.target.Label())
	return styleHeader.Width(w).Render(line + "\n" + line2)
}

func (m *model) renderSidebar(width int) string {
	var lines []string
	for i, it := range m.menu {
		cursor := "  "
		label := it.title
		if i == m.focus {
			cursor = "> "
			label = styleSel.Render(padRight(label, width-6))
		}
		kind := styleDim.Render("view")
		if it.kind == "task" {
			kind = styleKey.Render("run ")
		}
		lines = append(lines, cursor+label+" "+kind)
	}
	// Branding sits beneath the menu, as the sidebar footer. A missing or
	// invalid version.txt shows a diagnostic instead of a fabricated number.
	lines = append(lines, "")
	if m.modVersionErr != nil {
		lines = append(lines, styleWarn.Render(modversion.Display("")))
		lines = append(lines, styleDim.Render("check version.txt"))
	} else {
		lines = append(lines, styleTitle.Render(modversion.Display(m.modVersion)))
	}
	return panel("actions", lines, width, true)
}

func (m *model) renderContent(width int) string {
	title := m.menu[m.focus].title
	if m.modal {
		return panel("existing output found", strings.Split(m.modalView(width), "\n"), width, true)
	}
	if m.titleConfirm {
		return panel("confirm title branding", strings.Split(m.titleConfirmView(width), "\n"), width, true)
	}
	if m.showSettings() {
		return panel(title, strings.Split(m.settings.View(width), "\n"), width, true)
	}
	if m.showHelp {
		return panel("help", strings.Split(helpText(), "\n"), width, true)
	}
	return panel(title, strings.Split(m.content.View(), "\n"), width, true)
}

// modalView renders the overwrite dialog body.
func (m *model) modalView(width int) string {
	var b strings.Builder
	b.WriteString(styleWarn.Render("One or more output archives already exist.") + "\n\n")
	for _, st := range m.dests {
		state := string(st.Kind)
		switch st.Kind {
		case app.DestIdentical:
			state = styleOK.Render("identical · valid")
		case app.DestUnverifiable:
			state = styleBad.Render("unverifiable")
		case app.DestDifferent:
			state = styleWarn.Render("different")
		}
		b.WriteString(fmt.Sprintf("%-10s %s\n", st.Label, filepath.Base(st.Path)))
		b.WriteString(fmt.Sprintf("           %s bytes · sha256 %s · %s\n", fmtSize(st.Size), hashutil.Short(st.SHA256), state))
		if st.Detail != "" {
			b.WriteString(styleDim.Render("           "+st.Detail) + "\n")
		}
	}
	b.WriteString("\n")
	options := []struct {
		c    overwriteChoice
		text string
	}{
		{choiceReplace, "Replace existing"},
		{choiceKeep, "Keep existing (reuse valid, build missing)"},
		{choiceCancel, "Cancel"},
	}
	var parts []string
	for _, o := range options {
		txt := o.text
		if m.choice == o.c {
			txt = styleSel.Render(" " + txt + " ")
		} else {
			txt = styleDim.Render(txt)
		}
		parts = append(parts, txt)
	}
	b.WriteString(strings.Join(parts, "   ") + "\n\n")
	b.WriteString(styleDim.Render("enter selects · ←/→ or tab switches · r/k/c jump · esc cancels") + "\n")
	b.WriteString(styleDim.Render("nothing is overwritten until you choose; the default is Cancel") + "\n")
	return b.String()
}

func (m *model) renderHelp(w int) string {
	keys := m.helpKeys()
	line := strings.Join(keys, "  ·  ")
	for len(keys) > 2 && lipgloss.Width(line) > w {
		keys = keys[:len(keys)-1]
		line = strings.Join(keys, "  ·  ")
	}
	return styleHelp.MaxWidth(w).Render(line)
}

func (m *model) helpKeys() []string {
	if m.titleConfirm {
		return []string{"enter confirm title branding", "esc cancel"}
	}
	if m.modal {
		return []string{"←/→ switch", "enter select", "esc cancel"}
	}
	if m.busy {
		return []string{"esc cancel task", "L log", "? help", "q quit"}
	}
	return []string{"? help", "↑/↓ move", "enter run/open", "1/2/3 target", "T in-game title branding", "L log", "s setup", "r refresh", "q quit"}
}

func (m *model) refreshContent() {
	if m.status == nil {
		m.content.SetContent(styleDim.Render("loading…"))
		return
	}
	r := m.status
	width := maxInt(30, m.width-m.sideWidth-3)
	var b strings.Builder
	render := func(title string, lines []string, active bool) {
		b.WriteString(panel(title, lines, width, active))
		b.WriteString("\n")
	}
	switch m.menu[m.focus].mode {
	case viewOverview:
		render("Unburger Mod", modPanel(r), r.ModVersionError != "")
		render("inputs", inputPanel(r), true)
		render("target", targetPanel(m.target, r), true)
		if setupNeeded(r) {
			render("first run", []string{styleWarn.Render("some inputs are not prepared yet — press s to run setup (or use the Settings pane)")}, true)
		}
		if r.PatchData.Loaded {
			render("patch data (explicit)", patchPanel(r), false)
		}
		render("outputs", outputPanel(r), false)
		if len(r.Warnings) > 0 {
			render("warnings", warningPanel(r), true)
		}
	case viewPreflight, viewBuild, viewValidate, viewInspect:
		render("progress", m.progressLines(), m.busy)
		render("title branding", m.titleBrandingLines(), m.titleBrandingEnabled())
		if m.taskErr != nil {
			render("result", append([]string{styleBad.Render("failed: " + stripForceHint(m.taskErr.Error()))}, recoveryHints(m.taskErr)...), true)
		} else if m.taskSummary != "" {
			render("result", strings.Split(strings.TrimRight(m.taskSummary, "\n"), "\n"), true)
		} else if !m.busy {
			render("result", []string{styleDim.Render("press enter to run " + m.menu[m.focus].title + " for target " + m.target.Label())}, false)
		}
	case viewLogs:
		var lines []string
		for i, f := range m.files {
			cursor := "  "
			if i == m.fileIdx {
				cursor = "> "
			}
			lines = append(lines, fmt.Sprintf("%s%-40s %10s %s", cursor, filepath.Base(f.Path), fmtSize(f.Size), f.Mod.Format("01-02 15:04")))
		}
		if len(lines) == 0 {
			lines = append(lines, styleDim.Render("no reports yet"))
		}
		b.WriteString(panel("reports", lines, width, false) + "\n")
		if m.fileText != "" {
			b.WriteString(styleTitle.Render(m.filePath) + "\n")
			b.WriteString(m.fileText)
		}
	case viewSettings:
		b.WriteString(styleDim.Render("enter edits a field · space toggles in-game title branding · esc stops editing · esc again returns to the overview · ctrl+s saves") + "\n")
	}
	m.content.SetContent(b.String())
}

// progressLines renders the per-stage and overall progress.
func (m *model) progressLines() []string {
	var out []string
	if !m.prog.running && m.prog.stage == "" {
		out = append(out, styleDim.Render("idle — no task has run yet"))
		return out
	}
	stageName := ""
	if m.prog.stage != "" {
		stageName = m.prog.stage.Label()
	}
	indeterminate := m.prog.total <= 0
	barW := 28
	stageBar := renderBar(barW, m.prog.stageFrac, indeterminate, m.spinTick)
	stageCount := ""
	if !indeterminate {
		stageCount = fmt.Sprintf(" %d/%d", m.prog.completed, m.prog.total)
	} else if m.busy {
		stageCount = " " + m.spin.View()
	}
	out = append(out, styleDim.Render(padRight("stage", 9))+" "+styleText.Render(stageName)+"  "+stageBar+styleDim.Render(stageCount))
	out = append(out, styleDim.Render(padRight("overall", 9))+" "+renderBar(barW, m.prog.overall, false, 0)+percentLabel(m.prog.overall))
	if m.prog.item != "" {
		out = append(out, styleDim.Render(padRight("item", 9))+" "+styleText.Render(m.prog.item))
	}
	if m.prog.note != "" {
		out = append(out, styleDim.Render(padRight("step", 9))+" "+styleDim.Render(m.prog.note))
	}
	elapsed := time.Duration(0)
	if m.busy {
		elapsed = time.Since(m.prog.start)
	}
	stats := fmt.Sprintf("%s elapsed", elapsed.Round(time.Millisecond))
	if m.prog.bytes > 0 {
		stats += " · " + fmtSize(m.prog.bytes) + " written"
	}
	out = append(out, styleDim.Render(padRight("timing", 9))+" "+styleDim.Render(stats))
	for _, p := range m.svc.DestinationArchives(m.target) {
		out = append(out, styleDim.Render(padRight("output", 9))+" "+styleDim.Render(p))
	}
	if m.prog.warning != "" {
		out = append(out, styleWarn.Render("warning   "+m.prog.warning))
	}
	return out
}

func setupNeeded(r *app.Status) bool {
	return r.Translations.Error != "" || !r.Cache.Ready
}

func targetPanel(t packaging.Target, r *app.Status) []string {
	out := []string{
		kvLine("target", t.Label()),
		kvLine("keys", "1 = Atmosphère · 2 = Emulators · 3 = both"),
	}
	for _, a := range r.Outputs {
		out = append(out, kvLine(a.Label, filepath.Base(a.Path)+" · "+fmtSize(a.Size)))
	}
	if len(r.Outputs) == 0 {
		out = append(out, kvLine("archives", "none yet"))
	}
	return out
}

func inputPanel(r *app.Status) []string {
	out := []string{kvLine("mode", r.Mode)}
	if r.ProjectRoot != "" {
		out = append(out, kvLine("project root", r.ProjectRoot))
	}
	if r.Translations.Loaded {
		out = append(out, kvLine("translations", fmt.Sprintf("%d record(s) in %d bank(s) · %s", r.Translations.Records, r.Translations.Banks, hashutil.Short(r.Translations.ChangesSHA256))))
		out = append(out, kvLine("schema", fmt.Sprintf("%s · %s", r.Translations.Schema, r.Translations.Dir)))
	} else {
		out = append(out, kvLine("translations", styleBad.Render(r.Translations.Error)))
	}
	if r.Cache.Ready {
		out = append(out, kvLine("cache", fmt.Sprintf("%d base bank(s) · overlay %s", r.Cache.BankCount, hashutil.Short(r.Cache.OverlaySHA))))
	} else {
		out = append(out, kvLine("cache", styleWarn.Render("not prepared")+"  "+r.Cache.Dir))
	}
	if r.Sources != nil {
		identified := 0
		for _, a := range r.Sources.Found {
			if a.Identified {
				identified++
			}
		}
		line := fmt.Sprintf("%d identified", identified)
		if len(r.Sources.Missing) > 0 {
			line += styleWarn.Render(fmt.Sprintf(" · missing %v", r.Sources.Missing))
		}
		out = append(out, kvLine("containers", line))
	}
	out = append(out, kvLine("keys", fmt.Sprintf("%d present (contents never read)", len(r.Keys.Present))))
	if r.Source.Path != "" && r.Source.Supported {
		out = append(out, kvLine("overlay", hashutil.Short(r.Source.Identity.SHA256)))
	}
	return out
}

func patchPanel(r *app.Status) []string {
	out := []string{}
	if r.PatchData.Loaded {
		out = append(out, kvLine("bundle", r.PatchData.BundleID+" "+r.PatchData.BundleVersion))
		out = append(out, kvLine("release", r.PatchData.ReleaseVersion))
		out = append(out, kvLine("operations", fmt.Sprintf("%d", r.PatchData.Operations)))
		out = append(out, kvLine("manifest", hashutil.Short(r.PatchData.ManifestSHA256)))
	} else {
		msg := "not configured"
		if r.PatchData.Error != "" {
			msg = r.PatchData.Error
		}
		out = append(out, kvLine("bundle", styleBad.Render(msg)))
	}
	return out
}

// modPanel renders the Unburger Mod branding in the overview. When version.txt
// cannot be read it shows the diagnostic instead of a fabricated version.
func modPanel(r *app.Status) []string {
	if r.ModVersionError != "" {
		return []string{
			styleWarn.Render("version unavailable — no version is shown"),
			styleDim.Render(r.ModVersionError),
		}
	}
	return []string{styleText.Render(modversion.Display(r.ModVersion))}
}

func outputPanel(r *app.Status) []string {
	out := []string{kvLine("output dir", r.OutputDir)}
	for _, a := range r.Outputs {
		out = append(out, kvLine(a.Label, filepath.Base(a.Path)+" · "+fmtSize(a.Size)+" · "+hashutil.Short(a.SHA256)))
	}
	if len(r.Outputs) == 0 {
		out = append(out, kvLine("archives", "none yet — the first build will create them"))
	}
	if r.LastBuild != nil {
		out = append(out, kvLine("last build", filepath.Base(r.LastBuild.OutputPath)+" · "+hashutil.Short(r.LastBuild.OutputSHA256)+" · verified="+boolWord(r.LastBuild.Verified)))
	} else {
		out = append(out, kvLine("last build", "none"))
	}
	if r.LastValidation != "" {
		out = append(out, kvLine("validation", filepath.Base(r.LastValidation)))
	} else {
		out = append(out, kvLine("validation", "not run"))
	}
	out = append(out, kvLine("intermediate", r.OutputPath))
	return out
}

func warningPanel(r *app.Status) []string {
	out := []string{}
	for _, wl := range r.Warnings {
		out = append(out, styleWarn.Render("• ")+styleText.Render(wl))
	}
	return out
}

// stripForceHint removes the CLI-only "--force" instruction from an error before
// it is shown in the dashboard, which has no force key.
func stripForceHint(s string) string {
	for _, suffix := range []string{
		" (pass --force to replace)",
		" (replace from the dashboard, or pass --force on the CLI)",
	} {
		s = strings.ReplaceAll(s, suffix, "")
	}
	return s
}

// recoveryHints turns a failure into one or two concrete next steps.
func recoveryHints(err error) []string {
	if err == nil {
		return nil
	}
	msg := err.Error()
	var hint string
	switch {
	case strings.Contains(msg, "--force") || strings.Contains(msg, "refusing to overwrite"):
		hint = "an output archive already exists — choose Replace in the dialog, or pick another output directory"
	case strings.Contains(msg, "preflight failed"):
		hint = "run Preflight to see the failing step"
	case strings.Contains(msg, "extraction is required") || strings.Contains(msg, "verified build inputs"):
		hint = "supply the game containers and the key files in .local/keys/, or point the tool at an existing cache/extraction"
	case strings.Contains(msg, "translation dataset"):
		hint = "re-obtain the translations/ dataset; it is never reconstructed"
	case strings.Contains(msg, "container") || strings.Contains(msg, "local containers"):
		hint = "run setup to add the local containers, or set roms_base / roms_update in Settings"
	case strings.Contains(msg, "added resources"):
		hint = "restore patcher/assets/images/end_route_plate.png, or set an optional plate_base override"
	case strings.Contains(msg, "cancelled"):
		hint = "the run was cancelled; no partial artifact was written and the existing outputs are intact"
	}
	if hint == "" {
		hint = "see the log for the failing stage; press r to refresh, or run Preflight"
	}
	return []string{styleDim.Render("→ " + hint)}
}

func helpText() string {
	return strings.Join([]string{
		"keys — ↑/↓ move · enter open/run",
		"1/2/3 target · T in-game title branding · L expand the log",
		"  log: ↑/↓ PgUp/PgDn Home/End · esc",
		"s setup · r refresh · ? help · q quit",
		"esc cancels a running task, else back",
		"",
		"in-game title branding",
		"Enabled in normal builds. The build adds",
		"GUI/title.ftx with the footer",
		"Unburger Mod v<version.txt>, drawn from",
		"patcher/assets/fonts live. The",
		"2026-10-19 Eden run confirmed the",
		"reference face/style, not the shipped",
		"one; builds report UNVERIFIED until",
		"the shipped composition is run.",
		"The choice is saved to the settings file.",
		"",
		"overwriting",
		"an existing output opens a dialog:",
		"Replace · Keep existing · Cancel",
		"Keep existing reuses valid archives and",
		"builds only the missing ones; Cancel",
		"leaves everything untouched.",
		"There is no force key.",
		"",
		"the rebuilt archive is an intermediate in",
		"the work cache; the per-target archives",
		"are the primary outputs, written atomically",
		"to the output directory.",
	}, "\n")
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// fmtSize renders a byte count with a human unit.
func fmtSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGT"[exp])
}
