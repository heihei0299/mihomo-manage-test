package manager

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func remoteTransactionFileSystem() *fakeFileSystem {
	return &fakeFileSystem{
		fileExists: map[string]bool{
			OverrideFilePath:       true,
			subscriptionSourceFile: true,
			subscriptionURLFile:    true,
			subscriptionDataFile:   true,
			configYAML:             true,
		},
		written: map[string][]byte{
			OverrideFilePath:       []byte("log-level: info\n"),
			subscriptionSourceFile: []byte("remote\n"),
			subscriptionURLFile:    []byte("https://example.com/new.yaml\n"),
			subscriptionDataFile:   []byte("mode: old\n"),
			configYAML:             []byte("mode: old\n"),
		},
	}
}

type observingRemoteValidator struct {
	fs            FileSystem
	dataAtCheck   string
	configAtCheck string
	err           error
}

func (v *observingRemoteValidator) Validate(_ context.Context, configPath string) error {
	data, _ := v.fs.ReadFile(subscriptionDataFile)
	config, _ := v.fs.ReadFile(configPath)
	v.dataAtCheck = string(data)
	v.configAtCheck = string(config)
	return v.err
}

func newRemoteTransactionManager(fs *fakeFileSystem, validator ConfigValidator, reload func(context.Context) error) (*configPipeline, *fakeDownloader) {
	downloader := &fakeDownloader{content: "mode: new\nproxies:\n  - name: candidate\n"}
	linkStorage(fs, &downloader.fakeReleaseSource)
	return NewConfigManager(fs, downloader, validator, reload).(*configPipeline), downloader
}

func TestRemoteCandidateIsValidatedBeforeSubscriptionCachePublication(t *testing.T) {
	fs := remoteTransactionFileSystem()
	validator := &observingRemoteValidator{fs: fs}
	manager, _ := newRemoteTransactionManager(fs, validator, nil)

	if err := manager.UpdateConfig(context.Background()); err != nil {
		t.Fatalf("UpdateConfig failed: %v", err)
	}
	if validator.dataAtCheck != "mode: old\n" {
		t.Fatalf("subscription cache at validation = %q, want old cache", validator.dataAtCheck)
	}
	if !strings.Contains(validator.configAtCheck, "candidate") {
		t.Fatalf("validated config = %q, want candidate subscription", validator.configAtCheck)
	}
}

func TestRemoteValidationFailurePreservesConfigAndSubscriptionCache(t *testing.T) {
	fs := remoteTransactionFileSystem()
	manager, _ := newRemoteTransactionManager(fs, &failValidator{err: errors.New("candidate is invalid")}, nil)

	if err := manager.UpdateConfig(context.Background()); err == nil {
		t.Fatal("UpdateConfig should fail validation")
	}
	if got := string(fs.written[subscriptionDataFile]); got != "mode: old\n" {
		t.Fatalf("subscription cache after validation failure = %q", got)
	}
	if got := string(fs.written[configYAML]); got != "mode: old\n" {
		t.Fatalf("config after validation failure = %q", got)
	}
	status, err := manager.LastConfigApply(context.Background())
	if err != nil {
		t.Fatalf("LastConfigApply failed: %v", err)
	}
	if status.State != ConfigValidationFailed {
		t.Fatalf("status = %+v, want validation-failed", status)
	}
}

func TestRemoteSuccessPublishesMatchingConfigAndSubscriptionCache(t *testing.T) {
	fs := remoteTransactionFileSystem()
	manager, downloader := newRemoteTransactionManager(fs, &passValidator{}, nil)

	if err := manager.UpdateConfig(context.Background()); err != nil {
		t.Fatalf("UpdateConfig failed: %v", err)
	}
	if !downloader.downloadCalled {
		t.Fatal("remote update should download the configured URL")
	}
	data := string(fs.written[subscriptionDataFile])
	config := string(fs.written[configYAML])
	if !strings.Contains(data, "candidate") || !strings.Contains(config, "candidate") {
		t.Fatalf("published data/config do not contain the same candidate: data=%q config=%q", data, config)
	}
	if _, ok := fs.written[configApplyTransactionFile]; ok {
		t.Fatal("successful apply should remove its transaction record")
	}
}

func TestRemoteReloadFailureKeepsCommittedCandidateAndPendingStatus(t *testing.T) {
	fs := remoteTransactionFileSystem()
	manager, _ := newRemoteTransactionManager(fs, &passValidator{}, func(context.Context) error {
		return errors.New("reload unavailable")
	})

	if err := manager.UpdateConfig(context.Background()); err == nil {
		t.Fatal("UpdateConfig should report reload failure")
	}
	if !strings.Contains(string(fs.written[configYAML]), "candidate") || !strings.Contains(string(fs.written[subscriptionDataFile]), "candidate") {
		t.Fatal("reload failure should retain the committed candidate")
	}
	status, err := manager.LastConfigApply(context.Background())
	if err != nil {
		t.Fatalf("LastConfigApply failed: %v", err)
	}
	if status.State != ConfigPendingReload || !strings.Contains(status.ErrorSummary, "reload unavailable") {
		t.Fatalf("status = %+v, want pending-reload diagnostic", status)
	}
}

func TestRemoteDownloadCancellationPreservesOldSubscriptionCache(t *testing.T) {
	fs := remoteTransactionFileSystem()
	downloader := &blockingSubscriptionDownloader{started: make(chan struct{})}
	linkStorage(fs, &downloader.fakeReleaseSource)
	manager := NewConfigManager(fs, downloader, &passValidator{}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- manager.UpdateConfig(ctx) }()
	<-downloader.started
	cancel()

	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("UpdateConfig error = %v, want cancellation", err)
	}
	if got := string(fs.written[subscriptionDataFile]); got != "mode: old\n" {
		t.Fatalf("subscription cache after cancellation = %q", got)
	}
	if got := string(fs.written[configYAML]); got != "mode: old\n" {
		t.Fatalf("config after cancellation = %q", got)
	}
}

func TestInterruptedConfigTransactionRecoversBeforeNextManagerOperation(t *testing.T) {
	fs := remoteTransactionFileSystem()
	fs.written[configYAML] = []byte("mode: new\n")
	fs.written[subscriptionDataFile] = []byte("mode: new\n")
	fs.fileExists[configApplyTransactionFile] = true
	fs.fileExists[configApplyTransactionFile+".tmp"] = true
	fs.fileExists["/opt/mihomo/etc/.staging"] = true
	fs.fileExists["/opt/mihomo/etc/config.yaml.bak.recovery"] = true
	record, err := json.Marshal(configTransactionRecord{
		Phase:            transactionConfigCommitted,
		StagingDir:       "/opt/mihomo/etc/.staging",
		CandidatePath:    subscriptionDataFile + ".tmp",
		BackupPath:       "/opt/mihomo/etc/config.yaml.bak.recovery",
		Config:           transactionSnapshot{Data: []byte("mode: old\n"), Exists: true},
		SubscriptionData: transactionSnapshot{Data: []byte("mode: old\n"), Exists: true},
	})
	if err != nil {
		t.Fatalf("encode transaction: %v", err)
	}
	fs.written[configApplyTransactionFile] = record

	_ = NewConfigManager(fs, &fakeReleaseSource{}, &passValidator{}, nil)
	if got := string(fs.written[configYAML]); got != "mode: old\n" {
		t.Fatalf("recovered config = %q", got)
	}
	if got := string(fs.written[subscriptionDataFile]); got != "mode: old\n" {
		t.Fatalf("recovered subscription data = %q", got)
	}
	if _, ok := fs.written[configApplyTransactionFile]; ok {
		t.Fatal("recovered transaction record should be removed")
	}
}

type failedRecoveryFS struct {
	*fakeFileSystem
	err error
}

func (fs *failedRecoveryFS) WriteFile(path string, data []byte, perm uint32) error {
	if path == configYAML && string(data) == "mode: old\n" {
		return fs.err
	}
	return fs.fakeFileSystem.WriteFile(path, data, perm)
}

func TestRecoveryFailureBlocksFurtherConfigWrites(t *testing.T) {
	base := remoteTransactionFileSystem()
	base.written[configYAML] = []byte("mode: new\n")
	base.fileExists[configApplyTransactionFile] = true
	record, err := json.Marshal(configTransactionRecord{
		Phase:            transactionConfigCommitted,
		Config:           transactionSnapshot{Data: []byte("mode: old\n"), Exists: true},
		SubscriptionData: transactionSnapshot{Data: []byte("mode: old\n"), Exists: true},
	})
	if err != nil {
		t.Fatalf("encode transaction: %v", err)
	}
	base.written[configApplyTransactionFile] = record
	fs := &failedRecoveryFS{fakeFileSystem: base, err: errors.New("restore unavailable")}
	manager := NewConfigManager(fs, &fakeReleaseSource{}, &passValidator{}, nil)

	if err := manager.SetSubscriptionSource(context.Background(), "local"); err == nil || !strings.Contains(err.Error(), "recovery") {
		t.Fatalf("SetSubscriptionSource error = %v, want recovery block", err)
	}
}

func TestRemoteBackupFailurePreservesOldConfigAndSubscriptionCache(t *testing.T) {
	base := remoteTransactionFileSystem()
	fs := &failingBackupWriteFileSystem{fakeFileSystem: base, err: errors.New("backup unavailable")}
	manager, _ := newRemoteTransactionManager(base, &passValidator{}, nil)
	manager.fs = fs

	if err := manager.UpdateConfig(context.Background()); err == nil {
		t.Fatal("UpdateConfig should report backup failure")
	}
	if got := string(base.written[subscriptionDataFile]); got != "mode: old\n" {
		t.Fatalf("subscription cache after backup failure = %q", got)
	}
	if got := string(base.written[configYAML]); got != "mode: old\n" {
		t.Fatalf("config after backup failure = %q", got)
	}
}

func TestRemoteCommitFailureRestoresSubscriptionCache(t *testing.T) {
	base := remoteTransactionFileSystem()
	fs := &failingRenameFileSystem{fakeFileSystem: base, err: errors.New("config commit unavailable")}
	manager, _ := newRemoteTransactionManager(base, &passValidator{}, nil)
	manager.fs = fs

	if err := manager.UpdateConfig(context.Background()); err == nil {
		t.Fatal("UpdateConfig should report commit failure")
	}
	if got := string(base.written[subscriptionDataFile]); got != "mode: old\n" {
		t.Fatalf("subscription cache after commit failure = %q", got)
	}
	if got := string(base.written[configYAML]); got != "mode: old\n" {
		t.Fatalf("config after commit failure = %q", got)
	}
}

func TestRemoteSubscriptionPublishFailureRestoresCommittedConfig(t *testing.T) {
	base := remoteTransactionFileSystem()
	fs := &failingSubscriptionPublishFS{fakeFileSystem: base, err: errors.New("subscription publish unavailable")}
	manager, _ := newRemoteTransactionManager(base, &passValidator{}, nil)
	manager.fs = fs

	if err := manager.UpdateConfig(context.Background()); err == nil {
		t.Fatal("UpdateConfig should report subscription publish failure")
	}
	if got := string(base.written[subscriptionDataFile]); got != "mode: old\n" {
		t.Fatalf("subscription cache after publish failure = %q", got)
	}
	if got := string(base.written[configYAML]); got != "mode: old\n" {
		t.Fatalf("config after publish failure = %q", got)
	}
}

type failingSubscriptionPublishFS struct {
	*fakeFileSystem
	err error
}

func (fs *failingSubscriptionPublishFS) Rename(oldPath, newPath string) error {
	if newPath == subscriptionDataFile {
		return fs.err
	}
	return fs.fakeFileSystem.Rename(oldPath, newPath)
}

func TestRemoteValidationCleanupFailureIsRecordedWithPrimaryError(t *testing.T) {
	base := remoteTransactionFileSystem()
	fs := &failSecondSubscriptionTempRemoveFS{fakeFileSystem: base, err: errors.New("subscription staging cleanup unavailable")}
	manager, _ := newRemoteTransactionManager(base, &failValidator{err: errors.New("candidate invalid")}, nil)
	manager.fs = fs

	err := manager.UpdateConfig(context.Background())
	if err == nil || !strings.Contains(err.Error(), "candidate invalid") || !strings.Contains(err.Error(), "subscription staging cleanup unavailable") {
		t.Fatalf("UpdateConfig error = %v, want primary and cleanup errors", err)
	}
	status, statusErr := manager.LastConfigApply(context.Background())
	if statusErr != nil {
		t.Fatalf("LastConfigApply failed: %v", statusErr)
	}
	if !strings.Contains(status.ErrorSummary, "subscription staging cleanup unavailable") {
		t.Fatalf("status = %+v, want cleanup diagnostic", status)
	}
}

func TestRemoteRollbackFailureKeepsPrimaryAndRecoveryDiagnostics(t *testing.T) {
	base := remoteTransactionFileSystem()
	fs := &failSubscriptionRestoreFS{
		failingRenameFileSystem: &failingRenameFileSystem{fakeFileSystem: base, err: errors.New("config commit unavailable")},
		err:                     errors.New("subscription restore unavailable"),
	}
	manager, _ := newRemoteTransactionManager(base, &passValidator{}, nil)
	manager.fs = fs

	err := manager.UpdateConfig(context.Background())
	if err == nil || !strings.Contains(err.Error(), "config commit unavailable") || !strings.Contains(err.Error(), "subscription restore unavailable") {
		t.Fatalf("UpdateConfig error = %v, want primary and recovery errors", err)
	}
	status, statusErr := manager.LastConfigApply(context.Background())
	if statusErr != nil {
		t.Fatalf("LastConfigApply failed: %v", statusErr)
	}
	if !strings.Contains(status.ErrorSummary, "subscription restore unavailable") {
		t.Fatalf("status = %+v, want recovery diagnostic", status)
	}
}

type failSubscriptionRestoreFS struct {
	*failingRenameFileSystem
	err error
}

func (fs *failSubscriptionRestoreFS) WriteFile(path string, data []byte, perm uint32) error {
	if path == subscriptionDataFile && string(data) == "mode: old\n" {
		return fs.err
	}
	return fs.failingRenameFileSystem.WriteFile(path, data, perm)
}

type failSecondSubscriptionTempRemoveFS struct {
	*fakeFileSystem
	removes int
	err     error
}

func (fs *failSecondSubscriptionTempRemoveFS) Remove(path string) error {
	if path == subscriptionDataFile+".tmp" {
		fs.removes++
		if fs.removes >= 2 {
			return fs.err
		}
	}
	return fs.fakeFileSystem.Remove(path)
}
