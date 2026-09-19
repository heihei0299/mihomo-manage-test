package manager

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type validationCommandRunner struct {
	name string
	args []string
	ctx  context.Context
	out  string
	err  error
}

func (r *validationCommandRunner) RunCommand(ctx context.Context, name string, args ...string) (string, error) {
	r.ctx = ctx
	r.name = name
	r.args = append([]string(nil), args...)
	return r.out, r.err
}

func (r *validationCommandRunner) RunCommandIgnoreExit(context.Context, string, ...string) (string, error) {
	return r.out, r.err
}

func TestConfigValidatorUsesCommandRunnerForStagedConfig(t *testing.T) {
	runner := &validationCommandRunner{}
	validator := NewConfigValidator(runner)
	ctx := context.WithValue(context.Background(), struct{}{}, "marker")

	if err := validator.Validate(ctx, "/tmp/config-staging/config.yaml"); err != nil {
		t.Fatalf("Validate failed: %v", err)
	}
	if runner.name != binaryPath {
		t.Fatalf("command = %q, want %q", runner.name, binaryPath)
	}
	wantArgs := []string{"-t", "-d", "/tmp/config-staging"}
	if strings.Join(runner.args, "\x00") != strings.Join(wantArgs, "\x00") {
		t.Fatalf("args = %v, want %v", runner.args, wantArgs)
	}
	if runner.ctx == nil || runner.ctx.Value(struct{}{}) != "marker" {
		t.Fatal("validator did not pass caller context to command runner")
	}
}

func TestConfigValidatorPreservesCommandOutputOnFailure(t *testing.T) {
	commandErr := errors.New("exit status 1")
	runner := &validationCommandRunner{out: "parse error at line 4", err: commandErr}
	validator := NewConfigValidator(runner)

	err := validator.Validate(context.Background(), "/tmp/config.yaml")
	if !errors.Is(err, commandErr) {
		t.Fatalf("Validate error = %v, want command error", err)
	}
	if !strings.Contains(err.Error(), "parse error at line 4") {
		t.Fatalf("Validate error = %v, want command output", err)
	}
}

func TestConfigApplyStatusIncludesValidationCommandDiagnostic(t *testing.T) {
	runner := &validationCommandRunner{
		out: "parse error at line 4",
		err: errors.New("exit status 1"),
	}
	m := NewConfigManager(localApplyTestFileSystem(), &fakeReleaseSource{}, NewConfigValidator(runner), nil)

	if err := m.UpdateConfig(context.Background()); err == nil {
		t.Fatal("UpdateConfig should report validation failure")
	}
	status, err := m.LastConfigApply(context.Background())
	if err != nil {
		t.Fatalf("LastConfigApply failed: %v", err)
	}
	if status.State != ConfigValidationFailed || !strings.Contains(status.ErrorSummary, "parse error at line 4") {
		t.Fatalf("status = %+v, want validation diagnostic", status)
	}
}

func TestConfigValidatorReturnsCancellationUnwrapped(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runner := &validationCommandRunner{err: context.Canceled}
	validator := NewConfigValidator(runner)

	if err := validator.Validate(ctx, "/tmp/config.yaml"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Validate error = %v, want context cancellation", err)
	}
}
