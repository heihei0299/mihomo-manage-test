package manager

import (
	"context"
	"errors"
	"testing"
)

func TestSetSubscriptionSourceUsesConfigUpdateLock(t *testing.T) {
	lock := &fakeConfigUpdateLock{}
	fs := &fakeFileSystem{}
	m := NewConfigManager(fs, &fakeReleaseSource{}, nil, nil, WithConfigUpdateLock(lock))

	if err := m.SetSubscriptionSource(context.Background(), "local subscription"); err != nil {
		t.Fatalf("SetSubscriptionSource failed: %v", err)
	}
	if !lock.acquired || !lock.released {
		t.Fatalf("lock state = acquired:%v released:%v, want both true", lock.acquired, lock.released)
	}
}

func TestSetSubscriptionSourceBusyLeavesStateUntouched(t *testing.T) {
	lock := &fakeConfigUpdateLock{err: ErrConfigUpdateBusy}
	fs := &fakeFileSystem{written: map[string][]byte{subscriptionDataFile: []byte("old")}}
	m := NewConfigManager(fs, &fakeReleaseSource{}, nil, nil, WithConfigUpdateLock(lock))

	if err := m.SetSubscriptionSource(context.Background(), "new subscription"); !errors.Is(err, ErrConfigUpdateBusy) {
		t.Fatalf("SetSubscriptionSource error = %v, want busy", err)
	}
	if string(fs.written[subscriptionDataFile]) != "old" || len(fs.written) != 1 {
		t.Fatalf("state after busy set = %v, want unchanged", fs.written)
	}
}

func TestAdoptConfigUsesConfigUpdateLock(t *testing.T) {
	lock := &fakeConfigUpdateLock{}
	fs := &fakeFileSystem{
		fileExists: map[string]bool{
			configYAML:             true,
			OverrideFilePath:       true,
			subscriptionSourceFile: true,
			subscriptionDataFile:   true,
		},
		written: map[string][]byte{
			configYAML:             []byte("mode: direct\n"),
			OverrideFilePath:       []byte("mode: rule\n"),
			subscriptionSourceFile: []byte("local\n"),
			subscriptionDataFile:   []byte("mode: rule\n"),
		},
	}
	m := NewConfigManager(fs, &fakeReleaseSource{}, nil, nil, WithConfigUpdateLock(lock))

	if _, err := m.AdoptConfig(context.Background(), false); err != nil {
		t.Fatalf("AdoptConfig failed: %v", err)
	}
	if !lock.acquired || !lock.released {
		t.Fatalf("lock state = acquired:%v released:%v, want both true", lock.acquired, lock.released)
	}
}

type waitingConfigUpdateLock struct {
	started chan struct{}
}

func (l *waitingConfigUpdateLock) Acquire(ctx context.Context) (func(), error) {
	close(l.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestSetSubscriptionSourceHonorsCancellationWhileWaiting(t *testing.T) {
	lock := &waitingConfigUpdateLock{started: make(chan struct{})}
	m := NewConfigManager(&fakeFileSystem{}, &fakeReleaseSource{}, nil, nil, WithConfigUpdateLock(lock))
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- m.SetSubscriptionSource(ctx, "local") }()
	<-lock.started
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("SetSubscriptionSource error = %v, want cancellation", err)
	}
}

func TestPreviewConfigUsesConfigUpdateLockForLegacyMigration(t *testing.T) {
	lock := &fakeConfigUpdateLock{}
	fs := &fakeFileSystem{
		fileExists: map[string]bool{OverrideFilePath: true, subscriptionDataFile: true},
		written: map[string][]byte{
			OverrideFilePath:     []byte("mode: rule\n"),
			subscriptionDataFile: []byte("mode: rule\n"),
		},
	}
	m := NewConfigManager(fs, &fakeReleaseSource{}, nil, nil, WithConfigUpdateLock(lock))

	if _, err := m.PreviewConfig(context.Background()); err != nil {
		t.Fatalf("PreviewConfig failed: %v", err)
	}
	if !lock.acquired {
		t.Fatal("PreviewConfig should acquire the config update lock")
	}
}

type failingOverrideStagingFileSystem struct {
	*fakeFileSystem
	err error
}

func (fs *failingOverrideStagingFileSystem) WriteFile(path string, data []byte, perm uint32) error {
	if path == OverrideFilePath+".tmp" {
		return fs.err
	}
	return fs.fakeFileSystem.WriteFile(path, data, perm)
}

func TestAdoptConfigStagingFailurePreservesOverride(t *testing.T) {
	fs := &failingOverrideStagingFileSystem{
		fakeFileSystem: &fakeFileSystem{
			fileExists: map[string]bool{configYAML: true, OverrideFilePath: true, subscriptionSourceFile: true, subscriptionDataFile: true},
			written: map[string][]byte{
				configYAML:             []byte("mode: direct\n"),
				OverrideFilePath:       []byte("mode: rule\n"),
				subscriptionSourceFile: []byte("local\n"),
				subscriptionDataFile:   []byte("mode: rule\n"),
			},
		},
		err: errors.New("override staging failed"),
	}
	m := NewConfigManager(fs, &fakeReleaseSource{}, nil, nil)

	if _, err := m.AdoptConfig(context.Background(), false); err == nil {
		t.Fatal("AdoptConfig should report override staging failure")
	}
	if got := string(fs.written[OverrideFilePath]); got != "mode: rule\n" {
		t.Fatalf("override after staging failure = %q, want old override", got)
	}
}

func TestConfigWriteOperationsPropagateLockCancellation(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(manager ConfigManager) error
	}{
		{name: "set subscription", run: func(m ConfigManager) error {
			return m.SetSubscriptionSource(context.Background(), "local")
		}},
		{name: "adopt config", run: func(m ConfigManager) error {
			_, err := m.AdoptConfig(context.Background(), false)
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := NewConfigManager(&fakeFileSystem{}, &fakeReleaseSource{}, nil, nil, WithConfigUpdateLock(&fakeConfigUpdateLock{err: context.Canceled}))
			if err := test.run(m); !errors.Is(err, context.Canceled) {
				t.Fatalf("operation error = %v, want cancellation", err)
			}
		})
	}
}
