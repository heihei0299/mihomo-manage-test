package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"unicode"
)

func editorCommand(editor, path string) (*exec.Cmd, error) {
	parts, err := splitEditorCommand(editor)
	if err != nil {
		return nil, err
	}
	args := append(append([]string{}, parts[1:]...), path)
	return exec.Command(parts[0], args...), nil
}

func splitEditorCommand(input string) ([]string, error) {
	var parts []string
	var current strings.Builder
	var quote rune
	escaped := false
	hasToken := false
	for _, r := range input {
		if escaped {
			current.WriteRune(r)
			escaped = false
			hasToken = true
			continue
		}
		if r == '\\' && quote != '\'' {
			escaped = true
			hasToken = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				current.WriteRune(r)
			}
			hasToken = true
			continue
		}
		switch {
		case r == '\'' || r == '"':
			quote = r
			hasToken = true
		case unicode.IsSpace(r):
			if hasToken {
				parts = append(parts, current.String())
				current.Reset()
				hasToken = false
			}
		default:
			current.WriteRune(r)
			hasToken = true
		}
	}
	if escaped || quote != 0 {
		return nil, fmt.Errorf("editor command has an unfinished escape or quote")
	}
	if hasToken {
		parts = append(parts, current.String())
	}
	if len(parts) == 0 || parts[0] == "" {
		return nil, fmt.Errorf("editor is empty")
	}
	return parts, nil
}

func configuredEditorCommand(path string) (*exec.Cmd, error) {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	return editorCommand(editor, path)
}
