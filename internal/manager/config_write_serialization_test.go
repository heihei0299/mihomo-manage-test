package manager

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSetSubscriptionSourceSerializesItsStateWrites(t *testing.T) {
	lock := &fakeConfigUpdateLock{}
	fs := &fakeFileSystem{}
	manager := NewConfigManager(fs, &fakeReleaseSource{}, nil, nil, WithConfigUpdateLock(lock))

	if err := manager.SetSubscriptionSource(context.Background(), "local config"); err != nil {
		t.Fatalf("SetSubscriptionSource failed: %v", err)
	}
	if !lock.acquired || !lock.released {
		t.Fatalf("lock lifecycle = acquired:%v released:%v", lock.acquired, lock.released)
	}
}

func TestSetSubscriptionSourceReturnsLockFailureWithoutWriting(t *testing.T) {
	lock := &fakeConfigUpdateLock{err: ErrConfigUpdateBusy}
	fs := &fakeFileSystem{written: map[string][]byte{subscriptionDataFile: []byte("old")}}
	manager := NewConfigManager(fs, &fakeReleaseSource{}, nil, nil, WithConfigUpdateLock(lock))

	if err := manager.SetSubscriptionSource(context.Background(), "new config"); !errors.Is(err, ErrConfigUpdateBusy) {
		t.Fatalf("SetSubscriptionSource error = %v, want busy error", err)
	}
	if got := string(fs.written[subscriptionDataFile]); got != "old" {
		t.Fatalf("subscription data = %q, want old data", got)
	}
}

func TestAdoptConfigSerializesOverrideWrite(t *testing.T) {
	lock := &fakeConfigUpdateLock{}
	fs := &fakeFileSystem{
		fileExists: map[string]bool{
			configYAML:             true,
			OverrideFilePath:       true,
			subscriptionSourceFile: true,
			subscriptionDataFile:   true,
		},
		written: map[string][]byte{
			configYAML:             []byte("mode: rule\n"),
			OverrideFilePath:       []byte("log-level: info\n"),
			subscriptionSourceFile: []byte("local\n"),
			subscriptionDataFile:   []byte("mode: direct\n"),
		},
	}
	manager := NewConfigManager(fs, &fakeReleaseSource{}, nil, nil, WithConfigUpdateLock(lock))

	if _, err := manager.AdoptConfig(context.Background(), false); err != nil {
		t.Fatalf("AdoptConfig failed: %v", err)
	}
	if !lock.acquired || !lock.released {
		t.Fatalf("lock lifecycle = acquired:%v released:%v", lock.acquired, lock.released)
	}
	if _, ok := fs.written[OverrideFilePath+".tmp"]; ok {
		t.Fatal("override staging file should not remain after a successful adopt")
	}
}

func TestPreviewConfigSerializesLegacyMigration(t *testing.T) {
	lock := &fakeConfigUpdateLock{}
	fs := &fakeFileSystem{
		fileExists: map[string]bool{legacyTemplatePath: true},
		written:    map[string][]byte{legacyTemplatePath: []byte("mode: direct\n")},
	}
	manager := NewConfigManager(fs, &fakeReleaseSource{}, nil, nil, WithConfigUpdateLock(lock))

	if _, err := manager.PreviewConfig(context.Background()); err != nil {
		t.Fatalf("PreviewConfig failed: %v", err)
	}
	if !lock.acquired {
		t.Fatal("PreviewConfig should acquire the config update lock")
	}
	if !fs.FileExists(OverrideFilePath) || fs.FileExists(legacyTemplatePath) {
		t.Fatal("legacy template migration did not complete")
	}
}

func TestAdoptConfigStagingFailureKeepsPreviousOverride(t *testing.T) {
	fs := &overrideStagingFailureFS{
		fakeFileSystem: &fakeFileSystem{
			fileExists: map[string]bool{
				configYAML:             true,
				OverrideFilePath:       true,
				subscriptionSourceFile: true,
				subscriptionDataFile:   true,
			},
			written: map[string][]byte{
				configYAML:             []byte("mode: rule\n"),
				OverrideFilePath:       []byte("log-level: info\n"),
				subscriptionSourceFile: []byte("local\n"),
				subscriptionDataFile:   []byte("mode: direct\n"),
			},
		},
		err: errors.New("cannot stage override"),
	}
	manager := NewConfigManager(fs, &fakeReleaseSource{}, nil, nil)

	if _, err := manager.AdoptConfig(context.Background(), false); err == nil {
		t.Fatal("AdoptConfig should report staging failure")
	}
	if got := string(fs.written[OverrideFilePath]); got != "log-level: info\n" {
		t.Fatalf("override after failed staging = %q, want previous content", got)
	}
}

type overrideStagingFailureFS struct {
	*fakeFileSystem
	err error
}

func (fs *overrideStagingFailureFS) WriteFile(path string, data []byte, perm uint32) error {
	if path == OverrideFilePath+".tmp" {
		return fs.err
	}
	return fs.fakeFileSystem.WriteFile(path, data, perm)
}

type cancelWaitingConfigLock struct {
	started chan struct{}
}

func (l *cancelWaitingConfigLock) Acquire(ctx context.Context) (func(), error) {
	close(l.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestSetSubscriptionSourceHonorsCancellationWhileWaitingForLock(t *testing.T) {
	lock := &cancelWaitingConfigLock{started: make(chan struct{})}
	manager := NewConfigManager(&fakeFileSystem{}, &fakeReleaseSource{}, nil, nil, WithConfigUpdateLock(lock))
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- manager.SetSubscriptionSource(ctx, "local") }()
	select {
	case <-lock.started:
	case <-time.After(time.Second):
		t.Fatal("SetSubscriptionSource did not wait for the update lock")
	}
	cancel()

	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("SetSubscriptionSource error = %v, want cancellation", err)
	}
}
