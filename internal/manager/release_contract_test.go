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

func TestReleaseMatrixMatchesSupportedPlatforms(t *testing.T) {
	root := filepath.Dir(managerSourceDir(t))
	data, err := os.ReadFile(filepath.Join(root, "..", ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatalf("read release workflow: %v", err)
	}
	text := string(data)
	if strings.Contains(text, "goos: windows") {
		t.Fatal("release workflow must not publish Windows artifacts")
	}

	matches := regexp.MustCompile(`(?m)^\s+- goos: ([^\s]+)\s*\n\s+goarch: ([^\s]+)`).FindAllStringSubmatch(text, -1)
	got := make(map[string]bool, len(matches))
	for _, match := range matches {
		got[match[1]+"/"+match[2]] = true
	}
	want := map[string]bool{
		"linux/amd64":  true,
		"linux/arm64":  true,
		"darwin/amd64": true,
		"darwin/arm64": true,
	}
	if len(got) != len(want) {
		t.Fatalf("release targets = %v, want exactly %v", got, want)
	}
	for target := range want {
		if !got[target] {
			t.Errorf("release targets missing %s", target)
		}
	}
	if !strings.Contains(text, "GOOS=${{ matrix.goos }} GOARCH=${{ matrix.goarch }} go build") {
		t.Fatal("release build must pass matrix GOOS and GOARCH to go build")
	}
}

func TestRepositoryIdentityIsCanonical(t *testing.T) {
	const canonical = "github.com/heihei0299/mihomo-manage"
	root := filepath.Dir(managerSourceDir(t))
	for _, path := range []string{
		filepath.Join(root, "..", "go.mod"),
		filepath.Join(root, "..", "README.md"),
		filepath.Join(root, "..", ".github", "workflows", "release.yml"),
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if !strings.Contains(string(data), canonical) && !strings.Contains(string(data), "CANONICAL_REPOSITORY: heihei0299/mihomo-manage") {
			t.Errorf("%s does not identify %s", path, canonical)
		}
	}
	for _, path := range []string{
		filepath.Join(root, "..", "main.go"),
		filepath.Join(root, "..", "tui.go"),
		filepath.Join(root, "..", "internal", "cli", "handler.go"),
		filepath.Join(root, "..", "internal", "manager", "native_scheduler.go"),
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if strings.Contains(string(data), "github.com/anomalyco/mihomo-manager") {
			t.Errorf("%s still uses the old module path", path)
		}
	}
}

func TestServiceManagerRejectsUnsupportedPlatformWithoutRunningACommand(t *testing.T) {
	recorder := &commandRecorder{}
	service := &OSServiceManager{cmd: recorder, osType: "windows"}

	_, err := service.IsRunning(context.Background(), serviceName)
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
	if len(recorder.captured) != 0 {
		t.Fatalf("unsupported service invoked commands: %v", recorder.captured)
	}
}

func TestServiceUnitSelectionRejectsUnsupportedPlatform(t *testing.T) {
	if _, err := serviceUnitPathFor("windows"); err == nil {
		t.Fatal("service unit path should reject unsupported platforms")
	}
	if _, err := serviceUnitContentFor("windows", false); err == nil {
		t.Fatal("service unit content should reject unsupported platforms")
	}
}
