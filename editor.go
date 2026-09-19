package main

import (
	"fmt"
	"os/exec"
	"strings"
	"unicode"
)

func editorCommand(spec, path string) (*exec.Cmd, error) {
	parts, err := splitCommand(spec)
	if err != nil {
		return nil, err
	}
	if len(parts) == 0 {
		return nil, fmt.Errorf("editor is empty")
	}
	args := append(parts[1:], path)
	return exec.Command(parts[0], args...), nil
}

func splitCommand(spec string) ([]string, error) {
	var parts []string
	var current strings.Builder
	var quote rune
	escaped := false
	haveToken := false
	flush := func() {
		if haveToken {
			parts = append(parts, current.String())
			current.Reset()
			haveToken = false
		}
	}

	for _, r := range spec {
		if escaped {
			current.WriteRune(r)
			haveToken = true
			escaped = false
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else if r == '\\' && quote == '"' {
				escaped = true
			} else {
				current.WriteRune(r)
				haveToken = true
			}
			continue
		}
		switch {
		case r == '\\':
			escaped = true
			haveToken = true
		case r == '\'' || r == '"':
			quote = r
			haveToken = true
		case unicode.IsSpace(r):
			flush()
		default:
			current.WriteRune(r)
			haveToken = true
		}
	}
	if escaped {
		return nil, fmt.Errorf("editor command ends with an escape")
	}
	if quote != 0 {
		return nil, fmt.Errorf("editor command has an unterminated quote")
	}
	flush()
	return parts, nil
}
