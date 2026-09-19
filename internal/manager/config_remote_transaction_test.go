package manager

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func remoteApplyTestFileSystem() *fakeFileSystem {
	return &fakeFileSystem{
		fileExists: map[string]bool{
			OverrideFilePath:       true,
			subscriptionSourceFile: true,
			subscriptionURLFile:    true,
			subscriptionDataFile:   true,
			configYAML:             true,
		},
		written: map[string][]byte{
			OverrideFilePath:       []byte("mode: rule\n"),
			subscriptionSourceFile: []byte("remote\n"),
			subscriptionURLFile:    []byte("https://example.com/sub.yaml"),
			subscriptionDataFile:   []byte("proxies:\n  - name: old\n"),
			configYAML:             []byte("old config\n"),
		},
	}
}

type readingConfigValidator struct {
	fs      FileSystem
	content string
}

func (v *readingConfigValidator) Validate(ctx context.Context, path string) error {
	data, err := v.fs.ReadFile(path)
	if err != nil {
		return err
	}
	v.content = string(data)
	return nil
}

func TestRemoteCandidateIsUsedForValidation(t *testing.T) {
	fs := remoteApplyTestFileSystem()
	dl := &fakeDownloader{content: "proxies:\n  - name: new\n"}
	linkStorage(fs, &dl.fakeReleaseSource)
	validator := &readingConfigValidator{fs: fs}
	m := NewConfigManager(fs, dl, validator, nil)

	if err := m.UpdateConfig(context.Background()); err != nil {
		t.Fatalf("UpdateConfig failed: %v", err)
	}
	if !strings.Contains(validator.content, "new") || strings.Contains(validator.content, "old") {
		t.Fatalf("validated candidate = %q, want new subscription only", validator.content)
	}
}

func TestRemoteGenerationFailurePreservesSubscriptionCacheAndConfig(t *testing.T) {
	fs := remoteApplyTestFileSystem()
	dl := &fakeDownloader{content: "proxies: ["}
	linkStorage(fs, &dl.fakeReleaseSource)
	m := NewConfigManager(fs, dl, &passValidator{}, nil)

	if err := m.UpdateConfig(context.Background()); err == nil {
		t.Fatal("UpdateConfig should report candidate generation failure")
	}
	if got := string(fs.written[subscriptionDataFile]); !strings.Contains(got, "old") {
		t.Fatalf("subscription cache after generation failure = %q, want old cache", got)
	}
	if got := string(fs.written[configYAML]); got != "old config\n" {
		t.Fatalf("config after generation failure = %q, want old config", got)
	}
}

func TestLocalUpdateDoesNotDownloadOrReplaceSubscriptionCache(t *testing.T) {
	fs := localApplyTestFileSystem()
	dl := &fakeDownloader{content: "proxies:\n  - name: remote\n"}
	linkStorage(fs, &dl.fakeReleaseSource)
	m := NewConfigManager(fs, dl, &passValidator{}, nil)

	if err := m.UpdateConfig(context.Background()); err != nil {
		t.Fatalf("UpdateConfig failed: %v", err)
	}
	if dl.downloadCalled {
		t.Fatal("local subscription update must not download remote data")
	}
	if got := string(fs.written[subscriptionDataFile]); !strings.Contains(got, "mode: rule") {
		t.Fatalf("local subscription cache = %q, want unchanged local data", got)
	}
}

func TestRemoteValidationFailurePreservesSubscriptionCacheAndConfig(t *testing.T) {
	fs := remoteApplyTestFileSystem()
	dl := &fakeDownloader{content: "proxies:\n  - name: new\n"}
	linkStorage(fs, &dl.fakeReleaseSource)
	m := NewConfigManager(fs, dl, &failValidator{err: errors.New("invalid candidate")}, nil)

	if err := m.UpdateConfig(context.Background()); err == nil {
		t.Fatal("UpdateConfig should report candidate validation failure")
	}
	if got := string(fs.written[subscriptionDataFile]); !strings.Contains(got, "old") {
		t.Fatalf("subscription cache after validation failure = %q, want old cache", got)
	}
	if got := string(fs.written[configYAML]); got != "old config\n" {
		t.Fatalf("config after validation failure = %q, want old config", got)
	}
	if _, exists := fs.written[subscriptionDataFile+".tmp"]; exists {
		t.Fatal("subscription staging file should be cleaned after validation failure")
	}
}

func TestRemoteApplyCommitsMatchingConfigAndSubscription(t *testing.T) {
	fs := remoteApplyTestFileSystem()
	dl := &fakeDownloader{content: "proxies:\n  - name: new\n"}
	linkStorage(fs, &dl.fakeReleaseSource)
	m := NewConfigManager(fs, dl, &passValidator{}, nil)

	if err := m.UpdateConfig(context.Background()); err != nil {
		t.Fatalf("UpdateConfig failed: %v", err)
	}
	if got := string(fs.written[subscriptionDataFile]); !strings.Contains(got, "new") {
		t.Fatalf("subscription cache = %q, want new candidate", got)
	}
	if got := string(fs.written[configYAML]); !strings.Contains(got, "new") {
		t.Fatalf("config = %q, want candidate subscription", got)
	}
	if got := string(fs.written[configYAML]); strings.Contains(got, "old") {
		t.Fatalf("config = %q, contains old subscription", got)
	}
	status, err := m.LastConfigApply(context.Background())
	if err != nil {
		t.Fatalf("LastConfigApply failed: %v", err)
	}
	if status.SubscriptionHash != configContentHash("proxies:\n  - name: new\n") {
		t.Fatalf("subscription hash = %q, want candidate hash", status.SubscriptionHash)
	}
}

type failingSubscriptionCommitFileSystem struct {
	*fakeFileSystem
	err error
}

func (fs *failingSubscriptionCommitFileSystem) Rename(oldPath, newPath string) error {
	if newPath == subscriptionDataFile {
		return fs.err
	}
	return fs.fakeFileSystem.Rename(oldPath, newPath)
}

func TestRemoteSubscriptionCommitFailureRestoresConfigAndCache(t *testing.T) {
	base := remoteApplyTestFileSystem()
	fs := &failingSubscriptionCommitFileSystem{fakeFileSystem: base, err: errors.New("subscription commit failed")}
	dl := &fakeDownloader{content: "proxies:\n  - name: new\n"}
	linkStorage(base, &dl.fakeReleaseSource)
	m := NewConfigManager(fs, dl, &passValidator{}, nil)

	if err := m.UpdateConfig(context.Background()); err == nil {
		t.Fatal("UpdateConfig should report subscription commit failure")
	}
	if got := string(base.written[subscriptionDataFile]); !strings.Contains(got, "old") {
		t.Fatalf("subscription cache after commit failure = %q, want old cache", got)
	}
	if got := string(base.written[configYAML]); got != "old config\n" {
		t.Fatalf("config after subscription commit failure = %q, want old config", got)
	}
}

func TestRemoteCommitFailurePreservesSubscriptionCacheAndConfig(t *testing.T) {
	base := remoteApplyTestFileSystem()
	fs := &failingRenameFileSystem{fakeFileSystem: base, err: errors.New("config commit failed")}
	dl := &fakeDownloader{content: "proxies:\n  - name: new\n"}
	linkStorage(base, &dl.fakeReleaseSource)
	m := NewConfigManager(fs, dl, &passValidator{}, nil)

	if err := m.UpdateConfig(context.Background()); err == nil {
		t.Fatal("UpdateConfig should report config commit failure")
	}
	if got := string(base.written[subscriptionDataFile]); !strings.Contains(got, "old") {
		t.Fatalf("subscription cache after commit failure = %q, want old cache", got)
	}
	if got := string(base.written[configYAML]); got != "old config\n" {
		t.Fatalf("config after commit failure = %q, want old config", got)
	}
}

type failingSubscriptionBackupFileSystem struct {
	*fakeFileSystem
	err error
}

func (fs *failingSubscriptionBackupFileSystem) WriteFile(path string, data []byte, perm uint32) error {
	if strings.HasPrefix(path, subscriptionDataFile+".bak.") {
		return fs.err
	}
	return fs.fakeFileSystem.WriteFile(path, data, perm)
}

func TestRemoteSubscriptionBackupFailurePreservesConfigAndCache(t *testing.T) {
	base := remoteApplyTestFileSystem()
	fs := &failingSubscriptionBackupFileSystem{fakeFileSystem: base, err: errors.New("subscription backup failed")}
	dl := &fakeDownloader{content: "proxies:\n  - name: new\n"}
	linkStorage(base, &dl.fakeReleaseSource)
	m := NewConfigManager(fs, dl, &passValidator{}, nil)

	if err := m.UpdateConfig(context.Background()); err == nil {
		t.Fatal("UpdateConfig should report subscription backup failure")
	}
	if got := string(base.written[subscriptionDataFile]); !strings.Contains(got, "old") {
		t.Fatalf("subscription cache after backup failure = %q, want old cache", got)
	}
	if got := string(base.written[configYAML]); got != "old config\n" {
		t.Fatalf("config after backup failure = %q, want old config", got)
	}
}

func TestRemoteReloadFailureKeepsCommittedConfigAndSubscription(t *testing.T) {
	fs := remoteApplyTestFileSystem()
	dl := &fakeDownloader{content: "proxies:\n  - name: new\n"}
	linkStorage(fs, &dl.fakeReleaseSource)
	m := NewConfigManager(fs, dl, &passValidator{}, func(context.Context) error {
		return errors.New("reload failed")
	})

	if err := m.UpdateConfig(context.Background()); err == nil {
		t.Fatal("UpdateConfig should report reload failure")
	}
	if got := string(fs.written[subscriptionDataFile]); !strings.Contains(got, "new") {
		t.Fatalf("subscription cache after reload failure = %q, want committed candidate", got)
	}
	if got := string(fs.written[configYAML]); !strings.Contains(got, "new") {
		t.Fatalf("config after reload failure = %q, want committed candidate", got)
	}
	requireConfigApplyState(t, m, ConfigPendingReload)
}

type failingSubscriptionCleanupFileSystem struct {
	*fakeFileSystem
	err error
}

func (fs *failingSubscriptionCleanupFileSystem) Remove(path string) error {
	if path == subscriptionDataFile+".tmp" {
		return fs.err
	}
	return fs.fakeFileSystem.Remove(path)
}

func TestRemoteValidationFailureRetainsCleanupDiagnostic(t *testing.T) {
	base := remoteApplyTestFileSystem()
	fs := &failingSubscriptionCleanupFileSystem{fakeFileSystem: base, err: errors.New("subscription staging cleanup failed")}
	dl := &fakeDownloader{content: "proxies:\n  - name: new\n"}
	linkStorage(base, &dl.fakeReleaseSource)
	m := NewConfigManager(fs, dl, &failValidator{err: errors.New("invalid candidate")}, nil)

	if err := m.UpdateConfig(context.Background()); err == nil || !strings.Contains(err.Error(), "subscription staging cleanup failed") {
		t.Fatalf("UpdateConfig error = %v, want cleanup diagnostic", err)
	}
	status, statusErr := m.LastConfigApply(context.Background())
	if statusErr != nil {
		t.Fatalf("LastConfigApply failed: %v", statusErr)
	}
	if !strings.Contains(status.ErrorSummary, "subscription staging cleanup failed") {
		t.Fatalf("status = %+v, want cleanup diagnostic", status)
	}
}

type recordingChmodFileSystem struct {
	*fakeFileSystem
	chmod map[string]uint32
}

func (fs *recordingChmodFileSystem) Chmod(path string, perm uint32) error {
	if fs.chmod == nil {
		fs.chmod = make(map[string]uint32)
	}
	fs.chmod[path] = perm
	return fs.fakeFileSystem.Chmod(path, perm)
}

func TestRemoteSubscriptionStagingUsesConfigPermissions(t *testing.T) {
	base := remoteApplyTestFileSystem()
	fs := &recordingChmodFileSystem{fakeFileSystem: base}
	dl := &fakeDownloader{content: "proxies:\n  - name: new\n"}
	linkStorage(base, &dl.fakeReleaseSource)
	m := NewConfigManager(fs, dl, &passValidator{}, nil)

	if err := m.UpdateConfig(context.Background()); err != nil {
		t.Fatalf("UpdateConfig failed: %v", err)
	}
	if got := fs.chmod[subscriptionDataFile+".tmp"]; got != filePermUserRW {
		t.Fatalf("subscription staging mode = %o, want %o", got, filePermUserRW)
	}
}

type failingBackupCleanupFileSystem struct {
	*fakeFileSystem
	err error
}

func (fs *failingBackupCleanupFileSystem) Remove(path string) error {
	if strings.Contains(path, ".bak.") {
		return fs.err
	}
	return fs.fakeFileSystem.Remove(path)
}

func TestRemoteBackupCleanupFailureRecordsAppliedWarning(t *testing.T) {
	base := remoteApplyTestFileSystem()
	fs := &failingBackupCleanupFileSystem{fakeFileSystem: base, err: errors.New("backup cleanup failed")}
	dl := &fakeDownloader{content: "proxies:\n  - name: new\n"}
	linkStorage(base, &dl.fakeReleaseSource)
	m := NewConfigManager(fs, dl, &passValidator{}, nil)

	if err := m.UpdateConfig(context.Background()); err == nil || !strings.Contains(err.Error(), "backup cleanup failed") {
		t.Fatalf("UpdateConfig error = %v, want backup cleanup warning", err)
	}
	requireConfigApplyState(t, m, ConfigApplied)
	status, _ := m.LastConfigApply(context.Background())
	if !strings.Contains(status.ErrorSummary, "backup cleanup failed") {
		t.Fatalf("status = %+v, want backup cleanup diagnostic", status)
	}
}

type failingRestoreFileSystem struct {
	*failingSubscriptionCommitFileSystem
	restoreErr error
}

func (fs *failingRestoreFileSystem) WriteFile(path string, data []byte, perm uint32) error {
	if path == configYAML {
		return fs.restoreErr
	}
	return fs.fakeFileSystem.WriteFile(path, data, perm)
}

func TestRemoteRestoreFailureRetainsPrimaryAndRecoveryErrors(t *testing.T) {
	base := remoteApplyTestFileSystem()
	commit := &failingSubscriptionCommitFileSystem{fakeFileSystem: base, err: errors.New("subscription commit failed")}
	fs := &failingRestoreFileSystem{failingSubscriptionCommitFileSystem: commit, restoreErr: errors.New("config restore failed")}
	dl := &fakeDownloader{content: "proxies:\n  - name: new\n"}
	linkStorage(base, &dl.fakeReleaseSource)
	m := NewConfigManager(fs, dl, &passValidator{}, nil)

	err := m.UpdateConfig(context.Background())
	if err == nil || !strings.Contains(err.Error(), "subscription commit failed") || !strings.Contains(err.Error(), "config restore failed") {
		t.Fatalf("UpdateConfig error = %v, want primary and restore errors", err)
	}
	status, statusErr := m.LastConfigApply(context.Background())
	if statusErr != nil {
		t.Fatalf("LastConfigApply failed: %v", statusErr)
	}
	if !strings.Contains(status.ErrorSummary, "config restore failed") {
		t.Fatalf("status = %+v, want restore diagnostic", status)
	}
}

func TestConfigManagerRecoversInterruptedRemoteCommit(t *testing.T) {
	configBackup := configYAML + ".bak.recovery"
	subscriptionBackup := subscriptionDataFile + ".bak.recovery"
	stagedDir := configDir + "/.mihomo-config-staging-recovery"
	stagedSubscription := subscriptionDataFile + ".tmp.recovery"
	transaction := configTransactionState{
		State:                  configTransactionConfigCommitted,
		ConfigBackup:           configBackup,
		ConfigExisted:          true,
		SubscriptionStaged:     true,
		SubscriptionBackup:     subscriptionBackup,
		SubscriptionExisted:    true,
		StagedConfigDir:        stagedDir,
		StagedSubscriptionPath: stagedSubscription,
	}
	transactionData, err := json.Marshal(transaction)
	if err != nil {
		t.Fatalf("marshal transaction: %v", err)
	}
	fs := &fakeFileSystem{
		fileExists: map[string]bool{
			configApplyTransactionFile: true,
			configYAML:                 true,
			subscriptionDataFile:       true,
			configBackup:               true,
			subscriptionBackup:         true,
			stagedDir:                  true,
			stagedSubscription:         true,
		},
		written: map[string][]byte{
			configApplyTransactionFile: transactionData,
			configYAML:                 []byte("new config\n"),
			subscriptionDataFile:       []byte("new subscription\n"),
			configBackup:               []byte("old config\n"),
			subscriptionBackup:         []byte("old subscription\n"),
			stagedSubscription:         []byte("staged subscription\n"),
		},
	}

	NewConfigManager(fs, &fakeReleaseSource{}, nil, nil)
	if got := string(fs.written[configYAML]); got != "old config\n" {
		t.Fatalf("recovered config = %q, want old config", got)
	}
	if got := string(fs.written[subscriptionDataFile]); got != "old subscription\n" {
		t.Fatalf("recovered subscription = %q, want old subscription", got)
	}
	if _, exists := fs.written[configApplyTransactionFile]; exists {
		t.Fatal("recovery transaction marker should be removed")
	}
}

func TestConfigManagerRecoversInterruptedFirstRemoteCommit(t *testing.T) {
	configBackup := configYAML + ".bak.first-recovery"
	stagedDir := configDir + "/.mihomo-config-staging-first-recovery"
	stagedSubscription := subscriptionDataFile + ".tmp.first-recovery"
	transaction := configTransactionState{
		State:                  configTransactionConfigCommitted,
		ConfigBackup:           configBackup,
		ConfigExisted:          true,
		SubscriptionStaged:     true,
		SubscriptionExisted:    false,
		StagedConfigDir:        stagedDir,
		StagedSubscriptionPath: stagedSubscription,
	}
	transactionData, err := json.Marshal(transaction)
	if err != nil {
		t.Fatalf("marshal transaction: %v", err)
	}
	fs := &fakeFileSystem{
		fileExists: map[string]bool{
			configApplyTransactionFile: true,
			configYAML:                 true,
			subscriptionDataFile:       true,
			configBackup:               true,
		},
		written: map[string][]byte{
			configApplyTransactionFile: transactionData,
			configYAML:                 []byte("new config\n"),
			subscriptionDataFile:       []byte("new subscription\n"),
			configBackup:               []byte("old config\n"),
		},
	}

	NewConfigManager(fs, &fakeReleaseSource{}, nil, nil)
	if got := string(fs.written[configYAML]); got != "old config\n" {
		t.Fatalf("recovered config = %q, want old config", got)
	}
	if _, exists := fs.written[subscriptionDataFile]; exists {
		t.Fatal("recovery should remove first remote subscription cache")
	}
}

func TestRemoteValidationCancellationPreservesSubscriptionCache(t *testing.T) {
	fs := remoteApplyTestFileSystem()
	dl := &fakeDownloader{content: "proxies:\n  - name: new\n"}
	linkStorage(fs, &dl.fakeReleaseSource)
	validator := &blockingConfigValidator{started: make(chan struct{})}
	m := NewConfigManager(fs, dl, validator, nil)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- m.UpdateConfig(ctx) }()
	<-validator.started
	cancel()

	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("UpdateConfig error = %v, want cancellation", err)
	}
	if got := string(fs.written[subscriptionDataFile]); !strings.Contains(got, "old") {
		t.Fatalf("subscription cache after cancellation = %q, want old cache", got)
	}
}
