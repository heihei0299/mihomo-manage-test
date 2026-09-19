package manager

import (
	"context"
	"runtime"
	"strings"
)

func looksLikeVersion(s string) bool {
	if len(s) < 2 {
		return false
	}
	if s[0] != 'v' && s[0] != 'V' {
		return false
	}
	return s[1] >= '0' && s[1] <= '9'
}

func parseVersion(ctx context.Context, cmd CommandRunner, binaryPath string) (string, error) {
	out, err := cmd.RunCommand(ctx, binaryPath, "-v")
	if err != nil {
		return "", err
	}
	parts := strings.Fields(out)
	for _, p := range parts {
		if looksLikeVersion(p) {
			return p, nil
		}
	}
	return out, nil
}

type serviceController struct {
	fs             FileSystem
	cmd            CommandRunner
	svcMgr         ServiceManager
	validateConfig func(context.Context) error
}

func NewServiceControl(fs FileSystem, cmd CommandRunner, svcMgr ServiceManager, validateConfig func(context.Context) error) ServiceControl {
	if validateConfig == nil {
		panic("manager: config validation function is required")
	}
	return &serviceController{fs: fs, cmd: cmd, svcMgr: svcMgr, validateConfig: validateConfig}
}

func (m *serviceController) Status(ctx context.Context) (*Status, error) {
	if !m.fs.FileExists(binaryPath) {
		return &Status{
			Installed:     false,
			InstanceState: Stopped,
		}, nil
	}

	running, err := m.svcMgr.IsRunning(ctx, serviceName)
	if err != nil {
		return nil, err
	}

	state := Stopped
	if running {
		state = Running
	}

	version, versionErr := parseVersion(ctx, m.cmd, binaryPath)
	if versionErr != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	autostart, err := m.svcMgr.AutoStartEnabled(ctx, serviceName)
	if err != nil {
		return nil, err
	}

	return &Status{
		Installed:        true,
		InstanceState:    state,
		Version:          version,
		AutoStartEnabled: autostart,
	}, nil
}

func (m *serviceController) SetAutoStart(ctx context.Context, enabled bool) error {
	if !m.fs.FileExists(binaryPath) {
		return ErrMihomoNotInstalled
	}
	svcPath, err := serviceUnitPathFor(runtime.GOOS)
	if err != nil {
		return err
	}
	if enabled {
		return m.svcMgr.EnableAutoStart(ctx, serviceName, svcPath)
	}
	return m.svcMgr.DisableAutoStart(ctx, serviceName)
}

func (m *serviceController) Start(ctx context.Context) error {
	if err := m.validateConfig(ctx); err != nil {
		return err
	}
	if !m.fs.FileExists(binaryPath) {
		return ErrMihomoNotInstalled
	}
	running, err := m.svcMgr.IsRunning(ctx, serviceName)
	if err != nil {
		return err
	}
	if running {
		return ErrMihomoAlreadyRunning
	}
	return m.svcMgr.Start(ctx, serviceName)
}

func (m *serviceController) Stop(ctx context.Context) error {
	if !m.fs.FileExists(binaryPath) {
		return ErrMihomoNotInstalled
	}
	running, err := m.svcMgr.IsRunning(ctx, serviceName)
	if err != nil {
		return err
	}
	if !running {
		return ErrMihomoNotRunning
	}
	return m.svcMgr.Stop(ctx, serviceName)
}

func (m *serviceController) Restart(ctx context.Context) error {
	if err := m.validateConfig(ctx); err != nil {
		return err
	}
	if !m.fs.FileExists(binaryPath) {
		return ErrMihomoNotInstalled
	}
	return m.svcMgr.Restart(ctx, serviceName)
}

func (m *serviceController) Reload(ctx context.Context) error {
	if !m.fs.FileExists(binaryPath) {
		return ErrMihomoNotInstalled
	}
	running, err := m.svcMgr.IsRunning(ctx, serviceName)
	if err != nil {
		return err
	}
	if !running {
		return ErrMihomoNotRunning
	}
	return m.svcMgr.Reload(ctx, serviceName)
}
