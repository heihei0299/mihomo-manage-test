package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/heihei0299/mihomo-manage/internal/cli"
	"github.com/heihei0299/mihomo-manage/internal/manager"
)

// captureStderr redirects os.Stderr during fn and returns what was written.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("creating pipe: %v", err)
	}
	os.Stderr = w
	defer func() { os.Stderr = old }()
	fn()
	w.Close()
	out, _ := io.ReadAll(r)
	return string(out)
}

func TestConfigOverrideEditOpensOverrideFile(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "editor-log")
	script := filepath.Join(t.TempDir(), "fake-editor.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho \"$1\" >> \"$EDITOR_LOG\"\n"), 0o755); err != nil {
		t.Fatalf("writing editor script: %v", err)
	}
	t.Setenv("EDITOR", script)
	t.Setenv("EDITOR_LOG", logPath)

	cfg := &tuiMockConfig{}
	code := handleConfigCommand(&cli.Handler{}, cfg, context.Background(), []string{"override", "edit"})

	if code != 1 {
		t.Errorf("expected unreadable editor result to fail, got %d", code)
	}
	if cfg.updateCalled {
		t.Error("UpdateConfig must not be called when editor output cannot be read")
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("editor log not written: %v", err)
	}
	if strings.TrimSpace(string(data)) != manager.OverrideFilePath {
		t.Errorf("editor should open %s, got %q", manager.OverrideFilePath, string(data))
	}
}

func TestConfigOverrideEditSupportsEditorArguments(t *testing.T) {
	script := filepath.Join(t.TempDir(), "fake-editor.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n[ \"$1\" = \"--wait\" ] || exit 7\nprintf 'mode: rule\\n' > \"$2\"\n"), 0o755); err != nil {
		t.Fatalf("writing editor script: %v", err)
	}
	t.Setenv("EDITOR", script+" --wait")
	path := filepath.Join(t.TempDir(), "override.yaml")
	cfg := &tuiMockConfig{}

	if code := cliEditFile(cfg, path, []string{"edit"}); code != 0 {
		t.Fatalf("exit code = %d, want success", code)
	}
	if !cfg.updateCalled {
		t.Fatal("UpdateConfig should be called after a non-empty editor result")
	}
}

func TestConfigOverrideEditEmptyResultDoesNotUpdate(t *testing.T) {
	script := filepath.Join(t.TempDir(), "empty-editor.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n: > \"$1\"\n"), 0o755); err != nil {
		t.Fatalf("writing editor script: %v", err)
	}
	t.Setenv("EDITOR", script)
	path := filepath.Join(t.TempDir(), "override.yaml")
	cfg := &tuiMockConfig{}

	if code := cliEditFile(cfg, path, []string{"edit"}); code != 1 {
		t.Fatalf("exit code = %d, want failure", code)
	}
	if cfg.updateCalled {
		t.Fatal("UpdateConfig must not be called for empty editor result")
	}
}

func TestCLILogsForOSReturnsTypedUnsupportedError(t *testing.T) {
	err := runCLILogsForOS("windows", nil)
	var unsupported manager.UnsupportedPlatformError
	if !errors.As(err, &unsupported) {
		t.Fatalf("error = %v, want UnsupportedPlatformError", err)
	}
	if unsupported.Feature != "logs" || unsupported.GOOS != "windows" {
		t.Fatalf("unsupported error = %+v", unsupported)
	}
}

func TestCLILogsRejectsUnsupportedPlatform(t *testing.T) {
	var code int
	errOut := captureStderr(t, func() {
		code = cliLogsForOS("windows", nil)
	})
	if code != 1 {
		t.Fatalf("exit code = %d, want failure", code)
	}
	if !strings.Contains(errOut, "unsupported logs platform: windows") {
		t.Fatalf("stderr = %q, want typed unsupported diagnostic", errOut)
	}
}

func TestConfigTemplateCommandDeprecated(t *testing.T) {
	cfg := &tuiMockConfig{}
	var code int
	errOut := captureStderr(t, func() {
		code = handleConfigCommand(&cli.Handler{}, cfg, context.Background(), []string{"template", "edit"})
	})

	if code != 1 {
		t.Errorf("expected exit code 1, got %d", code)
	}
	if !strings.Contains(errOut, "config override edit") {
		t.Errorf("stderr should guide to 'config override edit', got %q", errOut)
	}
	if cfg.updateCalled {
		t.Error("UpdateConfig should not be called for deprecated 'config template' command")
	}
}

func TestLegacyTemplateCommandDeprecated(t *testing.T) {
	var code int
	errOut := captureStderr(t, func() {
		code = handleLegacyTemplate()
	})

	if code != 1 {
		t.Errorf("expected exit code 1, got %d", code)
	}
	if !strings.Contains(errOut, "config override edit") {
		t.Errorf("stderr should guide to 'config override edit', got %q", errOut)
	}
}

func TestConfigViewOverrideTab(t *testing.T) {
	m := model{configTab: configTabOverride}
	content := m.configView()

	if !strings.Contains(content, "/opt/mihomo/etc/override.yaml") {
		t.Errorf("config view should show override file path, got %q", content)
	}
	if !strings.Contains(content, "config override edit") {
		t.Errorf("config view should show 'config override edit', got %q", content)
	}
	if strings.Contains(content, "config-template") {
		t.Errorf("config view should not mention config-template, got %q", content)
	}
}

func TestUsageTextNoTemplate(t *testing.T) {
	text := usageText()
	for _, forbidden := range []string{"config template", "template edit", "config-template"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("usage should not contain %q, got %q", forbidden, text)
		}
	}
}

func TestConfigRulesDeprecationUsesOverrideName(t *testing.T) {
	t.Setenv("EDITOR", "/bin/true")
	cfg := &tuiMockConfig{}
	errOut := captureStderr(t, func() {
		handleConfigCommand(&cli.Handler{}, cfg, context.Background(), []string{"rules", "edit"})
	})
	if strings.Contains(errOut, "config-template") {
		t.Errorf("rules deprecation message should not mention config-template, got %q", errOut)
	}
}
