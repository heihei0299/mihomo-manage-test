package manager

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestSupportedReleaseContract(t *testing.T) {
	root := filepath.Dir(managerSourceDir(t))
	workflow, err := os.ReadFile(filepath.Join(root, "..", ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatalf("read release workflow: %v", err)
	}
	text := string(workflow)
	for _, target := range []string{
		"goos: linux\n            goarch: amd64",
		"goos: linux\n            goarch: arm64",
		"goos: darwin\n            goarch: amd64",
		"goos: darwin\n            goarch: arm64",
	} {
		if !strings.Contains(text, target) {
			t.Errorf("release workflow missing target %q", target)
		}
	}
	targets := regexp.MustCompile(`(?m)^\s+- goos: ([^\s]+)\s*\n\s+goarch: ([^\s]+)`).FindAllStringSubmatch(text, -1)
	wantTargets := map[string]bool{
		"linux/amd64":  true,
		"linux/arm64":  true,
		"darwin/amd64": true,
		"darwin/arm64": true,
	}
	if len(targets) != len(wantTargets) {
		t.Fatalf("release targets = %d, want exactly %d", len(targets), len(wantTargets))
	}
	for _, target := range targets {
		if !wantTargets[target[1]+"/"+target[2]] {
			t.Errorf("unexpected release target %s/%s", target[1], target[2])
		}
	}
	if !strings.Contains(text, "GOOS=${{ matrix.goos }} GOARCH=${{ matrix.goarch }} go build") {
		t.Fatal("release build must pass matrix GOOS and GOARCH to go build")
	}
}

func TestRepositoryIdentityIsConsistent(t *testing.T) {
	const canonical = "github.com/heihei0299/mihomo-manage"
	root := filepath.Dir(managerSourceDir(t))
	paths := []string{
		filepath.Join(root, "..", "go.mod"),
		filepath.Join(root, "..", "README.md"),
		filepath.Join(root, "..", ".github", "workflows", "release.yml"),
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		contents := string(data)
		if !strings.Contains(contents, canonical) && !strings.Contains(contents, "CANONICAL_REPOSITORY: heihei0299/mihomo-manage") {
			t.Errorf("%s does not reference canonical repository %q", path, canonical)
		}
	}
	architecture, err := os.ReadFile(filepath.Join(root, "manager", "architecture_test.go"))
	if err != nil {
		t.Fatalf("read architecture check: %v", err)
	}
	if !strings.Contains(string(architecture), canonical) {
		t.Fatal("architecture check does not use canonical repository identity")
	}
	for _, path := range []string{
		filepath.Join(root, "..", "main.go"),
		filepath.Join(root, "..", "main_test.go"),
		filepath.Join(root, "..", "tui.go"),
		filepath.Join(root, "..", "tui_test.go"),
		filepath.Join(root, "..", "internal", "cli", "handler.go"),
		filepath.Join(root, "..", "internal", "cli", "handler_test.go"),
		filepath.Join(root, "..", "internal", "manager", "native_scheduler.go"),
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read internal import file %s: %v", path, err)
		}
		if strings.Contains(string(data), "github.com/anomalyco/mihomo-manager") {
			t.Errorf("%s still uses the old repository identity", path)
		}
	}
}

func TestOSServiceManagerUnsupportedOSIsTypedAndDoesNotRunACommand(t *testing.T) {
	rec := &commandRecorder{}
	svc := &OSServiceManager{cmd: rec, osType: "windows"}

	_, err := svc.IsRunning(context.Background(), "mihomo")
	if err == nil {
		t.Fatal("expected unsupported-platform error")
	}
	var unsupported UnsupportedPlatformError
	if !errors.As(err, &unsupported) {
		t.Fatalf("error = %v, want UnsupportedPlatformError", err)
	}
	if unsupported.Feature != "service" || unsupported.GOOS != "windows" {
		t.Fatalf("unsupported error = %+v", unsupported)
	}
	if len(rec.captured) != 0 {
		t.Fatalf("unsupported service invoked commands: %v", rec.captured)
	}
}
