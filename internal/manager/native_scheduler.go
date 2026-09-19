package manager

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	nativescheduler "github.com/heihei0299/mihomo-manage/internal/scheduler"
)

const scheduleFile = "/opt/mihomo-manager/state/schedule.txt"

type PlatformScheduler = nativescheduler.Platform

func NewLinuxPlatformScheduler(fs FileSystem, cmd CommandRunner) PlatformScheduler {
	return nativescheduler.NewLinux(fs, cmd)
}

func NewDarwinPlatformScheduler(fs FileSystem, cmd CommandRunner) PlatformScheduler {
	return nativescheduler.NewDarwin(fs, cmd)
}

type nativeScheduleManager struct {
	fs          FileSystem
	platform    PlatformScheduler
	commandPath string
}

func NewScheduleManagerWithPlatform(fs FileSystem, platform PlatformScheduler, commandPath string) ScheduleManager {
	return &nativeScheduleManager{fs: fs, platform: platform, commandPath: commandPath}
}

func installedManagerPath() string {
	const releasePath = "/usr/local/bin/mihomo-manager"
	const legacyPath = "/opt/mihomo-manager/bin/mihomo-manager"
	executable, err := os.Executable()
	if err == nil {
		path := filepath.Clean(executable)
		if path == releasePath || path == legacyPath {
			return path
		}
	}
	return releasePath
}

func NewNativeScheduleManager(fs FileSystem, cmd CommandRunner) ScheduleManager {
	commandPath := installedManagerPath()
	switch runtime.GOOS {
	case "linux":
		return NewScheduleManagerWithPlatform(fs, NewLinuxPlatformScheduler(fs, cmd), commandPath)
	case "darwin":
		return NewScheduleManagerWithPlatform(fs, NewDarwinPlatformScheduler(fs, cmd), commandPath)
	default:
		return NewScheduleManagerWithPlatform(fs, unsupportedPlatformScheduler{os: runtime.GOOS}, commandPath)
	}
}

func (m *nativeScheduleManager) SetSchedule(ctx context.Context, interval time.Duration) error {
	if !m.fs.FileExists(binaryPath) {
		return ErrMihomoNotInstalled
	}
	return m.platform.Set(ctx, interval, m.commandPath)
}

func (m *nativeScheduleManager) StopSchedule(ctx context.Context) error {
	if err := m.platform.Stop(ctx); err != nil {
		return err
	}
	return m.fs.WriteFile(scheduleFile, []byte("off"), filePermUserRW)
}

func (m *nativeScheduleManager) ScheduleStatus(ctx context.Context) (time.Duration, bool, error) {
	interval, active, err := m.platform.Status(ctx)
	if err != nil {
		return 0, false, err
	}
	if active {
		return interval, true, nil
	}
	data, readErr := m.fs.ReadFile(scheduleFile)
	if readErr == nil {
		raw := strings.TrimSpace(string(data))
		if raw != "" && raw != "off" {
			seconds, parseErr := strconv.ParseInt(raw, 10, 64)
			if parseErr == nil && seconds > 0 {
				return 0, false, LegacyScheduleError{Interval: time.Duration(seconds) * time.Second}
			}
		}
	}
	return 0, false, nil
}

type unsupportedPlatformScheduler struct{ os string }

func (s unsupportedPlatformScheduler) Set(context.Context, time.Duration, string) error {
	return UnsupportedPlatformError{Feature: "scheduler", GOOS: s.os}
}

func (s unsupportedPlatformScheduler) Stop(context.Context) error {
	return UnsupportedPlatformError{Feature: "scheduler", GOOS: s.os}
}

func (s unsupportedPlatformScheduler) Status(context.Context) (time.Duration, bool, error) {
	return 0, false, UnsupportedPlatformError{Feature: "scheduler", GOOS: s.os}
}
