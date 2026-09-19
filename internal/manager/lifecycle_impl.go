package manager

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"runtime"
	"strings"
	"time"
)

const backupDir = managerRoot + "/backups"

type lifecycleManager struct {
	fs       FileSystem
	cmd      CommandRunner
	source   ReleaseSource
	svcMgr   ServiceManager
	schedule ScheduleManager
}

func NewLifecycleManager(fs FileSystem, cmd CommandRunner, source ReleaseSource, svcMgr ServiceManager, schedules ...ScheduleManager) LifecycleManager {
	var schedule ScheduleManager
	if len(schedules) > 0 {
		schedule = schedules[0]
	}
	return &lifecycleManager{fs: fs, cmd: cmd, source: source, svcMgr: svcMgr, schedule: schedule}
}

func (m *lifecycleManager) resolveVersion(ctx context.Context, version string) (string, error) {
	if version != "latest" {
		return version, nil
	}
	tag, err := m.source.LatestVersion(ctx, "MetaCubeX", "mihomo")
	if err != nil {
		return "", fmt.Errorf("resolve latest version: %w", err)
	}
	if strings.TrimSpace(tag) == "" {
		return "", errors.New("resolve latest version: release has no tag")
	}
	return tag, nil
}

func cleanupArtifacts(fs FileSystem, paths ...string) error {
	var cleanupErrs []error
	for _, path := range paths {
		if err := fs.Remove(path); err != nil {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("remove %s: %w", path, err))
		}
	}
	return errors.Join(cleanupErrs...)
}

func withCleanupError(primary error, fs FileSystem, paths ...string) error {
	if cleanupErr := cleanupArtifacts(fs, paths...); cleanupErr != nil {
		return errors.Join(primary, fmt.Errorf("cleanup failed: %w", cleanupErr))
	}
	return primary
}

func (m *lifecycleManager) downloadAndDecompress(ctx context.Context, version string, onProgress ProgressCallback, checkPhase, fetchPhase InstallationPhase) (string, error) {
	tempPath := fmt.Sprintf("%s.tmp.%s", binaryPath, version)
	gzPath := tempPath + ".gz"
	assetURL := releaseURL(runtime.GOOS, runtime.GOARCH, version)
	assetName := releaseAssetName(assetURL)

	onProgress(ProgressEvent{Phase: checkPhase, Message: fmt.Sprintf("Checking mihomo %s", version)})
	expected, err := m.source.ExpectedChecksum(ctx, "MetaCubeX", "mihomo", version, assetName)
	if err != nil {
		onProgress(ProgressEvent{Phase: checkPhase, Message: "Checksum unavailable", Error: err})
		return "", fmt.Errorf("checksum unavailable: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}

	onProgress(ProgressEvent{Phase: fetchPhase, Message: fmt.Sprintf("Downloading mihomo %s", version)})
	if err := m.source.Download(ctx, assetURL, gzPath); err != nil {
		onProgress(ProgressEvent{Phase: fetchPhase, Message: "Download failed", Error: err})
		return "", withCleanupError(fmt.Errorf("download failed: %w", err), m.fs, gzPath, tempPath)
	}
	if err := ctx.Err(); err != nil {
		return "", withCleanupError(err, m.fs, gzPath, tempPath)
	}
	if err := verifyChecksum(m.fs, gzPath, expected, assetName); err != nil {
		onProgress(ProgressEvent{Phase: checkPhase, Message: "Checksum verification failed", Error: err})
		return "", withCleanupError(err, m.fs, gzPath, tempPath)
	}
	onProgress(ProgressEvent{Phase: checkPhase, Message: "Checksum verified"})
	onProgress(ProgressEvent{Phase: fetchPhase, Message: "Decompressing"})
	if err := m.decompressGzip(gzPath, tempPath); err != nil {
		return "", withCleanupError(fmt.Errorf("decompress failed: %w", err), m.fs, gzPath, tempPath)
	}
	if err := ctx.Err(); err != nil {
		return "", withCleanupError(err, m.fs, gzPath, tempPath)
	}
	if err := m.fs.Remove(gzPath); err != nil {
		return "", withCleanupError(fmt.Errorf("cleanup downloaded artifact: %w", err), m.fs, gzPath, tempPath)
	}
	onProgress(ProgressEvent{Phase: fetchPhase, Message: "Download complete"})
	return tempPath, nil
}

func releaseAssetName(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err == nil && parsed.Path != "" {
		return path.Base(parsed.Path)
	}
	return path.Base(rawURL)
}

func verifyChecksum(fs FileSystem, filePath, expected, assetName string) error {
	normalized, err := normalizeChecksum(expected, assetName)
	if err != nil {
		return fmt.Errorf("invalid checksum for %s: %w", assetName, err)
	}
	data, err := fs.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("reading downloaded artifact: %w", err)
	}
	actualBytes := sha256.Sum256(data)
	actual := hex.EncodeToString(actualBytes[:])
	if !strings.EqualFold(actual, normalized) {
		return fmt.Errorf("checksum mismatch for %s: got %s, want %s", assetName, actual, normalized)
	}
	return nil
}

func (m *lifecycleManager) decompressGzip(src, dest string) error {
	data, err := m.fs.ReadFile(src)
	if err != nil {
		return err
	}
	gr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("decompress: %w", err)
	}
	defer gr.Close()
	decompressed, err := io.ReadAll(gr)
	if err != nil {
		return fmt.Errorf("decompress read: %w", err)
	}
	gr.Close()
	return m.fs.WriteFile(dest, decompressed, filePermUserRWX)
}

// rollbackInstall defines the install recovery boundary.
//
// State contract:
//
//	pre-deploy failure       -> no rollback-owned state exists
//	post-deploy failure      -> stop/unregister service and remove deployed files
//	rollback step failure    -> preserve both the primary failure and rollback failure
//	canceled caller context  -> rollback still runs with context.WithoutCancel
//
// Keep this contract aligned with the lifecycle rollback behavior tests.
func (m *lifecycleManager) rollbackInstall(ctx context.Context, phase string, err error) error {
	rollbackCtx := context.WithoutCancel(ctx)
	var rollbackErrs []error
	if rollbackErr := m.svcMgr.Stop(rollbackCtx, serviceName); rollbackErr != nil {
		rollbackErrs = append(rollbackErrs, fmt.Errorf("stop service: %w", rollbackErr))
	}
	if rollbackErr := m.svcMgr.Unregister(rollbackCtx, serviceName); rollbackErr != nil {
		rollbackErrs = append(rollbackErrs, fmt.Errorf("unregister service: %w", rollbackErr))
	}
	if rollbackErr := m.fs.Remove(binaryPath); rollbackErr != nil {
		rollbackErrs = append(rollbackErrs, fmt.Errorf("remove binary: %w", rollbackErr))
	}
	if rollbackErr := m.fs.Remove(configDir); rollbackErr != nil {
		rollbackErrs = append(rollbackErrs, fmt.Errorf("remove config: %w", rollbackErr))
	}
	if rollbackErr := m.fs.Remove(serviceUnitPath()); rollbackErr != nil {
		rollbackErrs = append(rollbackErrs, fmt.Errorf("remove service unit: %w", rollbackErr))
	}
	failure := fmt.Errorf("install failed at %s: %w", phase, err)
	if rollbackErr := errors.Join(rollbackErrs...); rollbackErr != nil {
		return errors.Join(failure, fmt.Errorf("rollback failed: %w", rollbackErr))
	}
	return failure
}

func (m *lifecycleManager) Install(ctx context.Context, version string, autoStart bool, onProgress ProgressCallback) error {
	if _, err := serviceUnitPathFor(runtime.GOOS); err != nil {
		return err
	}
	if version == "latest" {
		if resolved, err := m.resolveVersion(ctx, version); err == nil {
			version = resolved
		}
	}
	tempPath, err := m.downloadAndDecompress(ctx, version, onProgress, PhaseFetch, PhaseFetch)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return withCleanupError(err, m.fs, tempPath)
	}
	return m.installBinary(ctx, tempPath, autoStart, onProgress)
}

func (m *lifecycleManager) InstallFromLocal(ctx context.Context, localPath string, autoStart bool, onProgress ProgressCallback) error {
	if _, err := serviceUnitPathFor(runtime.GOOS); err != nil {
		return err
	}
	tempPath, err := m.resolveLocalBinary(ctx, localPath)
	if err != nil {
		return fmt.Errorf("local binary: %w", err)
	}
	installErr := m.installBinary(ctx, tempPath, autoStart, onProgress)
	if tempPath == localPath {
		return installErr
	}
	if cleanupErr := m.fs.Remove(tempPath); cleanupErr != nil {
		if installErr != nil {
			return errors.Join(installErr, fmt.Errorf("cleanup local binary: %w", cleanupErr))
		}
		return fmt.Errorf("cleanup local binary: %w", cleanupErr)
	}
	return installErr
}

func (m *lifecycleManager) resolveLocalBinary(ctx context.Context, localPath string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if strings.HasSuffix(localPath, ".gz") {
		tempPath := binaryPath + ".tmp.local"
		if err := m.decompressGzip(localPath, tempPath); err != nil {
			return "", withCleanupError(err, m.fs, tempPath)
		}
		if err := ctx.Err(); err != nil {
			return "", withCleanupError(err, m.fs, tempPath)
		}
		return tempPath, nil
	}
	if !m.fs.FileExists(localPath) {
		return "", fmt.Errorf("file not found: %s", localPath)
	}
	return localPath, nil
}

func (m *lifecycleManager) installBinary(ctx context.Context, binarySrc string, autoStart bool, onProgress ProgressCallback) error {
	svcPath, err := serviceUnitPathFor(runtime.GOOS)
	if err != nil {
		return err
	}
	svcContent, err := serviceUnitContentFor(runtime.GOOS, autoStart)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	onProgress(ProgressEvent{Phase: PhaseDeploy, Message: "Deploying binary"})
	if err := m.fs.Rename(binarySrc, binaryPath); err != nil {
		rollbackErr := m.rollbackInstall(ctx, "deploy rename", err)
		if cleanupErr := m.fs.Remove(binarySrc); cleanupErr != nil {
			return errors.Join(rollbackErr, fmt.Errorf("cleanup deploy source: %w", cleanupErr))
		}
		return rollbackErr
	}
	onProgress(ProgressEvent{Phase: PhaseDeploy, Message: "Binary deployed"})
	if err := ctx.Err(); err != nil {
		return m.rollbackInstall(ctx, "deploy", err)
	}

	onProgress(ProgressEvent{Phase: PhaseBootstrap, Message: "Creating directories"})
	if err := m.fs.MkdirAll(configDir, filePermUserRWX); err != nil {
		return m.rollbackInstall(ctx, "bootstrap mkdir config", err)
	}
	if err := m.fs.MkdirAll(stateDir, filePermUserRWX); err != nil {
		return m.rollbackInstall(ctx, "bootstrap mkdir state", err)
	}
	if err := m.fs.WriteFile(OverrideFilePath, defaultOverride, filePermUserRW); err != nil {
		return m.rollbackInstall(ctx, "bootstrap template", err)
	}
	if err := m.fs.WriteFile(configYAML, defaultConfig, filePermUserRW); err != nil {
		return m.rollbackInstall(ctx, "bootstrap config", err)
	}
	if err := m.fs.WriteFile(svcPath, svcContent, filePermUserRW); err != nil {
		return m.rollbackInstall(ctx, "bootstrap service unit", err)
	}
	onProgress(ProgressEvent{Phase: PhaseBootstrap, Message: "Config files created"})
	if err := ctx.Err(); err != nil {
		return m.rollbackInstall(ctx, "bootstrap", err)
	}

	onProgress(ProgressEvent{Phase: PhaseRegister, Message: "Registering system service"})
	if err := m.svcMgr.Register(ctx, serviceName, svcPath); err != nil {
		return m.rollbackInstall(ctx, "service register", err)
	}
	onProgress(ProgressEvent{Phase: PhaseRegister, Message: "Service registered"})
	if err := ctx.Err(); err != nil {
		return m.rollbackInstall(ctx, "service register", err)
	}

	if autoStart {
		onProgress(ProgressEvent{Phase: PhaseEnableAutoStart, Message: "Enabling auto-start"})
		if err := m.svcMgr.EnableAutoStart(ctx, serviceName, svcPath); err != nil {
			return m.rollbackInstall(ctx, "enable auto-start", err)
		}
		onProgress(ProgressEvent{Phase: PhaseEnableAutoStart, Message: "Auto-start enabled"})
		if err := ctx.Err(); err != nil {
			return m.rollbackInstall(ctx, "enable auto-start", err)
		}
	}
	if err := ctx.Err(); err != nil {
		return m.rollbackInstall(ctx, "before start", err)
	}

	onProgress(ProgressEvent{Phase: PhaseStart, Message: "Starting mihomo"})
	if err := m.svcMgr.Start(ctx, serviceName); err != nil {
		return m.rollbackInstall(ctx, "service start", err)
	}
	if err := ctx.Err(); err != nil {
		return m.rollbackInstall(ctx, "after service start", err)
	}
	onProgress(ProgressEvent{Phase: PhaseStart, Message: "mihomo is running"})

	return nil
}

func (m *lifecycleManager) Uninstall(ctx context.Context, keepBackup bool, onProgress ProgressCallback) error {
	if !m.fs.FileExists(binaryPath) {
		return ErrMihomoNotInstalled
	}
	if m.schedule != nil {
		if err := m.schedule.StopSchedule(ctx); err != nil {
			return fmt.Errorf("stop subscription schedule: %w", err)
		}
	}

	onProgress(ProgressEvent{Phase: PhaseUninstallStop, Message: "Stopping mihomo"})
	running, err := m.svcMgr.IsRunning(ctx, serviceName)
	if err != nil {
		return fmt.Errorf("check service state: %w", err)
	}
	if running {
		if err := m.svcMgr.Stop(ctx, serviceName); err != nil {
			return fmt.Errorf("stop failed: %w", err)
		}
	}
	onProgress(ProgressEvent{Phase: PhaseUninstallStop, Message: "Stopped"})

	onProgress(ProgressEvent{Phase: PhaseUninstallDeregister, Message: "Removing service"})
	if err := m.svcMgr.Unregister(ctx, serviceName); err != nil {
		return fmt.Errorf("unregister failed: %w", err)
	}
	onProgress(ProgressEvent{Phase: PhaseUninstallDeregister, Message: "Service removed"})

	onProgress(ProgressEvent{Phase: PhaseUninstallCleanup, Message: "Cleaning up files"})
	if keepBackup {
		backupPath := fmt.Sprintf("%s.bak.%d", installRoot, time.Now().Unix())
		if err := m.fs.Rename(installRoot, backupPath); err != nil {
			return fmt.Errorf("backup uninstall files: %w", err)
		}
		onProgress(ProgressEvent{Phase: PhaseUninstallCleanup, Message: "Files backed up to " + backupPath})
		return nil
	}

	var cleanupErrs []error
	for _, path := range []string{installRoot, managerRoot} {
		if err := m.fs.Remove(path); err != nil {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("remove %s: %w", path, err))
		}
	}
	if err := errors.Join(cleanupErrs...); err != nil {
		return err
	}
	onProgress(ProgressEvent{Phase: PhaseUninstallCleanup, Message: "Files removed"})
	return nil
}

// Upgrade is transactional around binary replacement.
//
// Recovery contract:
//
//	failure before backup       -> leave installed binary untouched
//	failure after service stop  -> resume the previously running service
//	failure after backup        -> restore the old binary
//	failure after replacement   -> restore old binary and prior running state
//	rollback failure            -> return both primary and rollback errors
func (m *lifecycleManager) Upgrade(ctx context.Context, version string, onProgress ProgressCallback) error {
	if !m.fs.FileExists(binaryPath) {
		return ErrMihomoNotInstalled
	}

	onProgress(ProgressEvent{Phase: PhaseUpgradeCheck, Message: "Checking upgrade target"})
	resolvedVersion, err := m.resolveVersion(ctx, version)
	if err != nil {
		onProgress(ProgressEvent{Phase: PhaseUpgradeCheck, Message: "Version lookup failed", Error: err})
		return err
	}

	tempPath, err := m.downloadAndDecompress(ctx, resolvedVersion, onProgress, PhaseUpgradeCheck, PhaseUpgradeFetch)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return withCleanupError(err, m.fs, tempPath)
	}

	reportFailure := func(phase InstallationPhase, message string, failure error) error {
		onProgress(ProgressEvent{Phase: phase, Message: message, Error: failure})
		return failure
	}
	onProgress(ProgressEvent{Phase: PhaseUpgradeStop, Message: "Stopping mihomo"})
	wasRunning, statusErr := m.svcMgr.IsRunning(ctx, serviceName)
	if statusErr != nil {
		failure := withCleanupError(fmt.Errorf("check service state: %w", statusErr), m.fs, tempPath)
		return reportFailure(PhaseUpgradeStop, "Service state check failed", failure)
	}
	resumeService := func() error {
		if !wasRunning {
			return nil
		}
		resumeCtx := context.WithoutCancel(ctx)
		if err := m.svcMgr.Start(resumeCtx, serviceName); err != nil {
			return fmt.Errorf("resume service: %w", err)
		}
		running, err := m.svcMgr.IsRunning(resumeCtx, serviceName)
		if err != nil {
			return fmt.Errorf("confirm resumed service: %w", err)
		}
		if !running {
			return errors.New("resumed service is not running")
		}
		return nil
	}
	if wasRunning {
		if err := m.svcMgr.Stop(ctx, serviceName); err != nil {
			failure := withCleanupError(withRecoveryError(fmt.Errorf("stop failed: %w", err), resumeService()), m.fs, tempPath)
			return reportFailure(PhaseUpgradeStop, "Stop failed; service recovery attempted", failure)
		}
	}
	onProgress(ProgressEvent{Phase: PhaseUpgradeStop, Message: "Stopped"})
	if err := ctx.Err(); err != nil {
		failure := withCleanupError(errors.Join(err, resumeService()), m.fs, tempPath)
		return reportFailure(PhaseUpgradeStop, "Upgrade canceled", failure)
	}

	onProgress(ProgressEvent{Phase: PhaseUpgradeReplace, Message: "Backing up old binary"})
	if err := m.fs.MkdirAll(backupDir, filePermUserRWX); err != nil {
		failure := withCleanupError(errors.Join(fmt.Errorf("create backup directory: %w", err), resumeService()), m.fs, tempPath)
		return reportFailure(PhaseUpgradeReplace, "Backup failed", failure)
	}
	backupPath := backupDir + "/mihomo.bak"
	if err := m.fs.Rename(binaryPath, backupPath); err != nil {
		failure := withCleanupError(errors.Join(fmt.Errorf("backup binary: %w", err), resumeService()), m.fs, tempPath)
		return reportFailure(PhaseUpgradeReplace, "Backup failed", failure)
	}
	if err := ctx.Err(); err != nil {
		failure := withRollbackError(err, m.restoreBinary(ctx, backupPath, tempPath, wasRunning, wasRunning))
		return reportFailure(PhaseUpgradeReplace, "Upgrade canceled; rolled back", failure)
	}

	onProgress(ProgressEvent{Phase: PhaseUpgradeReplace, Message: "Replacing binary"})
	if err := m.fs.Chmod(tempPath, filePermUserRWX); err != nil {
		failure := withRollbackError(fmt.Errorf("chmod failed: %w", err), m.restoreBinary(ctx, backupPath, tempPath, wasRunning, wasRunning))
		return reportFailure(PhaseUpgradeReplace, "Replacement failed; rolled back", failure)
	}
	if err := m.fs.Rename(tempPath, binaryPath); err != nil {
		failure := withRollbackError(fmt.Errorf("rename failed: %w", err), m.restoreBinary(ctx, backupPath, tempPath, wasRunning, wasRunning))
		return reportFailure(PhaseUpgradeReplace, "Replacement failed; rolled back", failure)
	}
	onProgress(ProgressEvent{Phase: PhaseUpgradeReplace, Message: "Binary replaced"})
	if err := ctx.Err(); err != nil {
		failure := withRollbackError(err, m.restoreBinary(ctx, backupPath, "", wasRunning, true))
		return reportFailure(PhaseUpgradeReplace, "Upgrade canceled; rolled back", failure)
	}

	if !wasRunning {
		running, err := m.svcMgr.IsRunning(ctx, serviceName)
		if err != nil {
			failure := withRollbackError(fmt.Errorf("confirm service stopped: %w", err), m.restoreBinary(ctx, backupPath, "", wasRunning, true))
			return reportFailure(PhaseUpgradeStart, "Stopped-state confirmation failed; rolled back", failure)
		}
		if running {
			failure := withRollbackError(errors.New("service did not remain stopped after upgrade"), m.restoreBinary(ctx, backupPath, "", wasRunning, true))
			return reportFailure(PhaseUpgradeStart, "Stopped-state confirmation failed; rolled back", failure)
		}
		onProgress(ProgressEvent{Phase: PhaseUpgradeStart, Message: "Keeping mihomo stopped"})
		return nil
	}

	onProgress(ProgressEvent{Phase: PhaseUpgradeStart, Message: "Starting mihomo"})
	if err := m.svcMgr.Start(ctx, serviceName); err != nil {
		failure := withRollbackError(fmt.Errorf("start failed: %w", err), m.restoreBinary(ctx, backupPath, "", wasRunning, true))
		return reportFailure(PhaseUpgradeStart, "Start failed; rolled back", failure)
	}
	if err := ctx.Err(); err != nil {
		failure := withRollbackError(err, m.restoreBinary(ctx, backupPath, "", wasRunning, true))
		return reportFailure(PhaseUpgradeStart, "Upgrade canceled; rolled back", failure)
	}
	running, err := m.svcMgr.IsRunning(ctx, serviceName)
	if err != nil {
		failure := withRollbackError(fmt.Errorf("confirm service running: %w", err), m.restoreBinary(ctx, backupPath, "", wasRunning, true))
		return reportFailure(PhaseUpgradeStart, "Running-state confirmation failed; rolled back", failure)
	}
	if !running {
		failure := withRollbackError(errors.New("service did not become running after start"), m.restoreBinary(ctx, backupPath, "", wasRunning, true))
		return reportFailure(PhaseUpgradeStart, "Running-state confirmation failed; rolled back", failure)
	}
	onProgress(ProgressEvent{Phase: PhaseUpgradeStart, Message: "Running " + resolvedVersion})

	return nil
}

func withRecoveryError(primary, recovery error) error {
	if recovery == nil {
		return primary
	}
	return errors.Join(primary, fmt.Errorf("recovery failed: %w", recovery))
}

func withRollbackError(primary, rollback error) error {
	if rollback == nil {
		return primary
	}
	return errors.Join(primary, fmt.Errorf("rollback failed; manual recovery may be required: %w", rollback))
}

func (m *lifecycleManager) restoreBinary(ctx context.Context, backupPath, tempPath string, restart, stopBeforeRestore bool) error {
	rollbackCtx := context.WithoutCancel(ctx)
	var restoreErrs []error
	serviceStopped := true
	if restart || stopBeforeRestore {
		if err := m.svcMgr.Stop(rollbackCtx, serviceName); err != nil {
			serviceStopped = false
			restoreErrs = append(restoreErrs, fmt.Errorf("stop service before restore: %w", err))
		}
	}

	binaryRestored := true
	if err := m.fs.Remove(binaryPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		binaryRestored = false
		restoreErrs = append(restoreErrs, fmt.Errorf("remove new binary: %w", err))
	}
	if err := m.fs.Rename(backupPath, binaryPath); err != nil {
		binaryRestored = false
		restoreErrs = append(restoreErrs, fmt.Errorf("restore old binary: %w", err))
	}
	if tempPath != "" {
		if err := m.fs.Remove(tempPath); err != nil {
			restoreErrs = append(restoreErrs, fmt.Errorf("remove temporary binary: %w", err))
		}
	}

	if serviceStopped && binaryRestored {
		if restart {
			if err := m.svcMgr.Start(rollbackCtx, serviceName); err != nil {
				restoreErrs = append(restoreErrs, fmt.Errorf("restart service: %w", err))
			} else if running, err := m.svcMgr.IsRunning(rollbackCtx, serviceName); err != nil {
				restoreErrs = append(restoreErrs, fmt.Errorf("confirm restored service running: %w", err))
			} else if !running {
				restoreErrs = append(restoreErrs, errors.New("restored service is not running"))
			}
		} else if running, err := m.svcMgr.IsRunning(rollbackCtx, serviceName); err != nil {
			restoreErrs = append(restoreErrs, fmt.Errorf("confirm restored service stopped: %w", err))
		} else if running {
			restoreErrs = append(restoreErrs, errors.New("restored service is running"))
		}
	}
	return errors.Join(restoreErrs...)
}

func (m *lifecycleManager) ListVersions(ctx context.Context) ([]VersionInfo, error) {
	versions, err := m.source.ListVersions(ctx, "MetaCubeX", "mihomo", 5)
	if err != nil {
		return nil, fmt.Errorf("list versions: %w", err)
	}
	if len(versions) == 0 {
		return nil, errors.New("no versions available")
	}
	return versions, nil
}
