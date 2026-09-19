package manager

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"

	"gopkg.in/yaml.v3"
)

// AdoptReport describes the differences found between the current config.yaml
// and the rendered config (subscription + override file).
type AdoptReport struct {
	NoChanges bool
	Fields    []string // scalar/map fields to be adopted into the override file
	ArrayDiff []string // array fields that differ (reported, never adopted)
	LargeDiff bool     // len(Fields) >= 5
}

// Adopt compares the current config.yaml with the rendered config and moves
// top-level scalar/map differences into the override file, preserving its
// existing content. Array differences are reported but never adopted. A large
// diff (>= 5 fields) requires force. Idempotent: a second run reports no
// changes once the differences have been adopted.
func (p *configPipeline) AdoptConfig(ctx context.Context, force bool) (AdoptReport, error) {
	report := AdoptReport{}
	release, err := p.acquireConfigUpdate(ctx)
	if err != nil {
		return report, err
	}
	defer release()

	cur, err := p.fs.ReadFile(configYAML)
	if err != nil {
		if os.IsNotExist(err) {
			return report, fmt.Errorf("config.yaml does not exist; nothing to adopt")
		}
		return report, err
	}

	rendered, err := p.previewConfig(ctx, nil)
	if err != nil {
		return report, err
	}

	var curMap, rendMap map[string]any
	if err := yaml.Unmarshal(cur, &curMap); err != nil {
		return report, fmt.Errorf("parsing config.yaml: %w", err)
	}
	if err := yaml.Unmarshal([]byte(rendered), &rendMap); err != nil {
		return report, fmt.Errorf("parsing rendered config: %w", err)
	}

	for k, v := range curMap {
		rv, ok := rendMap[k]
		if ok && reflect.DeepEqual(v, rv) {
			continue
		}
		if _, isList := v.([]any); isList {
			report.ArrayDiff = append(report.ArrayDiff, k)
			continue
		}
		report.Fields = append(report.Fields, k)
	}
	sort.Strings(report.Fields)
	sort.Strings(report.ArrayDiff)

	if len(report.Fields) == 0 && len(report.ArrayDiff) == 0 {
		report.NoChanges = true
		return report, nil
	}
	// Array-only differences are informational; nothing to write.
	if len(report.Fields) == 0 {
		return report, nil
	}

	report.LargeDiff = len(report.Fields) >= 5
	if report.LargeDiff && !force {
		return report, ErrAdoptNeedsConfirmation
	}

	if err := p.writeOverrideFields(report.Fields, curMap); err != nil {
		return report, err
	}
	return report, nil
}

// writeOverrideFields merges the given fields (values from curMap) into the
// override file, preserving any content already present.
func (p *configPipeline) writeOverrideFields(fields []string, curMap map[string]any) error {
	override := map[string]any{}
	if data, err := p.fs.ReadFile(OverrideFilePath); err == nil {
		if err := yaml.Unmarshal(data, &override); err != nil {
			return fmt.Errorf("parsing override file: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	for _, k := range fields {
		override[k] = curMap[k]
	}

	out, err := yaml.Marshal(override)
	if err != nil {
		return fmt.Errorf("marshaling override file: %w", err)
	}
	if err := p.fs.MkdirAll(configDir, filePermUserRWX); err != nil {
		return err
	}
	tmpPath := OverrideFilePath + ".tmp"
	if err := p.fs.WriteFile(tmpPath, out, filePermUserRW); err != nil {
		return errors.Join(fmt.Errorf("staging override file: %w", err), p.fs.Remove(tmpPath))
	}
	if err := p.fs.Rename(tmpPath, OverrideFilePath); err != nil {
		return errors.Join(fmt.Errorf("committing override file: %w", err), p.fs.Remove(tmpPath))
	}
	return nil
}
