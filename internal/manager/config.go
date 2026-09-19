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
	if v.cmd == nil {
		return errors.New("config validator command runner is not configured")
	}
	out, err := v.cmd.RunCommand(ctx, binaryPath, "-t", "-d", filepath.Dir(configPath))
	if err == nil {
		return nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if diagnostic := strings.TrimSpace(out); diagnostic != "" {
		return fmt.Errorf("config validation failed: %w: %s", err, diagnostic)
	}
	return fmt.Errorf("config validation failed: %w", err)
}

type configPipelineOptions struct {
	OnReload  func(ctx context.Context) error
	Validator ConfigValidator
	Warn      func(msg string)
	Lock      ConfigUpdateLock
}

type configPipeline struct {
	fs          FileSystem
	source      ReleaseSource
	onReload    func(ctx context.Context) error
	validate    ConfigValidator
	warn        func(msg string)
	lock        ConfigUpdateLock
	recoveryErr error
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
	if err := p.recoverConfigTransaction(context.Background()); err != nil {
		p.recoveryErr = err
		p.warn(fmt.Sprintf("config recovery failed: %v", err))
	} else {
		p.migrateLegacyTemplate(context.Background())
	}
	return p
}

// migrateLegacyTemplate keeps the legacy rename inside the same write lock as
// the other config state changes. A failed lock acquisition is retried on the
// next public config operation.
func (p *configPipeline) migrateLegacyTemplate(ctx context.Context) {
	if !p.fs.FileExists(legacyTemplatePath) || p.fs.FileExists(OverrideFilePath) {
		return
	}
	release, err := p.lock.Acquire(ctx)
	if err != nil {
		p.warn(fmt.Sprintf("failed to acquire config update lock for migration: %v", err))
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

func renderConfig(template, subscription, routingRules string) (string, error) {
	result := strings.ReplaceAll(template, "{{subscription}}", subscription)
	result = strings.ReplaceAll(result, "{{routing_rules}}", routingRules)
	return result, nil
}

type fileSnapshot struct {
	data   []byte
	exists bool
}

type configTransactionPhase string

const (
	transactionPrepared        configTransactionPhase = "prepared"
	transactionConfigCommitted configTransactionPhase = "config-committed"
	transactionCommitted       configTransactionPhase = "committed"
)

type transactionSnapshot struct {
	Data   []byte `json:"data,omitempty"`
	Exists bool   `json:"exists"`
}

type configTransactionRecord struct {
	Phase            configTransactionPhase `json:"phase"`
	StagingDir       string                 `json:"staging_dir"`
	CandidatePath    string                 `json:"candidate_path,omitempty"`
	BackupPath       string                 `json:"backup_path,omitempty"`
	Config           transactionSnapshot    `json:"config"`
	SubscriptionData transactionSnapshot    `json:"subscription_data"`
}

func transactionSnapshotOf(snapshot fileSnapshot) transactionSnapshot {
	return transactionSnapshot{Data: snapshot.data, Exists: snapshot.exists}
}

func (p *configPipeline) writeConfigTransaction(record configTransactionRecord) error {
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encoding config transaction: %w", err)
	}
	if err := p.fs.MkdirAll(stateDir, filePermUserRWX); err != nil {
		return fmt.Errorf("creating config transaction directory: %w", err)
	}
	tmpPath := configApplyTransactionFile + ".tmp"
	if err := p.fs.WriteFile(tmpPath, data, filePermUserRW); err != nil {
		return errors.Join(fmt.Errorf("writing config transaction: %w", err), p.fs.Remove(tmpPath))
	}
	if err := p.fs.Rename(tmpPath, configApplyTransactionFile); err != nil {
		return errors.Join(fmt.Errorf("committing config transaction: %w", err), p.fs.Remove(tmpPath))
	}
	return nil
}

func (p *configPipeline) clearConfigTransaction() error {
	if err := p.fs.Remove(configApplyTransactionFile); err != nil {
		return fmt.Errorf("removing config transaction: %w", err)
	}
	return nil
}

func (p *configPipeline) recoverConfigTransaction(ctx context.Context) error {
	data, err := p.fs.ReadFile(configApplyTransactionFile)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading config transaction: %w", err)
	}
	var record configTransactionRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return fmt.Errorf("decoding config transaction: %w", err)
	}
	release, err := p.lock.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquiring config recovery lock: %w", err)
	}
	defer release()

	data, err = p.fs.ReadFile(configApplyTransactionFile)
	if err != nil {
		return fmt.Errorf("reading config transaction under lock: %w", err)
	}
	if err := json.Unmarshal(data, &record); err != nil {
		return fmt.Errorf("decoding config transaction under lock: %w", err)
	}
	if record.Phase == transactionCommitted {
		var cleanupErrs []error
		for _, path := range []string{record.StagingDir, record.CandidatePath} {
			if path == "" {
				continue
			}
			if err := p.fs.Remove(path); err != nil {
				cleanupErrs = append(cleanupErrs, fmt.Errorf("cleanup committed artifact: %w", err))
			}
		}
		if err := p.clearConfigTransaction(); err != nil {
			cleanupErrs = append(cleanupErrs, err)
		}
		return errors.Join(cleanupErrs...)
	}
	if record.Phase != transactionPrepared && record.Phase != transactionConfigCommitted {
		return fmt.Errorf("unknown config transaction phase %q", record.Phase)
	}

	var recoveryErrs []error
	if err := p.restoreFile(configYAML, fileSnapshot{data: record.Config.Data, exists: record.Config.Exists}); err != nil {
		recoveryErrs = append(recoveryErrs, fmt.Errorf("restore config: %w", err))
	}
	if err := p.restoreFile(subscriptionDataFile, fileSnapshot{data: record.SubscriptionData.Data, exists: record.SubscriptionData.Exists}); err != nil {
		recoveryErrs = append(recoveryErrs, fmt.Errorf("restore subscription data: %w", err))
	}
	for _, path := range []string{record.StagingDir, record.CandidatePath, record.BackupPath} {
		if path == "" {
			continue
		}
		if err := p.fs.Remove(path); err != nil {
			recoveryErrs = append(recoveryErrs, fmt.Errorf("cleanup %s: %w", path, err))
		}
	}
	if len(recoveryErrs) > 0 {
		return errors.Join(recoveryErrs...)
	}
	return p.clearConfigTransaction()
}

func (p *configPipeline) ensureConfigRecovery() error {
	if p.recoveryErr != nil {
		return fmt.Errorf("configuration recovery is incomplete: %w", p.recoveryErr)
	}
	return nil
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
	if err := p.ensureConfigRecovery(); err != nil {
		return err
	}
	trimmed := strings.TrimSpace(source)
	if trimmed == "" {
		return fmt.Errorf("subscription source cannot be empty")
	}

	release, err := p.lock.Acquire(ctx)
	if err != nil {
		return err
	}
	defer release()

	if err := p.fs.MkdirAll(stateDir, filePermUserRWX); err != nil {
		return fmt.Errorf("creating state directory: %w", err)
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
	if err := p.ensureConfigRecovery(); err != nil {
		return "", err
	}
	release, err := p.lock.Acquire(ctx)
	if err != nil {
		return "", err
	}
	defer release()
	return p.previewConfig(ctx)
}

func (p *configPipeline) previewConfig(ctx context.Context) (string, error) {
	return p.previewConfigWithCandidate(ctx, nil)
}

func (p *configPipeline) previewConfigWithCandidate(ctx context.Context, candidate *subscriptionCandidate) (string, error) {
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
	}
	if err != nil && !os.IsNotExist(err) {
		return "", err
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

func (p *configPipeline) recordConfigApply(state ConfigApplyState, preview string, applyErr error) error {
	status := ConfigApplyStatus{
		State:       state,
		AttemptedAt: time.Now().UTC(),
		ConfigHash:  configContentHash(preview),
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

type subscriptionCandidate struct {
	path string
	data []byte
}

func candidatePath(candidate *subscriptionCandidate) string {
	if candidate == nil {
		return ""
	}
	return candidate.path
}

func (p *configPipeline) refreshSubscription(ctx context.Context) (*subscriptionCandidate, error) {
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
	if err := p.fs.Remove(tmpPath); err != nil {
		return nil, fmt.Errorf("removing stale subscription staging: %w", err)
	}
	cleanupDownload := func(primary error) error {
		return errors.Join(primary, p.fs.Remove(tmpPath))
	}
	if err := p.source.Download(ctx, url, tmpPath); err != nil {
		return nil, cleanupDownload(fmt.Errorf("fetching subscription: %w", err))
	}
	if err := ctx.Err(); err != nil {
		return nil, cleanupDownload(err)
	}
	fetched, err := p.fs.ReadFile(tmpPath)
	if err != nil {
		return nil, cleanupDownload(err)
	}
	if len(bytes.TrimSpace(fetched)) == 0 {
		return nil, cleanupDownload(fmt.Errorf("fetched subscription content is empty"))
	}
	return &subscriptionCandidate{path: tmpPath, data: fetched}, nil
}

func (p *configPipeline) buildApplyPreview(ctx context.Context, candidate *subscriptionCandidate) (string, error) {
	preview, err := p.previewConfigWithCandidate(ctx, candidate)
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

func (p *configPipeline) commitConfig(staged stagedConfig, candidate *subscriptionCandidate, oldConfig, oldSubscription fileSnapshot) (postCommitCleanupErr, applyErr error) {
	backupPath := ""
	transactionRecorded := false
	cleanupBeforeCommit := func(primary error) error {
		cleanupErrs := []error{p.fs.Remove(staged.dir)}
		if backupPath != "" {
			cleanupErrs = append(cleanupErrs, p.fs.Remove(backupPath))
		}
		var nonNil []error
		for _, cleanupErr := range cleanupErrs {
			if cleanupErr != nil {
				nonNil = append(nonNil, cleanupErr)
			}
		}
		if len(nonNil) == 0 {
			return primary
		}
		return errors.Join(primary, fmt.Errorf("cleanup before commit failed: %w", errors.Join(nonNil...)))
	}
	rollback := func() error {
		var rollbackErrs []error
		if err := p.restoreFile(configYAML, oldConfig); err != nil {
			rollbackErrs = append(rollbackErrs, fmt.Errorf("restore config: %w", err))
		}
		if err := p.restoreFile(subscriptionDataFile, oldSubscription); err != nil {
			rollbackErrs = append(rollbackErrs, fmt.Errorf("restore subscription data: %w", err))
		}
		if err := p.fs.Remove(staged.dir); err != nil {
			rollbackErrs = append(rollbackErrs, fmt.Errorf("cleanup staged config: %w", err))
		}
		if backupPath != "" {
			if err := p.fs.Remove(backupPath); err != nil {
				rollbackErrs = append(rollbackErrs, fmt.Errorf("cleanup config backup: %w", err))
			}
		}
		if transactionRecorded {
			if err := p.clearConfigTransaction(); err != nil {
				p.recoveryErr = err
				rollbackErrs = append(rollbackErrs, err)
			}
		}
		return errors.Join(rollbackErrs...)
	}

	if oldConfig.exists {
		backupPath = fmt.Sprintf("%s.bak.%d", configYAML, time.Now().UnixNano())
		if err := p.fs.WriteFile(backupPath, oldConfig.data, filePermUserRW); err != nil {
			return nil, cleanupBeforeCommit(fmt.Errorf("backup generated config: %w", err))
		}
	}
	transaction := configTransactionRecord{
		Phase:            transactionPrepared,
		StagingDir:       staged.dir,
		CandidatePath:    candidatePath(candidate),
		BackupPath:       backupPath,
		Config:           transactionSnapshotOf(oldConfig),
		SubscriptionData: transactionSnapshotOf(oldSubscription),
	}
	if err := p.writeConfigTransaction(transaction); err != nil {
		return nil, cleanupBeforeCommit(err)
	}
	transactionRecorded = true
	if err := p.fs.Rename(staged.path, configYAML); err != nil {
		primary := fmt.Errorf("committing generated config: %w", err)
		if rollbackErr := rollback(); rollbackErr != nil {
			primary = errors.Join(primary, fmt.Errorf("rollback failed: %w", rollbackErr))
		}
		return nil, primary
	}
	transaction.Phase = transactionConfigCommitted
	if err := p.writeConfigTransaction(transaction); err != nil {
		primary := fmt.Errorf("recording committed config transaction: %w", err)
		if rollbackErr := rollback(); rollbackErr != nil {
			primary = errors.Join(primary, fmt.Errorf("rollback failed: %w", rollbackErr))
		}
		return nil, primary
	}
	if candidate != nil {
		if err := p.fs.Rename(candidate.path, subscriptionDataFile); err != nil {
			primary := fmt.Errorf("publishing subscription data: %w", err)
			if rollbackErr := rollback(); rollbackErr != nil {
				primary = errors.Join(primary, fmt.Errorf("rollback failed: %w", rollbackErr))
			}
			return nil, primary
		}
		candidate.path = ""
	}
	transaction.Phase = transactionCommitted
	transaction.CandidatePath = ""
	if err := p.writeConfigTransaction(transaction); err != nil {
		primary := fmt.Errorf("recording committed subscription transaction: %w", err)
		if rollbackErr := rollback(); rollbackErr != nil {
			primary = errors.Join(primary, fmt.Errorf("rollback failed: %w", rollbackErr))
		}
		return nil, primary
	}
	var cleanupErrs []error
	if err := p.fs.Remove(staged.dir); err != nil {
		cleanupErrs = append(cleanupErrs, fmt.Errorf("cleanup staged config: %w", err))
	}
	if err := p.clearConfigTransaction(); err != nil {
		p.recoveryErr = err
		cleanupErrs = append(cleanupErrs, err)
	}
	return errors.Join(cleanupErrs...), nil
}

func (p *configPipeline) UpdateConfig(ctx context.Context) (applyErr error) {
	if err := p.ensureConfigRecovery(); err != nil {
		return err
	}
	release, err := p.lock.Acquire(ctx)
	if err != nil {
		return err
	}
	defer release()

	preview := ""
	statusRecorded := false
	var candidate *subscriptionCandidate
	defer func() {
		if candidate != nil && candidate.path != "" {
			if cleanupErr := p.fs.Remove(candidate.path); cleanupErr != nil {
				applyErr = errors.Join(applyErr, fmt.Errorf("cleanup subscription staging: %w", cleanupErr))
			}
			candidate.path = ""
		}
		if applyErr != nil && !statusRecorded {
			statusErr := p.recordConfigApply(ConfigApplyFailed, preview, applyErr)
			statusRecorded = true
			if statusErr != nil {
				applyErr = errors.Join(applyErr, statusErr)
			}
		}
	}()

	candidate, err = p.refreshSubscription(ctx)
	if err != nil {
		return err
	}
	preview, err = p.buildApplyPreview(ctx, candidate)
	if err != nil {
		return err
	}

	staged, err := p.stageConfig(ctx, preview)
	if err != nil {
		return err
	}

	if p.validate != nil {
		if err := p.validate.Validate(ctx, staged.path); err != nil {
			failure := p.cleanupStagedConfig(staged, err)
			if candidate != nil && candidate.path != "" {
				if cleanupErr := p.fs.Remove(candidate.path); cleanupErr != nil {
					failure = errors.Join(failure, fmt.Errorf("cleanup subscription staging: %w", cleanupErr))
				}
				candidate.path = ""
			}
			state := ConfigValidationFailed
			if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				state = ConfigApplyFailed
			}
			statusErr := p.recordConfigApply(state, preview, failure)
			statusRecorded = true
			if statusErr != nil {
				failure = errors.Join(failure, statusErr)
			}
			return failure
		}
	}
	if err := ctx.Err(); err != nil {
		return p.cleanupStagedConfig(staged, err)
	}

	oldConfig, err := p.snapshotFile(configYAML)
	if err != nil {
		return fmt.Errorf("snapshotting current config: %w", err)
	}
	oldSubscription, err := p.snapshotFile(subscriptionDataFile)
	if err != nil {
		return fmt.Errorf("snapshotting current subscription data: %w", err)
	}
	postCommitCleanupErr, applyErr := p.commitConfig(staged, candidate, oldConfig, oldSubscription)
	if applyErr != nil {
		return applyErr
	}

	if p.onReload != nil {
		if err := p.onReload(ctx); err != nil {
			failure := err
			if postCommitCleanupErr != nil {
				failure = errors.Join(failure, fmt.Errorf("cleanup staged config: %w", postCommitCleanupErr))
			}
			statusErr := p.recordConfigApply(ConfigPendingReload, preview, failure)
			statusRecorded = true
			if statusErr != nil {
				failure = errors.Join(failure, statusErr)
			}
			return failure
		}
	}

	if postCommitCleanupErr != nil {
		statusErr := p.recordConfigApply(ConfigApplied, preview, postCommitCleanupErr)
		statusRecorded = true
		if statusErr != nil {
			return errors.Join(postCommitCleanupErr, statusErr)
		}
		return postCommitCleanupErr
	}
	statusErr := p.recordConfigApply(ConfigApplied, preview, nil)
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
	if err := p.ensureConfigRecovery(); err != nil {
		return err
	}
	if p.validate == nil {
		return nil
	}
	return p.validate.Validate(ctx, configYAML)
}
