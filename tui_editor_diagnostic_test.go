package main

import (
	"errors"
	"strings"
	"testing"
)

func TestTUISubscriptionEditShowsConfigUpdateDiagnostic(t *testing.T) {
	m := model{
		mode:       modeConfig,
		configTab:  configTabSubscription,
		execResult: "failed",
		actionErr:  errors.New("config update failed: validation failed: line 4"),
	}

	if view := m.configView(); !strings.Contains(view, "config update failed: validation failed: line 4") {
		t.Fatalf("config view = %q, want update diagnostic", view)
	}
}
