package manager

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

var bannedGenericManagerFiles = map[string]struct{}{
	"util.go":      {},
	"utils.go":     {},
	"helper.go":    {},
	"helpers.go":   {},
	"common.go":    {},
	"misc.go":      {},
	"constants.go": {},
	"shared.go":    {},
}

func managerSourceDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve architecture test path")
	}
	return filepath.Dir(file)
}

func managerFileOwner(name string) string {
	switch {
	case strings.HasPrefix(name, "config"), name == "merge.go", name == "adopt.go", name == "lock.go":
		return "config"
	case strings.HasPrefix(name, "lifecycle"):
		return "lifecycle"
	case strings.HasPrefix(name, "service"):
		return "service"
	case strings.HasPrefix(name, "schedule"), name == "native_scheduler.go":
		return "schedule"
	case name == "system.go":
		return "os-seam"
	case name == "manager.go", name == "errors.go", name == "paths.go", name == "defaults.go":
		return "shared"
	default:
		return ""
	}
}

func TestManagerProductionFilesHaveExplicitOwner(t *testing.T) {
	entries, err := os.ReadDir(managerSourceDir(t))
	if err != nil {
		t.Fatalf("read manager directory: %v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		if _, banned := bannedGenericManagerFiles[name]; banned {
			t.Errorf("%s is a generic dumping-ground name; assign the code to an owning domain or dedicated package", name)
			continue
		}
		if owner := managerFileOwner(name); owner == "" {
			t.Errorf("%s has no declared manager domain owner; update the architecture deliberately before adding it", name)
		}
	}
}

func TestSchedulerDoesNotImportManager(t *testing.T) {
	schedulerDir := filepath.Join(managerSourceDir(t), "..", "scheduler")
	entries, err := os.ReadDir(schedulerDir)
	if err != nil {
		t.Fatalf("read scheduler directory: %v", err)
	}
	const managerImport = "github.com/heihei0299/mihomo-manage/internal/manager"
	fileset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		path := filepath.Join(schedulerDir, name)
		file, err := parser.ParseFile(fileset, path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, spec := range file.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatalf("unquote import in %s: %v", name, err)
			}
			if importPath == managerImport {
				t.Errorf("%s imports manager; scheduler must remain a one-way dependency below manager", name)
			}
		}
	}
}
