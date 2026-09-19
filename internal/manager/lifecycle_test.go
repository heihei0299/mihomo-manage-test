package manager

import (
	"context"
	"testing"
)

func noopProgress(ProgressEvent) {}

func assertFileExists(t *testing.T, fs *fakeFileSystem, path string, msg string) {
	t.Helper()
	if fs.written != nil {
		if _, ok := fs.written[path]; ok {
			return
		}
	}
	for _, newPath := range fs.renamed {
		if newPath == path {
			return
		}
	}
	t.Errorf("%s: expected %q to exist (written or renamed)", msg, path)
}

func newLifecycleTestManager(fs *fakeFileSystem, source *fakeReleaseSource, svc *mockServiceManager) LifecycleManager {
	linkStorage(fs, source)
	return NewLifecycleManager(fs, &fakeCmdRunner{}, source, svc)
}

func TestLifecycleInstallThenStatus(t *testing.T) {
	fs := &fakeFileSystem{}
	cmd := &fakeCmdRunner{cmdOutput: "Mihomo Meta v1.18.0 linux amd64"}
	source := &fakeReleaseSource{}
	linkStorage(fs, source)
	svc := &mockServiceManager{}
	life := NewLifecycleManager(fs, cmd, source, svc)
	ctrl := NewServiceControl(fs, cmd, svc, passConfigValidation)

	life.Install(context.Background(), "v1.18.0", true, noopProgress)

	status, err := ctrl.Status(context.Background())
	if err != nil {
		t.Fatalf("Status after install: %v", err)
	}
	if !status.Installed {
		t.Error("Status should show installed after Install")
	}
	if status.InstanceState != Running {
		t.Errorf("Status should be Running, got %v", status.InstanceState)
	}
	if status.Version != "v1.18.0" {
		t.Errorf("Status version should be v1.18.0, got %q", status.Version)
	}
}

func TestLifecycleSubscriptionUpdate(t *testing.T) {
	fs := &fakeFileSystem{
		fileExists: map[string]bool{
			OverrideFilePath:    true,
			subscriptionURLFile: true,
			configYAML:          true,
		},
		written: map[string][]byte{
			OverrideFilePath:    []byte("mode: rule\n"),
			subscriptionURLFile: []byte(`https://example.com/sub`),
			configYAML:          []byte(`old config`),
		},
	}
	dl := &fakeDownloader{content: "proxies:\n  - name: node1\n    type: ss"}
	linkStorage(fs, &dl.fakeReleaseSource)
	svc := &mockServiceManager{}
	m := NewConfigManager(fs, dl, &passValidator{}, func(ctx context.Context) error {
		return svc.Reload(context.Background(), serviceName)
	})

	err := m.UpdateConfig(context.Background())
	if err != nil {
		t.Fatalf("UpdateConfig failed: %v", err)
	}

	assertFileExists(t, fs, configYAML, "config should be updated")
	if !dl.downloadCalled {
		t.Error("BUG 2: remote subscription URL should have been fetched via Download")
	}
	if !svc.reloadCalled {
		t.Error("BUG 3: UpdateConfig should reload service after writing config")
	}
}
