package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestCLIEditorDoesNotUpdateAfterEmptyResult(t *testing.T) {
	script := filepath.Join(t.TempDir(), "empty-editor.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n: > \"$1\"\n"), 0o755); err != nil {
		t.Fatalf("write editor: %v", err)
	}
	t.Setenv("EDITOR", script)
	cfg := &tuiMockConfig{}

	path := filepath.Join(t.TempDir(), "override.yaml")
	if err := os.WriteFile(path, []byte("mode: rule\n"), 0o600); err != nil {
		t.Fatalf("write override: %v", err)
	}
	if code := cliEditFile(context.Background(), cfg, path, []string{"edit"}); code != 1 {
		t.Fatalf("handleConfigCommand code = %d, want 1", code)
	}
	if cfg.updateCalled {
		t.Fatal("empty editor result must not update config")
	}
}
