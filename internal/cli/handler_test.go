package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/heihei0299/mihomo-manage/internal/manager"
)

type mockControl struct {
	statusFn    func() (*manager.Status, error)
	startFn     func() error
	stopFn      func() error
	restartFn   func() error
	autoStartFn func(enabled bool) error
}

func (m *mockControl) Status(ctx context.Context) (*manager.Status, error) {
	if m.statusFn != nil {
		return m.statusFn()
	}
	return &manager.Status{Installed: true, InstanceState: manager.Running, Version: "v1.0.0", AutoStartEnabled: true}, nil
}

func (m *mockControl) Start(ctx context.Context) error {
	if m.startFn != nil {
		return m.startFn()
	}
	return nil
}

func (m *mockControl) Stop(ctx context.Context) error {
	if m.stopFn != nil {
		return m.stopFn()
	}
	return nil
}

func (m *mockControl) Restart(ctx context.Context) error {
	if m.restartFn != nil {
		return m.restartFn()
	}
	return nil
}
func (m *mockControl) Reload(ctx context.Context) error { return nil }

func (m *mockControl) SetAutoStart(ctx context.Context, enabled bool) error {
	if m.autoStartFn != nil {
		return m.autoStartFn(enabled)
	}
	return nil
}

type mockLifecycle struct {
	installFn      func(version string, autoStart bool, cb manager.ProgressCallback) error
	upgradeFn      func(version string, cb manager.ProgressCallback) error
	listVersionsFn func() ([]manager.VersionInfo, error)
}

func (m *mockLifecycle) Install(ctx context.Context, version string, autoStart bool, onProgress manager.ProgressCallback) error {
	if m.installFn != nil {
		return m.installFn(version, autoStart, onProgress)
	}
	return nil
}

func (m *mockLifecycle) InstallFromLocal(ctx context.Context, localPath string, autoStart bool, onProgress manager.ProgressCallback) error {
	if m.installFn != nil {
		return m.installFn(localPath, autoStart, onProgress)
	}
	return nil
}

func (m *mockLifecycle) Uninstall(ctx context.Context, keepBackup bool, onProgress manager.ProgressCallback) error {
	return nil
}

func (m *mockLifecycle) Upgrade(ctx context.Context, version string, onProgress manager.ProgressCallback) error {
	if m.upgradeFn != nil {
		return m.upgradeFn(version, onProgress)
	}
	return nil
}

func (m *mockLifecycle) ListVersions(ctx context.Context) ([]manager.VersionInfo, error) {
	if m.listVersionsFn != nil {
		return m.listVersionsFn()
	}
	return []manager.VersionInfo{{Tag: "v1.0.0"}}, nil
}

type mockConfig struct {
	previewFn         func() (string, error)
	setSubscriptionFn func(url string) error
	updateConfigFn    func() error
	adoptConfigFn     func(force bool) (manager.AdoptReport, error)
	lastConfig        manager.ConfigApplyStatus
	lastConfigErr     error
}

func (m *mockConfig) AdoptConfig(ctx context.Context, force bool) (manager.AdoptReport, error) {
	if m.adoptConfigFn != nil {
		return m.adoptConfigFn(force)
	}
	return manager.AdoptReport{NoChanges: true}, nil
}

func (m *mockConfig) SetSubscriptionSource(ctx context.Context, url string) error {
	if m.setSubscriptionFn != nil {
		return m.setSubscriptionFn(url)
	}
	return nil
}

func (m *mockConfig) PreviewConfig(ctx context.Context) (string, error) {
	if m.previewFn != nil {
		return m.previewFn()
	}
	return "proxies: test", nil
}

func (m *mockConfig) UpdateConfig(ctx context.Context) error {
	if m.updateConfigFn != nil {
		return m.updateConfigFn()
	}
	return nil
}

func (m *mockConfig) ValidateConfig(ctx context.Context) error { return nil }

func (m *mockConfig) LastConfigApply(ctx context.Context) (manager.ConfigApplyStatus, error) {
	if m.lastConfig.State != "" || m.lastConfigErr != nil {
		return m.lastConfig, m.lastConfigErr
	}
	return manager.ConfigApplyStatus{State: manager.ConfigUnknown}, nil
}

type mockSchedule struct {
	setScheduleFn    func(interval time.Duration) error
	stopScheduleFn   func() error
	scheduleStatusFn func() (time.Duration, bool, error)
}

func (m *mockSchedule) SetSchedule(ctx context.Context, interval time.Duration) error {
	if m.setScheduleFn != nil {
		return m.setScheduleFn(interval)
	}
	return nil
}

func (m *mockSchedule) StopSchedule(ctx context.Context) error {
	if m.stopScheduleFn != nil {
		return m.stopScheduleFn()
	}
	return nil
}

func (m *mockSchedule) ScheduleStatus(ctx context.Context) (time.Duration, bool, error) {
	if m.scheduleStatusFn != nil {
		return m.scheduleStatusFn()
	}
	return 0, false, nil
}

func TestStatusRunning(t *testing.T) {
	var stdout, stderr bytes.Buffer
	h := New(&mockControl{}, &mockLifecycle{}, &mockConfig{}, &mockSchedule{}, &stdout, &stderr)

	code := h.Status(context.Background())

	if code != 0 {
		t.Errorf("expected exit code 0, got %d", code)
	}
	if !strings.Contains(stdout.String(), "running") {
		t.Errorf("stdout should contain 'running', got %q", stdout.String())
	}
}

func TestVersionsEmptyReturnsFailure(t *testing.T) {
	var stdout, stderr strings.Builder
	h := New(&mockControl{}, &mockLifecycle{
		listVersionsFn: func() ([]manager.VersionInfo, error) { return nil, nil },
	}, &mockConfig{}, &mockSchedule{}, &stdout, &stderr)

	if code := h.Versions(context.Background()); code == 0 {
		t.Fatal("empty version list should return a non-zero result")
	}
	if !strings.Contains(stderr.String(), "no versions") {
		t.Fatalf("stderr = %q, want clear empty-result message", stderr.String())
	}
}

func TestUpgradeFailureReturnsNonZeroAndExplainsFailure(t *testing.T) {
	var stdout, stderr strings.Builder
	h := New(&mockControl{}, &mockLifecycle{
		upgradeFn: func(string, manager.ProgressCallback) error {
			return errors.New("rollback failed; manual recovery may be required: restart service failed")
		},
	}, &mockConfig{}, &mockSchedule{}, &stdout, &stderr)

	if code := h.Upgrade(context.Background(), "v1.2.3"); code == 0 {
		t.Fatal("failed upgrade should return a non-zero result")
	}
	if !strings.Contains(stderr.String(), "rollback failed") || !strings.Contains(stderr.String(), "manual recovery") {
		t.Fatalf("stderr = %q, want failure details and manual-recovery guidance", stderr.String())
	}
}

func TestUpgradePrintsLifecycleProgressOnSuccess(t *testing.T) {
	var stdout, stderr strings.Builder
	h := New(&mockControl{}, &mockLifecycle{
		upgradeFn: func(_ string, cb manager.ProgressCallback) error {
			for _, phase := range []manager.InstallationPhase{
				manager.PhaseUpgradeStop,
				manager.PhaseUpgradeReplace,
				manager.PhaseUpgradeStart,
			} {
				cb(manager.ProgressEvent{Phase: phase, Message: phase.String() + " complete"})
			}
			return nil
		},
	}, &mockConfig{}, &mockSchedule{}, &stdout, &stderr)

	if code := h.Upgrade(context.Background(), "v1.2.3"); code != 0 {
		t.Fatalf("expected success, got %d: %s", code, stderr.String())
	}
	for _, phase := range []string{"[stop]", "[replace]", "[start]", "upgrade complete"} {
		if !strings.Contains(stdout.String(), phase) {
			t.Errorf("stdout = %q, want %q", stdout.String(), phase)
		}
	}
}

func TestStatusShowsLatestConfigApply(t *testing.T) {
	var stdout, stderr bytes.Buffer
	h := New(&mockControl{}, &mockLifecycle{}, &mockConfig{
		lastConfig: manager.ConfigApplyStatus{State: manager.ConfigPendingReload},
	}, &mockSchedule{}, &stdout, &stderr)

	if code := h.Status(context.Background()); code != 0 {
		t.Fatalf("expected exit code 0, got %d", code)
	}
	if !strings.Contains(stdout.String(), "config: pending-reload") {
		t.Fatalf("stdout = %q, want config status", stdout.String())
	}
}

func TestStatusShowsConfigApplyFailure(t *testing.T) {
	var stdout, stderr bytes.Buffer
	h := New(&mockControl{}, &mockLifecycle{}, &mockConfig{
		lastConfig: manager.ConfigApplyStatus{
			State:        manager.ConfigApplyFailed,
			ErrorSummary: "writing staged config failed",
		},
	}, &mockSchedule{}, &stdout, &stderr)

	if code := h.Status(context.Background()); code != 0 {
		t.Fatalf("expected exit code 0, got %d", code)
	}
	if got := stdout.String(); !strings.Contains(got, "config: apply-failed (writing staged config failed)") {
		t.Fatalf("stdout = %q, want apply-failed status", got)
	}
}

func TestStatusNotInstalled(t *testing.T) {
	var stdout, stderr bytes.Buffer
	h := New(&mockControl{
		statusFn: func() (*manager.Status, error) {
			return &manager.Status{Installed: false}, nil
		},
	}, &mockLifecycle{}, &mockConfig{}, &mockSchedule{}, &stdout, &stderr)

	code := h.Status(context.Background())

	if code != 2 {
		t.Errorf("expected exit code 2, got %d", code)
	}
	if !strings.Contains(stdout.String(), "not installed") {
		t.Errorf("stdout should contain 'not installed', got %q", stdout.String())
	}
}

func TestStatusError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	h := New(&mockControl{
		statusFn: func() (*manager.Status, error) {
			return nil, errors.New("service not found")
		},
	}, &mockLifecycle{}, &mockConfig{}, &mockSchedule{}, &stdout, &stderr)

	code := h.Status(context.Background())

	if code != 1 {
		t.Errorf("expected exit code 1, got %d", code)
	}
	if !strings.Contains(stderr.String(), "error") {
		t.Errorf("stderr should contain 'error', got %q", stderr.String())
	}
}

func TestStart(t *testing.T) {
	var stdout, stderr bytes.Buffer
	h := New(&mockControl{}, &mockLifecycle{}, &mockConfig{}, &mockSchedule{}, &stdout, &stderr)

	code := h.Start(context.Background())

	if code != 0 {
		t.Errorf("expected exit code 0, got %d", code)
	}
	if !strings.Contains(stdout.String(), "started") {
		t.Errorf("stdout should contain 'started', got %q", stdout.String())
	}
}

func TestStartError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	h := New(&mockControl{
		startFn: func() error { return errors.New("not installed") },
	}, &mockLifecycle{}, &mockConfig{}, &mockSchedule{}, &stdout, &stderr)

	code := h.Start(context.Background())

	if code != 1 {
		t.Errorf("expected exit code 1, got %d", code)
	}
	if !strings.Contains(stderr.String(), "error") {
		t.Errorf("stderr should contain 'error', got %q", stderr.String())
	}
}

func TestRestart(t *testing.T) {
	var stdout, stderr bytes.Buffer
	h := New(&mockControl{}, &mockLifecycle{}, &mockConfig{}, &mockSchedule{}, &stdout, &stderr)

	code := h.Restart(context.Background())

	if code != 0 {
		t.Errorf("expected exit code 0, got %d", code)
	}
	if !strings.Contains(stdout.String(), "restarted") {
		t.Errorf("stdout should contain 'restarted', got %q", stdout.String())
	}
}

func TestRestartError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	h := New(&mockControl{
		restartFn: func() error { return errors.New("not installed") },
	}, &mockLifecycle{}, &mockConfig{}, &mockSchedule{}, &stdout, &stderr)

	code := h.Restart(context.Background())

	if code != 1 {
		t.Errorf("expected exit code 1, got %d", code)
	}
	if !strings.Contains(stderr.String(), "error") {
		t.Errorf("stderr should contain 'error', got %q", stderr.String())
	}
}

func TestAutoStartOn(t *testing.T) {
	var stdout, stderr bytes.Buffer
	h := New(&mockControl{}, &mockLifecycle{}, &mockConfig{}, &mockSchedule{}, &stdout, &stderr)

	code := h.AutoStart(context.Background(), true)

	if code != 0 {
		t.Errorf("expected exit code 0, got %d", code)
	}
	if !strings.Contains(stdout.String(), "on") {
		t.Errorf("stdout should contain 'on', got %q", stdout.String())
	}
}

func TestAutoStartOff(t *testing.T) {
	var stdout, stderr bytes.Buffer
	h := New(&mockControl{}, &mockLifecycle{}, &mockConfig{}, &mockSchedule{}, &stdout, &stderr)

	code := h.AutoStart(context.Background(), false)

	if code != 0 {
		t.Errorf("expected exit code 0, got %d", code)
	}
	if !strings.Contains(stdout.String(), "off") {
		t.Errorf("stdout should contain 'off', got %q", stdout.String())
	}
}

func TestAutoStartError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	h := New(&mockControl{
		autoStartFn: func(enabled bool) error { return errors.New("not installed") },
	}, &mockLifecycle{}, &mockConfig{}, &mockSchedule{}, &stdout, &stderr)

	code := h.AutoStart(context.Background(), true)

	if code != 1 {
		t.Errorf("expected exit code 1, got %d", code)
	}
	if !strings.Contains(stderr.String(), "error") {
		t.Errorf("stderr should contain 'error', got %q", stderr.String())
	}
}

func TestPreviewConfig(t *testing.T) {
	var stdout, stderr bytes.Buffer
	h := New(&mockControl{}, &mockLifecycle{}, &mockConfig{
		previewFn: func() (string, error) {
			return "proxies:\n  - server: example\n", nil
		},
	}, &mockSchedule{}, &stdout, &stderr)

	code := h.PreviewConfig(context.Background())

	if code != 0 {
		t.Errorf("expected exit code 0, got %d", code)
	}
	if !strings.Contains(stdout.String(), "example") {
		t.Errorf("stdout should contain config content, got %q", stdout.String())
	}
}

func TestScheduleStatusReportsWrappedLegacyError(t *testing.T) {
	var stdout, stderr strings.Builder
	h := New(&mockControl{}, &mockLifecycle{}, &mockConfig{}, &mockSchedule{
		scheduleStatusFn: func() (time.Duration, bool, error) {
			return 0, false, fmt.Errorf("schedule lookup: %w", manager.LegacyScheduleError{Interval: time.Hour})
		},
	}, &stdout, &stderr)

	code := h.ScheduleStatus(context.Background())
	if code != 1 {
		t.Errorf("expected exit code 1, got %d", code)
	}
	if !strings.Contains(stderr.String(), "schedule:") {
		t.Errorf("stderr should use legacy schedule branch, got %q", stderr.String())
	}
	if strings.Contains(stderr.String(), "schedule status failed") {
		t.Errorf("stderr should not use generic schedule branch, got %q", stderr.String())
	}
}

func TestHandlerAdoptConfigNoChanges(t *testing.T) {
	var stdout, stderr strings.Builder
	h := New(&mockControl{}, &mockLifecycle{}, &mockConfig{
		adoptConfigFn: func(bool) (manager.AdoptReport, error) {
			return manager.AdoptReport{NoChanges: true}, nil
		},
	}, &mockSchedule{}, &stdout, &stderr)

	code := h.AdoptConfig(context.Background(), false)
	if code != 0 {
		t.Errorf("expected exit code 0, got %d", code)
	}
	if !strings.Contains(stdout.String(), "no changes") {
		t.Errorf("stdout should report no changes, got %q", stdout.String())
	}
}

func TestHandlerAdoptConfigLargeDiff(t *testing.T) {
	var stdout, stderr strings.Builder
	h := New(&mockControl{}, &mockLifecycle{}, &mockConfig{
		adoptConfigFn: func(bool) (manager.AdoptReport, error) {
			return manager.AdoptReport{Fields: []string{"a", "b", "c", "d", "e"}, LargeDiff: true}, fmt.Errorf("adopt: %w", manager.ErrAdoptNeedsConfirmation)
		},
	}, &mockSchedule{}, &stdout, &stderr)

	code := h.AdoptConfig(context.Background(), false)
	if code != 1 {
		t.Errorf("expected exit code 1, got %d", code)
	}
	if !strings.Contains(stderr.String(), "--force") {
		t.Errorf("stderr should suggest --force, got %q", stderr.String())
	}
}

func TestHandlerAdoptConfigAdopts(t *testing.T) {
	var stdout, stderr strings.Builder
	report := manager.AdoptReport{Fields: []string{"port", "mode"}, ArrayDiff: []string{"proxies"}}
	h := New(&mockControl{}, &mockLifecycle{}, &mockConfig{
		adoptConfigFn: func(bool) (manager.AdoptReport, error) { return report, nil },
	}, &mockSchedule{}, &stdout, &stderr)

	code := h.AdoptConfig(context.Background(), false)
	if code != 0 {
		t.Errorf("expected exit code 0, got %d", code)
	}
	if !strings.Contains(stdout.String(), "port") || !strings.Contains(stdout.String(), "mode") {
		t.Errorf("stdout should list adopted fields, got %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "proxies") {
		t.Errorf("stdout should list skipped arrays, got %q", stdout.String())
	}
}
