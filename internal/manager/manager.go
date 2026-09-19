package manager

import "context"

type InstanceState int

const (
	Stopped InstanceState = iota
	Running
	Upgrading
)

func (s InstanceState) String() string {
	switch s {
	case Stopped:
		return "stopped"
	case Running:
		return "running"
	case Upgrading:
		return "upgrading"
	default:
		return "unknown"
	}
}

type InstallationPhase int

const (
	PhaseFetch InstallationPhase = iota
	PhaseDeploy
	PhaseBootstrap
	PhaseRegister
	PhaseEnableAutoStart
	PhaseStart
	PhaseUpgradeCheck
	PhaseUpgradeFetch
	PhaseUpgradeStop
	PhaseUpgradeReplace
	PhaseUpgradeStart
	PhaseUninstallStop
	PhaseUninstallDeregister
	PhaseUninstallCleanup
)

func (p InstallationPhase) String() string {
	switch p {
	case PhaseFetch:
		return "fetch"
	case PhaseDeploy:
		return "deploy"
	case PhaseBootstrap:
		return "bootstrap"
	case PhaseRegister:
		return "register"
	case PhaseEnableAutoStart:
		return "enable-auto-start"
	case PhaseStart:
		return "start"
	case PhaseUpgradeCheck:
		return "check"
	case PhaseUpgradeFetch:
		return "fetch"
	case PhaseUpgradeStop:
		return "stop"
	case PhaseUpgradeReplace:
		return "replace"
	case PhaseUpgradeStart:
		return "start"
	case PhaseUninstallStop:
		return "stop"
	case PhaseUninstallDeregister:
		return "deregister"
	case PhaseUninstallCleanup:
		return "cleanup"
	default:
		return "unknown"
	}
}

type ProgressEvent struct {
	Phase   InstallationPhase
	Message string
	Error   error
}

type ProgressCallback func(ProgressEvent)

type VersionInfo struct {
	Tag string
}

type Status struct {
	InstanceState    InstanceState
	Installed        bool
	Version          string
	AutoStartEnabled bool
}

type ServiceManager interface {
	IsRunning(ctx context.Context, name string) (bool, error)
	Register(ctx context.Context, name, serviceFilePath string) error
	Unregister(ctx context.Context, name string) error
	Start(ctx context.Context, name string) error
	Stop(ctx context.Context, name string) error
	Restart(ctx context.Context, name string) error
	Reload(ctx context.Context, name string) error
	EnableAutoStart(ctx context.Context, name, serviceFilePath string) error
	DisableAutoStart(ctx context.Context, name string) error
	AutoStartEnabled(ctx context.Context, name string) (bool, error)
}

func NewConfigValidator(runners ...CommandRunner) ConfigValidator {
	var runner CommandRunner = OSSystem{}
	if len(runners) > 0 && runners[0] != nil {
		runner = runners[0]
	}
	return &configValidator{cmd: runner}
}

const ServiceName = "mihomo"
