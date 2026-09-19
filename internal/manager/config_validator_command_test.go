package manager

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type validatorCommandStub struct {
	name string
	args []string
	ctx  context.Context
	out  string
	err  error
}

func (s *validatorCommandStub) RunCommand(ctx context.Context, name string, args ...string) (string, error) {
	s.ctx = ctx
	s.name = name
	s.args = append([]string(nil), args...)
	return s.out, s.err
}

func (s *validatorCommandStub) RunCommandIgnoreExit(context.Context, string, ...string) (string, error) {
	return s.out, s.err
}

func TestConfigValidatorUsesCommandRunnerWithStagedConfigDirectory(t *testing.T) {
	runner := &validatorCommandStub{}
	validator := NewConfigValidator(runner)
	ctx := context.WithValue(context.Background(), struct{}{}, "caller")

	if err := validator.Validate(ctx, "/tmp/config-stage/config.yaml"); err != nil {
		t.Fatalf("Validate failed: %v", err)
	}
	if runner.name != binaryPath {
		t.Fatalf("command = %q, want %q", runner.name, binaryPath)
	}
	wantArgs := []string{"-t", "-d", "/tmp/config-stage"}
	if strings.Join(runner.args, "\x00") != strings.Join(wantArgs, "\x00") {
		t.Fatalf("args = %v, want %v", runner.args, wantArgs)
	}
	if runner.ctx == nil || runner.ctx.Value(struct{}{}) != "caller" {
		t.Fatal("validator did not pass caller context")
	}
}

func TestConfigValidatorKeepsCommandOutputInFailure(t *testing.T) {
	commandErr := errors.New("exit status 1")
	validator := NewConfigValidator(&validatorCommandStub{out: "line 4: invalid mode", err: commandErr})

	err := validator.Validate(context.Background(), "/tmp/config.yaml")
	if !errors.Is(err, commandErr) {
		t.Fatalf("Validate error = %v, want command error", err)
	}
	if !strings.Contains(err.Error(), "line 4: invalid mode") {
		t.Fatalf("Validate error = %v, want command output", err)
	}
}

func TestConfigValidatorPreservesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	validator := NewConfigValidator(&validatorCommandStub{err: context.Canceled})

	if err := validator.Validate(ctx, "/tmp/config.yaml"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Validate error = %v, want cancellation", err)
	}
}

func TestConfigApplyStatusIncludesValidatorDiagnostic(t *testing.T) {
	runner := &validatorCommandStub{out: "line 4: invalid mode", err: errors.New("exit status 1")}
	manager := NewConfigManager(localApplyTestFileSystem(), &fakeReleaseSource{}, NewConfigValidator(runner), nil)

	if err := manager.UpdateConfig(context.Background()); err == nil {
		t.Fatal("UpdateConfig should report validation failure")
	}
	status, err := manager.LastConfigApply(context.Background())
	if err != nil {
		t.Fatalf("LastConfigApply failed: %v", err)
	}
	if status.State != ConfigValidationFailed || !strings.Contains(status.ErrorSummary, "line 4: invalid mode") {
		t.Fatalf("status = %+v, want validation diagnostic", status)
	}
}
