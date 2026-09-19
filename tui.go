package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/heihei0299/mihomo-manage/internal/manager"

	tea "github.com/charmbracelet/bubbletea"
)

type viewMode int

const (
	modeStatus viewMode = iota
	modeConfirmUninstall
	modeChooseVersion
	modeConfig
	modeSchedule
)

type configTab int

const (
	configTabSubscription configTab = iota
	configTabOverride
	configTabPreview
)

type action int

const (
	actNone action = iota
	actStart
	actStop
	actRestart
	actReload
	actInstall
	actUpgrade
	actUninstall
	actAutostartOn
	actAutostartOff
)

type actionDef struct {
	enabled func(*manager.Status) bool
}

var (
	installedStopped = func(s *manager.Status) bool { return s != nil && s.Installed && s.InstanceState == manager.Stopped }
	installedRunning = func(s *manager.Status) bool { return s != nil && s.Installed && s.InstanceState == manager.Running }
)

var actionRegistry = map[action]actionDef{
	actStart:   {enabled: installedStopped},
	actStop:    {enabled: installedRunning},
	actRestart: {enabled: installedRunning},
	actReload:  {enabled: installedRunning},
}

type model struct {
	ctx              context.Context
	control          manager.ServiceControl
	lifecycle        manager.LifecycleManager
	config           manager.ConfigManager
	schedule         manager.ScheduleManager
	status           *manager.Status
	statusErr        error
	configStatus     manager.ConfigApplyStatus
	configErr        error
	scheduleInterval time.Duration
	scheduleActive   bool
	scheduleErr      error
	ready            bool
	executing        action
	execResult       string
	actionErr        error
	phaseLabel       string
	phaseMsg         string

	mode           viewMode
	keepBackup     bool
	versions       []manager.VersionInfo
	versionsErr    error
	selectedIdx    int
	configTab      configTab
	previewContent string
}

type statusMsg struct {
	status           *manager.Status
	err              error
	configStatus     manager.ConfigApplyStatus
	configErr        error
	scheduleInterval time.Duration
	scheduleActive   bool
	scheduleErr      error
}

type actionDoneMsg struct {
	action action
	err    error
}

type progressMsg struct {
	phase   manager.InstallationPhase
	message string
	err     error
	nextCmd tea.Cmd
}

type versionsMsg struct {
	versions []manager.VersionInfo
	err      error
}

type configPreviewMsg struct {
	content string
	err     error
}

type subscriptionEditMsg struct {
	err error
}

type scheduleDoneMsg struct {
	interval time.Duration
	err      error
}

var schedulePresets = []time.Duration{0, time.Hour, 6 * time.Hour, 12 * time.Hour, 24 * time.Hour}

func editorCommand(editor, path string) (*exec.Cmd, error) {
	parts := strings.Fields(editor)
	if len(parts) == 0 {
		return nil, fmt.Errorf("editor is empty")
	}
	args := append(append([]string{}, parts[1:]...), path)
	return exec.Command(parts[0], args...), nil
}

func editSubscriptionCmd(cfg manager.ConfigManager, ctx context.Context) tea.Cmd {
	file, err := os.CreateTemp("", "mihomo-subscription-*")
	if err != nil {
		return func() tea.Msg { return subscriptionEditMsg{err: err} }
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		os.Remove(path)
		return func() tea.Msg { return subscriptionEditMsg{err: err} }
	}

	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	command, err := editorCommand(editor, path)
	if err != nil {
		os.Remove(path)
		return func() tea.Msg { return subscriptionEditMsg{err: err} }
	}
	return tea.ExecProcess(command, func(runErr error) tea.Msg {
		defer os.Remove(path)
		if runErr != nil {
			return subscriptionEditMsg{err: fmt.Errorf("editor failed: %w", runErr)}
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return subscriptionEditMsg{err: err}
		}
		if strings.TrimSpace(string(data)) == "" {
			return subscriptionEditMsg{err: fmt.Errorf("subscription source cannot be empty")}
		}
		return subscriptionEditMsg{err: cfg.SetSubscriptionSource(ctx, string(data))}
	})
}

func tuiContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func (m model) Init() tea.Cmd {
	return fetchStatusCmd(m.control, m.config, m.schedule, tuiContext(m.ctx))
}

func fetchStatusCmd(ctrl manager.ServiceControl, cfg manager.ConfigManager, schedule manager.ScheduleManager, ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		s, err := ctrl.Status(ctx)
		configStatus, configErr := cfg.LastConfigApply(ctx)
		var interval time.Duration
		var active bool
		var scheduleErr error
		if schedule != nil {
			interval, active, scheduleErr = schedule.ScheduleStatus(ctx)
		}
		return statusMsg{status: s, err: err, configStatus: configStatus, configErr: configErr, scheduleInterval: interval, scheduleActive: active, scheduleErr: scheduleErr}
	}
}

func fetchConfigPreview(cfg manager.ConfigManager, ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		s, err := cfg.PreviewConfig(ctx)
		return configPreviewMsg{content: s, err: err}
	}
}

func fetchVersionsCmd(lifecycle manager.LifecycleManager, ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		v, err := lifecycle.ListVersions(ctx)
		return versionsMsg{versions: v, err: err}
	}
}

func execActionCmd(ctrl manager.ServiceControl, lifecycle manager.LifecycleManager, a action, progressCh chan<- progressMsg, version string, keepBackup bool) tea.Cmd {
	return execActionCmdWithContext(context.Background(), ctrl, lifecycle, a, progressCh, version, keepBackup)
}

func execActionCmdWithContext(ctx context.Context, ctrl manager.ServiceControl, lifecycle manager.LifecycleManager, a action, progressCh chan<- progressMsg, version string, keepBackup bool) tea.Cmd {
	return func() tea.Msg {
		var err error
		switch a {
		case actStart:
			err = ctrl.Start(ctx)
		case actStop:
			err = ctrl.Stop(ctx)
		case actRestart:
			err = ctrl.Restart(ctx)
		case actReload:
			err = ctrl.Reload(ctx)
		case actInstall:
			err = lifecycle.Install(ctx, version, true, func(e manager.ProgressEvent) {
				if progressCh != nil {
					progressCh <- progressMsg{phase: e.Phase, message: e.Message, err: e.Error}
				}
			})
		case actUpgrade:
			err = lifecycle.Upgrade(ctx, version, func(e manager.ProgressEvent) {
				if progressCh != nil {
					progressCh <- progressMsg{phase: e.Phase, message: e.Message, err: e.Error}
				}
			})
		case actUninstall:
			err = lifecycle.Uninstall(ctx, keepBackup, func(e manager.ProgressEvent) {
				if progressCh != nil {
					progressCh <- progressMsg{phase: e.Phase, message: e.Message, err: e.Error}
				}
			})
		case actAutostartOn:
			err = ctrl.SetAutoStart(ctx, true)
		case actAutostartOff:
			err = ctrl.SetAutoStart(ctx, false)
		}
		if progressCh != nil {
			close(progressCh)
		}
		return actionDoneMsg{action: a, err: err}
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if m.executing != actNone {
			return m, nil
		}

		switch m.mode {
		case modeConfirmUninstall:
			return m.updateConfirmUninstall(msg)
		case modeChooseVersion:
			return m.updateChooseVersion(msg)
		case modeConfig:
			return m.updateConfigMode(msg)
		case modeSchedule:
			return m.updateScheduleMode(msg)
		}

		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "c":
			if isInstalled(m.status) {
				m.mode = modeConfig
				m.configTab = configTabSubscription
				m.previewContent = ""
				return m, fetchConfigPreview(m.config, tuiContext(m.ctx))
			}
		case "r":
			return m, fetchStatusCmd(m.control, m.config, m.schedule, tuiContext(m.ctx))
		case "1":
			if isActionAllowed(m.status, actStart) {
				return m.startAction(actStart, "latest")
			}
		case "2":
			if isActionAllowed(m.status, actStop) {
				return m.startAction(actStop, "")
			}
		case "3":
			if isActionAllowed(m.status, actRestart) {
				return m.startAction(actRestart, "")
			}
		case "4":
			if isActionAllowed(m.status, actReload) {
				return m.startAction(actReload, "")
			}
		case "5":
			if m.status != nil && m.status.Installed {
				m.mode = modeChooseVersion
				m.versions = nil
				m.versionsErr = nil
				m.selectedIdx = 0
				return m, fetchVersionsCmd(m.lifecycle, tuiContext(m.ctx))
			}
		case "i":
			if !isInstalled(m.status) {
				return m.startAction(actInstall, "latest")
			}
		case "a":
			if isInstalled(m.status) {
				if m.status.AutoStartEnabled {
					return m.startAction(actAutostartOff, "")
				}
				return m.startAction(actAutostartOn, "")
			}
		case "t":
			if isInstalled(m.status) {
				m.mode = modeSchedule
				m.selectedIdx = schedulePresetIndex(m.scheduleInterval, m.scheduleActive)
				return m, nil
			}
		case "u":
			if isInstalled(m.status) {
				m.mode = modeConfirmUninstall
				m.keepBackup = false
				return m, nil
			}
		}
		return m, nil

	case statusMsg:
		m.ready = true
		m.status = msg.status
		m.statusErr = msg.err
		m.configStatus = msg.configStatus
		m.configErr = msg.configErr
		m.scheduleInterval = msg.scheduleInterval
		m.scheduleActive = msg.scheduleActive
		m.scheduleErr = msg.scheduleErr
		return m, nil

	case versionsMsg:
		m.versionsErr = msg.err
		if msg.err == nil {
			m.versions = msg.versions
		} else {
			m.versions = nil
		}
		return m, nil

	case configPreviewMsg:
		if msg.err == nil {
			m.previewContent = msg.content
		}
		return m, nil

	case scheduleDoneMsg:
		m.mode = modeStatus
		m.execResult = ""
		m.actionErr = msg.err
		if msg.err != nil {
			m.execResult = "failed"
		} else {
			m.execResult = "success"
		}
		return m, fetchStatusCmd(m.control, m.config, m.schedule, tuiContext(m.ctx))

	case subscriptionEditMsg:
		m.execResult = ""
		m.actionErr = msg.err
		if msg.err != nil {
			m.execResult = "failed"
		} else {
			m.execResult = "success"
		}
		return m, fetchConfigPreview(m.config, tuiContext(m.ctx))

	case progressMsg:
		m.phaseLabel = msg.phase.String()
		m.phaseMsg = msg.message
		return m, msg.nextCmd

	case actionDoneMsg:
		m.executing = actNone
		m.mode = modeStatus
		if msg.err != nil {
			m.execResult = "failed"
			m.actionErr = msg.err
		} else {
			m.execResult = "success"
			m.actionErr = nil
		}
		return m, fetchStatusCmd(m.control, m.config, m.schedule, tuiContext(m.ctx))
	}
	return m, nil
}

func (m model) updateConfirmUninstall(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y":
		return m.startAction(actUninstall, "")
	case "n", "N", "q":
		m.mode = modeStatus
		return m, nil
	case "b", "B":
		m.keepBackup = !m.keepBackup
		return m, nil
	}
	return m, nil
}

func (m model) updateChooseVersion(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "down", "j":
		if m.selectedIdx < len(m.versions)-1 {
			m.selectedIdx++
		}
		return m, nil
	case "up", "k":
		if m.selectedIdx > 0 {
			m.selectedIdx--
		}
		return m, nil
	case "enter":
		if len(m.versions) > 0 {
			ver := m.versions[m.selectedIdx]
			return m.startAction(actUpgrade, ver.Tag)
		}
		return m, nil
	case "q", "esc":
		m.mode = modeStatus
		return m, nil
	}
	return m, nil
}

func schedulePresetLabel(interval time.Duration) string {
	if interval == 0 {
		return "off"
	}
	return interval.String()
}

func schedulePresetIndex(interval time.Duration, active bool) int {
	if !active {
		return 0
	}
	for i, preset := range schedulePresets {
		if preset == interval {
			return i
		}
	}
	return 0
}

func scheduleCommand(sched manager.ScheduleManager, ctx context.Context, interval time.Duration) tea.Cmd {
	return func() tea.Msg {
		var err error
		if interval == 0 {
			err = sched.StopSchedule(ctx)
		} else {
			err = sched.SetSchedule(ctx, interval)
		}
		return scheduleDoneMsg{interval: interval, err: err}
	}
}

func (m model) updateScheduleMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc":
		m.mode = modeStatus
		return m, nil
	case "up", "k":
		if m.selectedIdx > 0 {
			m.selectedIdx--
		}
	case "down", "j":
		if m.selectedIdx < len(schedulePresets)-1 {
			m.selectedIdx++
		}
	case "enter":
		return m, scheduleCommand(m.schedule, tuiContext(m.ctx), schedulePresets[m.selectedIdx])
	}
	return m, nil
}

func (m model) scheduleView() string {
	view := "Schedule subscription-update (enter to apply, q to cancel):\n\n"
	for i, preset := range schedulePresets {
		prefix := "  "
		if i == m.selectedIdx {
			prefix = "> "
		}
		view += fmt.Sprintf("%s%s\n", prefix, schedulePresetLabel(preset))
	}
	return view
}

func (m model) updateConfigMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc":
		m.mode = modeStatus
		return m, nil
	case "tab", "right":
		m.configTab = (m.configTab + 1) % 3
		return m, nil
	case "left":
		m.configTab = (m.configTab - 1 + 3) % 3
		return m, nil
	case "r":
		return m, fetchConfigPreview(m.config, tuiContext(m.ctx))
	case "e":
		if m.configTab == configTabSubscription {
			return m, editSubscriptionCmd(m.config, tuiContext(m.ctx))
		}
	}
	return m, nil
}

func (m model) startAction(a action, version string) (tea.Model, tea.Cmd) {
	m.executing = a
	m.mode = modeStatus
	ch := make(chan progressMsg, 20)
	return m, tea.Batch(
		progressReaderCmd(ch),
		execActionCmdWithContext(tuiContext(m.ctx), m.control, m.lifecycle, a, ch, version, m.keepBackup),
	)
}

func progressReaderCmd(ch <-chan progressMsg) tea.Cmd {
	return func() tea.Msg {
		e, ok := <-ch
		if !ok {
			return nil
		}
		e.nextCmd = progressReaderCmd(ch)
		return e
	}
}

func isInstalled(s *manager.Status) bool {
	return s != nil && s.Installed
}

func isActionAllowed(s *manager.Status, a action) bool {
	if s == nil {
		return false
	}
	if !s.Installed {
		return a == actInstall
	}
	if def, ok := actionRegistry[a]; ok {
		return def.enabled(s)
	}
	return false
}

func (m model) View() string {
	if !m.ready {
		return "Checking mihomo status...\n\nPress q to quit"
	}
	if m.statusErr != nil {
		return fmt.Sprintf("Error: %v\n\nPress q to quit", m.statusErr)
	}

	if m.executing != actNone {
		return m.executingView()
	}

	switch m.mode {
	case modeConfirmUninstall:
		return m.uninstallView()
	case modeChooseVersion:
		return m.versionChoiceView()
	case modeConfig:
		return m.configView()
	case modeSchedule:
		return m.scheduleView()
	}

	return m.statusView()
}

func (m model) executingView() string {
	label := "working..."
	switch m.executing {
	case actStart:
		label = "starting..."
	case actStop:
		label = "stopping..."
	case actRestart:
		label = "restarting..."
	case actReload:
		label = "reloading..."
	case actInstall:
		label = "installing..."
	case actUpgrade:
		label = "upgrading..."
	case actUninstall:
		label = "uninstalling..."
	case actAutostartOn:
		label = "enabling autostart..."
	case actAutostartOff:
		label = "disabling autostart..."
	}
	phase := ""
	if m.phaseMsg != "" {
		phase = fmt.Sprintf("\n[%s] %s", m.phaseLabel, m.phaseMsg)
	}
	return fmt.Sprintf("Status: %s%s\n\nPress r to refresh, q to quit", label, phase)
}

func (m model) uninstallView() string {
	backup := "no"
	if m.keepBackup {
		backup = "yes"
	}
	return fmt.Sprintf(
		"Uninstall mihomo?\n\n"+
			"Keep backup: %s  (b to toggle)\n\n"+
			"y) Confirm    n) Cancel",
		backup,
	)
}

func (m model) versionChoiceView() string {
	s := "Select version (enter to confirm, q to cancel):\n\n"
	if m.versionsErr != nil {
		return s + "Error loading versions: " + m.versionsErr.Error() + "\n"
	}
	if len(m.versions) == 0 {
		return s + "No releases available.\n"
	}
	for i, v := range m.versions {
		prefix := "  "
		if i == m.selectedIdx {
			prefix = "> "
		}
		s += fmt.Sprintf("%s%s\n", prefix, v.Tag)
	}
	return s
}

func (m model) statusView() string {
	s := m.status
	stateStr := s.InstanceState.String()
	version := s.Version
	if version == "" {
		version = "unknown"
	}

	autostart := "off"
	if s.AutoStartEnabled {
		autostart = "on"
	}

	var actions string
	if !s.Installed {
		actions = "\ni) Install"
	} else {
		actions += "\n1) Start"
		if !isActionAllowed(s, actStart) {
			actions += "  (already running)"
		}
		actions += "\n2) Stop"
		if !isActionAllowed(s, actStop) {
			actions += "  (not running)"
		}
		actions += "\n3) Restart"
		if !isActionAllowed(s, actRestart) {
			actions += "  (not running)"
		}
		actions += "\n4) Reload"
		if !isActionAllowed(s, actReload) {
			actions += "  (not running)"
		}
		actions += "\n5) Upgrade"
		actions += "\na) Autostart: " + autostart + "  (toggle)"
		actions += "\nt) Schedule"
		actions += "\nu) Uninstall"
	}

	result := ""
	if m.execResult == "success" {
		result = "\n✓ Operation succeeded"
	} else if m.execResult == "failed" {
		result = fmt.Sprintf("\n✗ %v", m.actionErr)
	}

	configState := string(m.configStatus.State)
	if configState == "" || m.configErr != nil {
		configState = "unknown"
	}
	scheduleState := "off"
	if m.scheduleErr != nil {
		var legacy manager.LegacyScheduleError
		if errors.As(m.scheduleErr, &legacy) {
			scheduleState = "legacy"
		} else {
			scheduleState = "unknown"
		}
	} else if m.scheduleActive {
		scheduleState = "every " + m.scheduleInterval.String()
	}
	return fmt.Sprintf(
		"┌────────────────────────────┐\n"+
			"│ mihomo: %-18s │\n"+
			"│ version: %-18s │\n"+
			"│ autostart: %-15s │\n"+
			"│ config: %-17s │\n"+
			"│ schedule: %-15s │\n"+
			"└────────────────────────────┘%s%s\n\n"+
			"r) Refresh    q) Quit",
		stateStr, version, autostart, configState, scheduleState, actions, result,
	)
}

func (m model) configView() string {
	tabs := []string{"Subscription", "Override", "Preview"}
	tabLine := ""
	for i, t := range tabs {
		sep := "  "
		if i == int(m.configTab) {
			tabLine += fmt.Sprintf("[%s]", t)
		} else {
			tabLine += fmt.Sprintf(" %s ", t)
		}
		if i < len(tabs)-1 {
			tabLine += sep
		}
	}
	tabLine += "\n\n"

	var content string
	switch m.configTab {
	case configTabSubscription:
		content = "Subscription source: remote or local\n"
		content += "e) Edit source with $EDITOR (URL or local subscription-data)\n"
		content += "CLI: mihomo-manager subscription set <url-or-data>\n"
	case configTabOverride:
		content = fmt.Sprintf("Override file: %s\n", manager.OverrideFilePath)
		content += "Edit with: mihomo-manager config override edit\n"
		content += "\nRules are embedded in the override file's 'rules:' field.\n"
	case configTabPreview:
		if m.previewContent == "" {
			content = "Loading preview...\n"
		} else if len(m.previewContent) > 1000 {
			content = m.previewContent[:1000] + "\n... (truncated)"
		} else {
			content = m.previewContent
		}
	}

	return tabLine + content + "\n\nTab/← → switch tab  r) refresh preview  q) back"
}

func startTUI(ctx context.Context, ctrl manager.ServiceControl, lifecycle manager.LifecycleManager, cfg manager.ConfigManager, schedule manager.ScheduleManager) error {
	p := tea.NewProgram(model{ctx: ctx, control: ctrl, lifecycle: lifecycle, config: cfg, schedule: schedule}, tea.WithContext(ctx))
	_, err := p.Run()
	return err
}
