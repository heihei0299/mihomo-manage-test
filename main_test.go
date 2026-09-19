package main

import (
	"context"
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

	if code != 0 {
		t.Errorf("expected exit code 0, got %d", code)
	}
	if !cfg.updateCalled {
		t.Error("UpdateConfig should be called after editing")
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("editor log not written: %v", err)
	}
	if strings.TrimSpace(string(data)) != manager.OverrideFilePath {
		t.Errorf("editor should open %s, got %q", manager.OverrideFilePath, string(data))
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
