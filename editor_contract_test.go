package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEditorCommandParsesQuotedExecutableAndArguments(t *testing.T) {
	cmd, err := editorCommand(`"/tmp/my editor" --wait`, "/tmp/subscription")
	if err != nil {
		t.Fatalf("editorCommand failed: %v", err)
	}
	want := []string{"/tmp/my editor", "--wait", "/tmp/subscription"}
	if strings.Join(cmd.Args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("editor args = %v, want %v", cmd.Args, want)
	}
}

func TestCLIEditorPassesArgumentsAndUpdatesAfterSuccess(t *testing.T) {
	temp := t.TempDir()
	logPath := filepath.Join(temp, "args")
	script := filepath.Join(temp, "editor.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$EDITOR_LOG\"\nprintf 'mode: rule\\n' > \"$2\"\n"), 0o755); err != nil {
		t.Fatalf("write editor: %v", err)
	}
	t.Setenv("EDITOR", script+" --wait")
	t.Setenv("EDITOR_LOG", logPath)
	path := filepath.Join(temp, "override.yaml")
	if err := os.WriteFile(path, []byte("mode: rule\n"), 0o600); err != nil {
		t.Fatalf("write override: %v", err)
	}
	cfg := &tuiMockConfig{}

	if code := cliEditFile(context.Background(), cfg, path, []string{"edit"}); code != 0 {
		t.Fatalf("handleConfigCommand code = %d, want 0", code)
	}
	if !cfg.updateCalled {
		t.Fatal("successful editor should update config")
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read editor args: %v", err)
	}
	args := strings.Fields(string(data))
	if len(args) != 2 || args[0] != "--wait" || args[1] != path {
		t.Fatalf("editor args = %v", args)
	}
}

func TestEditedSubscriptionRejectsEmptyContentWithoutUpdating(t *testing.T) {
	path := filepath.Join(t.TempDir(), "subscription")
	if err := os.WriteFile(path, []byte("\n  \t"), 0o600); err != nil {
		t.Fatalf("write subscription: %v", err)
	}
	cfg := &tuiMockConfig{}

	err := applyEditedSubscription(context.Background(), cfg, path)
	if err == nil || !strings.Contains(err.Error(), "cannot be empty") {
		t.Fatalf("applyEditedSubscription error = %v, want empty-content error", err)
	}
	if cfg.updateCalled {
		t.Fatal("empty editor result must not update config")
	}
}

func TestEditedSubscriptionPropagatesUpdateFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "subscription")
	if err := os.WriteFile(path, []byte("mode: rule\n"), 0o600); err != nil {
		t.Fatalf("write subscription: %v", err)
	}
	want := errors.New("validation failed: line 4")
	cfg := &tuiMockConfig{updateErr: want}

	if err := applyEditedSubscription(context.Background(), cfg, path); !errors.Is(err, want) {
		t.Fatalf("applyEditedSubscription error = %v, want %v", err, want)
	}
}

func TestLogsRejectsUnsupportedPlatformBeforeStartingACommand(t *testing.T) {
	var output strings.Builder
	if code := logsForPlatform("darwin", nil, &output); code != 1 {
		t.Fatalf("logs code = %d, want 1", code)
	}
	if !strings.Contains(output.String(), "unsupported logs platform: darwin") {
		t.Fatalf("logs output = %q, want unsupported-platform diagnostic", output.String())
	}
}
