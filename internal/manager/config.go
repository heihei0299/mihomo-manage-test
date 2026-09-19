package manager

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const legacyTemplatePath = "/opt/mihomo/etc/config-template.yaml"

type ConfigValidator interface {
	Validate(ctx context.Context, configPath string) error
}

type ConfigUpdateLock interface {
	Acquire(ctx context.Context) (release func(), err error)
}

type configManagerOption func(*configPipelineOptions)

func WithConfigUpdateLock(lock ConfigUpdateLock) configManagerOption {
	return func(opts *configPipelineOptions) { opts.Lock = lock }
}

type noopConfigUpdateLock struct{}

func (noopConfigUpdateLock) Acquire(context.Context) (func(), error) {
	return func() {}, nil
}

type configValidator struct {
	cmd CommandRunner
}

func (v *configValidator) Validate(ctx context.Context, configPath string) error {
	cmd := v.cmd
	if cmd == nil {
		cmd = OSSystem{}
	}
	out, err := cmd.RunCommand(ctx, binaryPath, "-t", "-d", filepath.Dir(configPath))
	if err == nil {
		return nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if strings.TrimSpace(out) == "" {
		return fmt.Errorf("config validation failed: %w", err)
	}
	return fmt.Errorf("config validation failed: %w: %s", err, strings.TrimSpace(out))
}

type configPipelineOptions struct {
	OnReload  func(ctx context.Context) error
	Validator ConfigValidator
	Warn      func(msg string)
	Lock      ConfigUpdateLock
}

type configPipeline struct {
	fs       FileSystem
	source   ReleaseSource
	onReload func(ctx context.Context) error
	validate ConfigValidator
	warn     func(msg string)
	lock     ConfigUpdateLock
}

func newConfigPipeline(fs FileSystem, source ReleaseSource, opts configPipelineOptions) *configPipeline {
	p := &configPipeline{fs: fs, source: source}
	if opts.OnReload != nil {
		p.onReload = opts.OnReload
	}
	if opts.Validator != nil {
		p.validate = opts.Validator
	}
	if opts.Warn != nil {
		p.warn = opts.Warn
	} else {
		p.warn = func(msg string) { fmt.Fprintln(os.Stderr, "warning:", msg) }
	}
	if opts.Lock != nil {
		p.lock = opts.Lock
	} else {
		p.lock = noopConfigUpdateLock{}
	}
	if err := p.recoverConfigTransaction(); err != nil {
		p.warn(fmt.Sprintf("failed to recover config transaction: %v", err))
	}
	p.migrateLegacyTemplate()
	return p
}

func (p *configPipeline) acquireConfigUpdate(ctx context.Context) (func(), error) {
	release, err := p.lock.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		release()
		return nil, err
	}
	return release, nil
}

// migrateLegacyTemplate renames the old config-template.yaml to the override
// file on first use, so existing setups carry over without manual steps. It
// runs once: after a successful rename the legacy path no longer exists.
func (p *configPipeline) migrateLegacyTemplate() {
	if !p.fs.FileExists(legacyTemplatePath) || p.fs.FileExists(OverrideFilePath) {
		return
	}
	release, err := p.acquireConfigUpdate(context.Background())
	if err != nil {
		p.warn(fmt.Sprintf("failed to acquire config update lock for %s: %v", legacyTemplatePath, err))
		return
	}
	defer release()
	p.migrateLegacyTemplateLocked()
}

func (p *configPipeline) migrateLegacyTemplateLocked() {
	if !p.fs.FileExists(legacyTemplatePath) || p.fs.FileExists(OverrideFilePath) {
		return
	}
	if err := p.fs.Rename(legacyTemplatePath, OverrideFilePath); err != nil {
		p.warn(fmt.Sprintf("failed to migrate %s: %v", legacyTemplatePath, err))
		return
	}
	p.warn("migrated config-template.yaml to override.yaml. The old file name is no longer recognized.")
}

const (
	configTransactionPrepared        = "prepared"
	configTransactionConfigCommitted = "config-committed"
	configTransactionCommitted       = "committed"
)

type configTransactionState struct {
	State                  string `json:"state"`
	ConfigBackup           string `json:"config_backup,omitempty"`
	ConfigExisted          bool   `json:"config_existed"`
	SubscriptionBackup     string `json:"subscription_backup,omitempty"`
	SubscriptionStaged     bool   `json:"subscription_staged"`
	SubscriptionExisted    bool   `json:"subscription_existed"`
	StagedConfigDir        string `json:"staged_config_dir,omitempty"`
	StagedSubscriptionPath string `json:"staged_subscription_path,omitempty"`
}

func (p *configPipeline) writeConfigTransaction(transaction configTransactionState) error {
	data, err := json.Marshal(transaction)
	if err != nil {
		return fmt.Errorf("encoding config transaction: %w", err)
	}
	if err := p.fs.MkdirAll(stateDir, filePermUserRWX); err != nil {
		return fmt.Errorf("creating state directory: %w", err)
	}
	tmpPath := configApplyTransactionFile + ".tmp"
	if err := p.fs.WriteFile(tmpPath, data, filePermUserRW); err != nil {
		return fmt.Errorf("writing config transaction: %w", err)
	}
	if err := p.fs.Rename(tmpPath, configApplyTransactionFile); err != nil {
		return errors.Join(fmt.Errorf("committing config transaction: %w", err), p.fs.Remove(tmpPath))
	}
	return nil
}

func (p *configPipeline) restoreTransactionFile(path, backup string, existed bool) error {
	if !existed {
		return p.fs.Remove(path)
	}
	data, err := p.fs.ReadFile(backup)
	if err != nil {
		return fmt.Errorf("reading backup %s: %w", backup, err)
	}
	return p.fs.WriteFile(path, data, filePermUserRW)
}

func (p *configPipeline) cleanupConfigTransaction(transaction configTransactionState, removeMarker bool) error {
	paths := []string{
		transaction.StagedConfigDir,
		transaction.StagedSubscriptionPath,
		transaction.SubscriptionBackup,
	}
	if removeMarker {
		paths = append(paths, configApplyTransactionFile)
	}
	var cleanupErrs []error
	for _, path := range paths {
		if path == "" {
			continue
		}
		if err := p.fs.Remove(path); err != nil {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("cleanup transaction artifact %s: %w", path, err))
		}
	}
	return errors.Join(cleanupErrs...)
}

func (p *configPipeline) recoverConfigTransactionLocked() error {
	data, err := p.fs.ReadFile(configApplyTransactionFile)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading config transaction: %w", err)
	}
	var transaction configTransactionState
	if err := json.Unmarshal(data, &transaction); err != nil {
		return fmt.Errorf("decoding config transaction: %w", err)
	}
	if transaction.State == configTransactionCommitted {
		return p.cleanupConfigTransaction(transaction, true)
	}
	if transaction.State != configTransactionPrepared && transaction.State != configTransactionConfigCommitted {
		return fmt.Errorf("unknown config transaction state %q", transaction.State)
	}
	var restoreErrs []error
	if err := p.restoreTransactionFile(configYAML, transaction.ConfigBackup, transaction.ConfigExisted); err != nil {
		restoreErrs = append(restoreErrs, fmt.Errorf("restore config: %w", err))
	}
	if transaction.SubscriptionStaged {
		if err := p.restoreTransactionFile(subscriptionDataFile, transaction.SubscriptionBackup, transaction.SubscriptionExisted); err != nil {
			restoreErrs = append(restoreErrs, fmt.Errorf("restore subscription data: %w", err))
		}
	}
	if len(restoreErrs) > 0 {
		return errors.Join(restoreErrs...)
	}
	return p.cleanupConfigTransaction(transaction, true)
}

func (p *configPipeline) recoverConfigTransaction() error {
	if !p.fs.FileExists(configApplyTransactionFile) {
		if _, err := p.fs.ReadFile(configApplyTransactionFile); os.IsNotExist(err) {
			return nil
		}
	}
	release, err := p.acquireConfigUpdate(context.Background())
	if err != nil {
		return err
	}
	defer release()
	return p.recoverConfigTransactionLocked()
}

func renderConfig(template, subscription, routingRules string) (string, error) {
	result := strings.ReplaceAll(template, "{{subscription}}", subscription)
	result = strings.ReplaceAll(result, "{{routing_rules}}", routingRules)
	return result, nil
}

type fileSnapshot struct {
	data   []byte
	exists bool
}

func (p *configPipeline) snapshotFile(path string) (fileSnapshot, error) {
	data, err := p.fs.ReadFile(path)
	if err == nil {
		return fileSnapshot{data: data, exists: true}, nil
	}
	if os.IsNotExist(err) {
		return fileSnapshot{}, nil
	}
	return fileSnapshot{}, err
}

func (p *configPipeline) restoreFile(path string, snapshot fileSnapshot) error {
	if snapshot.exists {
		return p.fs.WriteFile(path, snapshot.data, filePermUserRW)
	}
	return p.fs.Remove(path)
}

func (p *configPipeline) restoreSubscriptionState(snapshots map[string]fileSnapshot) error {
	var restoreErrs []error
	for _, path := range []string{subscriptionDataFile, subscriptionURLFile, subscriptionSourceFile} {
		if err := p.restoreFile(path, snapshots[path]); err != nil {
			restoreErrs = append(restoreErrs, fmt.Errorf("restore %s: %w", path, err))
		}
	}
	return errors.Join(restoreErrs...)
}

func looksLikeURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

func (p *configPipeline) SetSubscriptionSource(ctx context.Context, source string) error {
	release, err := p.acquireConfigUpdate(ctx)
	if err != nil {
		return err
	}
	defer release()

	if err := p.fs.MkdirAll(stateDir, filePermUserRWX); err != nil {
		return fmt.Errorf("creating state directory: %w", err)
	}

	trimmed := strings.TrimSpace(source)
	if trimmed == "" {
		return fmt.Errorf("subscription source cannot be empty")
	}

	snapshots := make(map[string]fileSnapshot, 3)
	for _, path := range []string{subscriptionDataFile, subscriptionURLFile, subscriptionSourceFile} {
		snapshot, err := p.snapshotFile(path)
		if err != nil {
			return fmt.Errorf("reading subscription state: %w", err)
		}
		snapshots[path] = snapshot
	}

	rollback := func(err error) error {
		if restoreErr := p.restoreSubscriptionState(snapshots); restoreErr != nil {
			return errors.Join(err, restoreErr)
		}
		return err
	}

	if looksLikeURL(trimmed) {
		if err := p.fs.WriteFile(subscriptionURLFile, []byte(trimmed), filePermUserRW); err != nil {
			return rollback(err)
		}
		if err := p.fs.Remove(subscriptionDataFile); err != nil {
			return rollback(fmt.Errorf("removing local subscription data: %w", err))
		}
		if err := p.fs.WriteFile(subscriptionSourceFile, []byte(remoteSubscriptionSource+"\n"), filePermUserRW); err != nil {
			return rollback(fmt.Errorf("recording subscription source: %w", err))
		}
		return nil
	}

	if err := p.fs.WriteFile(subscriptionDataFile, []byte(source), filePermUserRW); err != nil {
		return rollback(err)
	}
	if err := p.fs.Remove(subscriptionURLFile); err != nil {
		return rollback(fmt.Errorf("removing remote subscription URL: %w", err))
	}
	if err := p.fs.WriteFile(subscriptionSourceFile, []byte(localSubscriptionSource+"\n"), filePermUserRW); err != nil {
		return rollback(fmt.Errorf("recording subscription source: %w", err))
	}
	return nil
}

const (
	remoteSubscriptionSource = "remote"
	localSubscriptionSource  = "local"
)

func (p *configPipeline) filePresent(path string) bool {
	if p.fs.FileExists(path) {
		return true
	}
	_, err := p.fs.ReadFile(path)
	return err == nil
}

func (p *configPipeline) requireSourceValue(path, missingMessage, emptyMessage string) error {
	data, err := p.fs.ReadFile(path)
	if err != nil {
		return fmt.Errorf("%s: %w", missingMessage, err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return fmt.Errorf("%s", emptyMessage)
	}
	return nil
}

func (p *configPipeline) subscriptionSource() (string, error) {
	marker, err := p.fs.ReadFile(subscriptionSourceFile)
	if err == nil {
		source := strings.TrimSpace(string(marker))
		switch source {
		case remoteSubscriptionSource:
			if !p.filePresent(subscriptionURLFile) {
				return "", fmt.Errorf("remote subscription source is configured but its URL is missing")
			}
			if err := p.requireSourceValue(subscriptionURLFile, "reading remote subscription URL", "remote subscription URL is empty"); err != nil {
				return "", err
			}
		case localSubscriptionSource:
			if !p.filePresent(subscriptionDataFile) {
				return "", fmt.Errorf("local subscription source is configured but its data is missing")
			}
			if err := p.requireSourceValue(subscriptionDataFile, "reading local subscription data", "local subscription data is empty"); err != nil {
				return "", err
			}
		default:
			return "", fmt.Errorf("invalid subscription source %q", source)
		}
		return source, nil
	}
	if !os.IsNotExist(err) {
		return "", fmt.Errorf("reading subscription source: %w", err)
	}

	hasRemote := p.filePresent(subscriptionURLFile)
	hasLocal := p.filePresent(subscriptionDataFile)
	switch {
	case hasRemote && hasLocal:
		return "", fmt.Errorf("conflicting legacy subscription sources; choose remote or local explicitly")
	case hasRemote:
		if err := p.requireSourceValue(subscriptionURLFile, "reading legacy remote subscription URL", "legacy remote subscription URL is empty"); err != nil {
			return "", err
		}
		if err := p.fs.WriteFile(subscriptionSourceFile, []byte(remoteSubscriptionSource+"\n"), filePermUserRW); err != nil {
			return "", fmt.Errorf("migrating remote subscription source: %w", err)
		}
		return remoteSubscriptionSource, nil
	case hasLocal:
		if err := p.requireSourceValue(subscriptionDataFile, "reading legacy local subscription data", "legacy local subscription data is empty"); err != nil {
			return "", err
		}
		if err := p.fs.WriteFile(subscriptionSourceFile, []byte(localSubscriptionSource+"\n"), filePermUserRW); err != nil {
			return "", fmt.Errorf("migrating local subscription source: %w", err)
		}
		return localSubscriptionSource, nil
	default:
		return "", nil
	}
}

func (p *configPipeline) PreviewConfig(ctx context.Context) (string, error) {
	release, err := p.acquireConfigUpdate(ctx)
	if err != nil {
		return "", err
	}
	defer release()
	return p.previewConfig(ctx, nil)
}

func (p *configPipeline) previewConfig(ctx context.Context, candidate *stagedSubscription) (string, error) {
	p.migrateLegacyTemplateLocked()
	if _, err := p.subscriptionSource(); err != nil {
		return "", err
	}
	var subData []byte
	var err error
	if candidate != nil {
		subData = candidate.data
	} else {
		subData, err = p.fs.ReadFile(subscriptionDataFile)
		if err != nil && !os.IsNotExist(err) {
			return "", err
		}
	}

	tmpl, tmplErr := p.fs.ReadFile(OverrideFilePath)
	if tmplErr != nil && !os.IsNotExist(tmplErr) {
		return "", tmplErr
	}

	tmplStr := ""
	if tmplErr == nil {
		tmplStr = string(tmpl)
	}

	subStr := ""
	if err == nil {
		subStr = string(subData)
	}

	if strings.Contains(tmplStr, "{{subscription}}") || strings.Contains(tmplStr, "{{routing_rules}}") {
		p.warn("config-template.yaml uses old placeholder format. Please migrate to YAML overlay format.")
	}

	return mergeConfig(subStr, tmplStr)
}

func configContentHash(content string) string {
	hash := sha256.Sum256([]byte(content))
	return hex.EncodeToString(hash[:])
}

func (p *configPipeline) writeConfigApplyStatus(status ConfigApplyStatus) error {
	data, err := json.Marshal(status)
	if err != nil {
		return fmt.Errorf("encoding config apply status: %w", err)
	}
	if err := p.fs.MkdirAll(stateDir, filePermUserRWX); err != nil {
		return fmt.Errorf("creating state directory: %w", err)
	}
	tmpPath := configApplyStatusFile + ".tmp"
	if err := p.fs.WriteFile(tmpPath, data, filePermUserRW); err != nil {
		return fmt.Errorf("writing config apply status: %w", err)
	}
	if err := p.fs.Rename(tmpPath, configApplyStatusFile); err != nil {
		p.fs.Remove(tmpPath)
		return fmt.Errorf("committing config apply status: %w", err)
	}
	return nil
}

func (p *configPipeline) recordConfigApply(state ConfigApplyState, preview string, applyErr error, subscriptionData []byte) error {
	status := ConfigApplyStatus{
		State:       state,
		AttemptedAt: time.Now().UTC(),
		ConfigHash:  configContentHash(preview),
	}
	if len(subscriptionData) > 0 {
		status.SubscriptionHash = configContentHash(string(subscriptionData))
	}
	if applyErr != nil {
		status.ErrorSummary = applyErr.Error()
	}
	return p.writeConfigApplyStatus(status)
}

type stagedConfig struct {
	dir  string
	path string
}

type stagedSubscription struct {
	path string
	data []byte
}

func (p *configPipeline) refreshSubscription(ctx context.Context) (*stagedSubscription, error) {
	source, err := p.subscriptionSource()
	if err != nil {
		return nil, err
	}
	if source == "" {
		return nil, ErrSubscriptionSourceNotConfigured
	}
	if source != remoteSubscriptionSource {
		return nil, nil
	}

	data, err := p.fs.ReadFile(subscriptionURLFile)
	if err != nil {
		return nil, fmt.Errorf("reading subscription URL: %w", err)
	}
	url := strings.TrimSpace(string(data))
	if url == "" {
		return nil, nil
	}

	tmpPath := subscriptionDataFile + ".tmp"
	cleanupDownload := func(primary error) error {
		return errors.Join(primary, p.fs.Remove(tmpPath))
	}
	if err := p.source.Download(ctx, url, tmpPath); err != nil {
		return nil, cleanupDownload(fmt.Errorf("fetching subscription: %w", err))
	}
	fetched, err := p.fs.ReadFile(tmpPath)
	if err != nil {
		return nil, cleanupDownload(err)
	}
	if len(bytes.TrimSpace(fetched)) == 0 {
		return nil, cleanupDownload(fmt.Errorf("fetched subscription content is empty"))
	}
	return &stagedSubscription{path: tmpPath, data: fetched}, nil
}

func (p *configPipeline) cleanupStagedSubscription(candidate *stagedSubscription, primary error) error {
	if candidate == nil {
		return primary
	}
	if cleanupErr := p.fs.Remove(candidate.path); cleanupErr != nil {
		return errors.Join(primary, fmt.Errorf("cleanup subscription staging: %w", cleanupErr))
	}
	return primary
}

func (p *configPipeline) cleanupApplyStaging(staged stagedConfig, candidate *stagedSubscription, primary error) error {
	return p.cleanupStagedSubscription(candidate, p.cleanupStagedConfig(staged, primary))
}

func (p *configPipeline) buildApplyPreview(ctx context.Context, candidate *stagedSubscription) (string, error) {
	preview, err := p.previewConfig(ctx, candidate)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(preview) == "" {
		return "", fmt.Errorf("generated config is empty")
	}
	return preview, nil
}

func (p *configPipeline) stageConfig(ctx context.Context, preview string) (stagedConfig, error) {
	staged := stagedConfig{
		dir: filepath.Join(configDir, fmt.Sprintf(".mihomo-config-staging-%d", time.Now().UnixNano())),
	}
	staged.path = filepath.Join(staged.dir, "config.yaml")
	if err := p.fs.MkdirAll(staged.dir, filePermUserRWX); err != nil {
		return stagedConfig{}, fmt.Errorf("creating config staging directory: %w", err)
	}
	if err := p.fs.WriteFile(staged.path, []byte(preview), filePermUserRW); err != nil {
		return stagedConfig{}, p.cleanupStagedConfig(staged, fmt.Errorf("writing staged config: %w", err))
	}
	if err := ctx.Err(); err != nil {
		return stagedConfig{}, p.cleanupStagedConfig(staged, err)
	}
	return staged, nil
}

func (p *configPipeline) cleanupStagedConfig(staged stagedConfig, primary error) error {
	if cleanupErr := p.fs.Remove(staged.dir); cleanupErr != nil {
		return errors.Join(primary, fmt.Errorf("cleanup staged config: %w", cleanupErr))
	}
	return primary
}

func (p *configPipeline) commitConfig(staged stagedConfig, candidate *stagedSubscription) (postCommitCleanupErr, applyErr error) {
	configSnapshot, err := p.snapshotFile(configYAML)
	if err != nil {
		return nil, p.cleanupApplyStaging(staged, candidate, err)
	}
	subscriptionSnapshot := fileSnapshot{}
	if candidate != nil {
		subscriptionSnapshot, err = p.snapshotFile(subscriptionDataFile)
		if err != nil {
			return nil, p.cleanupApplyStaging(staged, candidate, err)
		}
		if err := p.fs.Chmod(candidate.path, filePermUserRW); err != nil {
			return nil, p.cleanupApplyStaging(staged, candidate, fmt.Errorf("preparing subscription data: %w", err))
		}
	}

	transaction := configTransactionState{
		State:               configTransactionPrepared,
		ConfigExisted:       configSnapshot.exists,
		SubscriptionStaged:  candidate != nil,
		SubscriptionExisted: subscriptionSnapshot.exists,
		StagedConfigDir:     staged.dir,
	}
	if candidate != nil {
		transaction.StagedSubscriptionPath = candidate.path
	}
	if configSnapshot.exists {
		transaction.ConfigBackup = fmt.Sprintf("%s.bak.%d", configYAML, time.Now().UnixNano())
		if err := p.fs.WriteFile(transaction.ConfigBackup, configSnapshot.data, filePermUserRW); err != nil {
			return nil, p.cleanupTransactionBeforeCommit(staged, candidate, transaction, err)
		}
	}
	if candidate != nil && subscriptionSnapshot.exists {
		transaction.SubscriptionBackup = fmt.Sprintf("%s.bak.%d", subscriptionDataFile, time.Now().UnixNano())
		if err := p.fs.WriteFile(transaction.SubscriptionBackup, subscriptionSnapshot.data, filePermUserRW); err != nil {
			return nil, p.cleanupTransactionBeforeCommit(staged, candidate, transaction, err)
		}
	}
	if err := p.writeConfigTransaction(transaction); err != nil {
		return nil, p.cleanupTransactionBeforeCommit(staged, candidate, transaction, err)
	}

	rollback := func(primary error) error {
		var restoreErrs []error
		if err := p.restoreTransactionFile(configYAML, transaction.ConfigBackup, transaction.ConfigExisted); err != nil {
			restoreErrs = append(restoreErrs, fmt.Errorf("restore config: %w", err))
		}
		if candidate != nil {
			if err := p.restoreTransactionFile(subscriptionDataFile, transaction.SubscriptionBackup, transaction.SubscriptionExisted); err != nil {
				restoreErrs = append(restoreErrs, fmt.Errorf("restore subscription data: %w", err))
			}
		}
		if len(restoreErrs) == 0 {
			return errors.Join(primary, p.cleanupConfigTransaction(transaction, true))
		}
		return errors.Join(primary, errors.Join(restoreErrs...), p.cleanupStagedConfig(staged, p.cleanupStagedSubscription(candidate, nil)))
	}

	if err := p.fs.Rename(staged.path, configYAML); err != nil {
		return nil, rollback(fmt.Errorf("committing generated config: %w", err))
	}
	transaction.State = configTransactionConfigCommitted
	if err := p.writeConfigTransaction(transaction); err != nil {
		return nil, rollback(fmt.Errorf("recording config commit: %w", err))
	}
	if candidate != nil {
		if err := p.fs.Rename(candidate.path, subscriptionDataFile); err != nil {
			return nil, rollback(fmt.Errorf("committing subscription data: %w", err))
		}
	}
	transaction.State = configTransactionCommitted
	if err := p.writeConfigTransaction(transaction); err != nil {
		return nil, rollback(fmt.Errorf("recording subscription commit: %w", err))
	}
	return p.cleanupConfigTransaction(transaction, true), nil
}

func (p *configPipeline) cleanupTransactionBeforeCommit(staged stagedConfig, candidate *stagedSubscription, transaction configTransactionState, primary error) error {
	failure := p.cleanupApplyStaging(staged, candidate, primary)
	var cleanupErrs []error
	if transaction.SubscriptionBackup != "" {
		if err := p.fs.Remove(transaction.SubscriptionBackup); err != nil {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("cleanup transaction artifact %s: %w", transaction.SubscriptionBackup, err))
		}
	}
	return errors.Join(failure, errors.Join(cleanupErrs...))
}

func (p *configPipeline) UpdateConfig(ctx context.Context) (applyErr error) {
	release, err := p.acquireConfigUpdate(ctx)
	if err != nil {
		return err
	}
	defer release()

	preview := ""
	var candidate *stagedSubscription
	statusRecorded := false
	candidateData := func() []byte {
		if candidate == nil {
			return nil
		}
		return candidate.data
	}
	defer func() {
		if applyErr != nil && !statusRecorded {
			statusErr := p.recordConfigApply(ConfigApplyFailed, preview, applyErr, candidateData())
			statusRecorded = true
			if statusErr != nil {
				applyErr = errors.Join(applyErr, statusErr)
			}
		}
	}()

	if err := p.recoverConfigTransactionLocked(); err != nil {
		return err
	}
	candidate, err = p.refreshSubscription(ctx)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return p.cleanupStagedSubscription(candidate, err)
	}
	preview, err = p.buildApplyPreview(ctx, candidate)
	if err != nil {
		return p.cleanupStagedSubscription(candidate, err)
	}

	staged, err := p.stageConfig(ctx, preview)
	if err != nil {
		return p.cleanupStagedSubscription(candidate, err)
	}

	if p.validate != nil {
		if err := p.validate.Validate(ctx, staged.path); err != nil {
			failure := p.cleanupApplyStaging(staged, candidate, err)
			state := ConfigValidationFailed
			if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				state = ConfigApplyFailed
			}
			statusErr := p.recordConfigApply(state, preview, failure, candidateData())
			statusRecorded = true
			if statusErr != nil {
				failure = errors.Join(failure, statusErr)
			}
			return failure
		}
	}
	if err := ctx.Err(); err != nil {
		return p.cleanupApplyStaging(staged, candidate, err)
	}

	postCommitCleanupErr, applyErr := p.commitConfig(staged, candidate)
	if applyErr != nil {
		return applyErr
	}

	if p.onReload != nil {
		if err := p.onReload(ctx); err != nil {
			failure := err
			if postCommitCleanupErr != nil {
				failure = errors.Join(failure, fmt.Errorf("cleanup staged config: %w", postCommitCleanupErr))
			}
			statusErr := p.recordConfigApply(ConfigPendingReload, preview, failure, candidateData())
			statusRecorded = true
			if statusErr != nil {
				failure = errors.Join(failure, statusErr)
			}
			return failure
		}
	}

	if postCommitCleanupErr != nil {
		statusErr := p.recordConfigApply(ConfigApplied, preview, postCommitCleanupErr, candidateData())
		statusRecorded = true
		if statusErr != nil {
			return errors.Join(postCommitCleanupErr, statusErr)
		}
		return postCommitCleanupErr
	}
	statusErr := p.recordConfigApply(ConfigApplied, preview, nil, candidateData())
	statusRecorded = true
	return statusErr
}

func (p *configPipeline) LastConfigApply(ctx context.Context) (ConfigApplyStatus, error) {
	data, err := p.fs.ReadFile(configApplyStatusFile)
	if os.IsNotExist(err) {
		return ConfigApplyStatus{State: ConfigUnknown}, nil
	}
	if err != nil {
		return ConfigApplyStatus{State: ConfigUnknown, ErrorSummary: err.Error()}, err
	}
	var status ConfigApplyStatus
	if err := json.Unmarshal(data, &status); err != nil {
		return ConfigApplyStatus{State: ConfigUnknown, ErrorSummary: fmt.Sprintf("invalid status file: %v", err)}, nil
	}
	switch status.State {
	case ConfigApplied, ConfigPendingReload, ConfigValidationFailed, ConfigApplyFailed:
		return status, nil
	default:
		return ConfigApplyStatus{State: ConfigUnknown, ErrorSummary: "unknown config apply state"}, nil
	}
}

// Validate runs the configured ConfigValidator against the generated config.
// A nil validator means validation is a no-op (validation not configured).
func (p *configPipeline) ValidateConfig(ctx context.Context) error {
	if p.validate == nil {
		return nil
	}
	return p.validate.Validate(ctx, configYAML)
}
