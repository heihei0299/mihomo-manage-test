package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func editorCommand(editor, path string) (*exec.Cmd, error) {
	parts := strings.Fields(editor)
	if len(parts) == 0 {
		return nil, fmt.Errorf("editor is empty")
	}
	args := append(append([]string{}, parts[1:]...), path)
	return exec.Command(parts[0], args...), nil
}

func configuredEditorCommand(path string) (*exec.Cmd, error) {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	return editorCommand(editor, path)
}
