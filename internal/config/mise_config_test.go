package config

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func unboundMisePaths(t *testing.T) Paths {
	t.Helper()
	paths := Paths{Root: t.TempDir(), Home: t.TempDir(), StateDir: filepath.Join(t.TempDir(), "state")}
	if err := os.Mkdir(paths.InRoot("mise"), 0o755); err != nil {
		t.Fatal(err)
	}
	return paths
}

func TestEnsureMiseConfigBindingConnectsMissingAndEmptyDirectories(t *testing.T) {
	for _, test := range []struct {
		name  string
		empty bool
	}{
		{"missing", false},
		{"empty", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			paths := unboundMisePaths(t)
			if test.empty {
				if err := os.MkdirAll(miseConfigDir(paths), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if err := ensureMiseConfigBinding(paths); err != nil {
				t.Fatal(err)
			}
			if err := requireMiseConfigBinding(paths); err != nil {
				t.Fatalf("installed binding is not current: %v", err)
			}
			info, err := os.Lstat(miseConfigDir(paths))
			if err != nil || info.Mode()&os.ModeSymlink == 0 {
				t.Fatalf("global configuration is not a symlink: info=%v err=%v", info, err)
			}
		})
	}
}

func TestEnsureMiseConfigBindingAdoptsTheExistingMachineLink(t *testing.T) {
	paths := unboundMisePaths(t)
	if err := os.MkdirAll(filepath.Dir(miseConfigDir(paths)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(miseConfigSource(paths), miseConfigDir(paths)); err != nil {
		t.Fatal(err)
	}
	if err := ensureMiseConfigBinding(paths); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureMiseConfigBindingPreservesForeignConfiguration(t *testing.T) {
	for _, test := range []struct {
		name string
		make func(t *testing.T, paths Paths)
	}{
		{
			name: "directory",
			make: func(t *testing.T, paths Paths) {
				t.Helper()
				if err := os.MkdirAll(miseConfigDir(paths), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(miseConfigDir(paths), "config.toml"), []byte("[tools]\nnode = \"22\"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "symlink",
			make: func(t *testing.T, paths Paths) {
				t.Helper()
				foreign := t.TempDir()
				if err := os.MkdirAll(filepath.Dir(miseConfigDir(paths)), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(foreign, miseConfigDir(paths)); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			paths := unboundMisePaths(t)
			test.make(t, paths)
			before, err := os.Lstat(miseConfigDir(paths))
			if err != nil {
				t.Fatal(err)
			}
			if err := ensureMiseConfigBinding(paths); err == nil || !strings.Contains(err.Error(), "left untouched") {
				t.Fatalf("foreign configuration error = %v", err)
			}
			after, err := os.Lstat(miseConfigDir(paths))
			if err != nil {
				t.Fatal(err)
			}
			if before.Mode() != after.Mode() {
				t.Fatalf("foreign configuration mode changed from %v to %v", before.Mode(), after.Mode())
			}
			if test.name == "directory" {
				data, readErr := os.ReadFile(filepath.Join(miseConfigDir(paths), "config.toml"))
				if readErr != nil || string(data) != "[tools]\nnode = \"22\"\n" {
					t.Fatalf("foreign bytes changed: %q, %v", data, readErr)
				}
			}
		})
	}
}

func TestMiseConfigBindingRefusesAnInvalidMachineSource(t *testing.T) {
	paths := Paths{Root: t.TempDir(), Home: t.TempDir()}
	if err := ensureMiseConfigBinding(paths); err == nil || !strings.Contains(err.Error(), "declarations are missing") {
		t.Fatalf("missing source error = %v", err)
	}
	if _, err := os.Lstat(miseConfigDir(paths)); !os.IsNotExist(err) {
		t.Fatalf("an invalid source created a global path: %v", err)
	}
}

func TestMiseInspectionStopsBeforeReadingForeignGlobalConfiguration(t *testing.T) {
	paths := unboundMisePaths(t)
	if err := os.MkdirAll(miseConfigDir(paths), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(miseConfigDir(paths), "config.toml"), []byte("[tools]\nnode = \"22\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &miseStubRunner{}
	checks := miseChecks(t, Inspector{Paths: paths, Runner: runner})
	if len(checks) != 2 || !checks[0].OK || checks[1].OK || !strings.Contains(checks[1].Detail, "left untouched") {
		t.Fatalf("foreign configuration checks = %+v", checks)
	}
	if len(runner.commands) != 1 || runner.commands[0] != "mise --version" {
		t.Fatalf("inspection read foreign configuration: %v", runner.commands)
	}
}

func TestPruneStopsBeforeReadingForeignGlobalConfiguration(t *testing.T) {
	fixture := newPruneFixture(t)
	paths := fixture.pruner.Paths
	if err := os.Remove(miseConfigDir(paths)); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(miseConfigDir(paths), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(miseConfigDir(paths), "config.toml"), []byte("[tools]\nnode = \"22\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &miseStubRunner{}
	fixture.pruner.Mise = runner
	plan, err := fixture.pruner.Plan()
	if err != nil {
		t.Fatal(err)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("prune read foreign configuration: %v", runner.commands)
	}
	if !strings.Contains(strings.Join(plan.warnings, "\n"), "left untouched") {
		t.Fatalf("prune warnings = %v", plan.warnings)
	}
}

func TestMiseRunnerAndOrdinaryMiseReadTheSameGlobalConfiguration(t *testing.T) {
	realMise, err := exec.LookPath("mise")
	if err != nil {
		t.Fatal("mise is not on PATH")
	}
	paths := unboundMisePaths(t)
	if err := os.WriteFile(paths.InRoot("mise", "config.toml"), []byte("[tools]\nnode = \"24\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensureMiseConfigBinding(paths); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(misePath(paths)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realMise, misePath(paths)); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", paths.Home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("MISE_STATE_DIR", t.TempDir())
	t.Setenv("MISE_CACHE_DIR", t.TempDir())
	t.Setenv("MISE_DATA_DIR", t.TempDir())
	for _, name := range miseLocalEnvironment {
		t.Setenv(name, "")
	}

	managed := NewMiseRunner(paths).Run(context.Background(), "mise", "config", "ls", "-J")
	if managed.Err != nil {
		t.Fatalf("Config's Mise runner: %v", managed.Failure())
	}
	ordinary := exec.Command(realMise, "config", "ls", "-J")
	ordinary.Dir = paths.Home
	ordinaryOutput, err := ordinary.CombinedOutput()
	if err != nil {
		t.Fatalf("ordinary mise: %v\n%s", err, ordinaryOutput)
	}
	if strings.TrimSpace(managed.Stdout) != strings.TrimSpace(string(ordinaryOutput)) {
		t.Fatalf("Config loaded %s; ordinary mise loaded %s", managed.Stdout, ordinaryOutput)
	}
	if !strings.Contains(managed.Stdout, paths.InRoot("mise", "config.toml")) &&
		!strings.Contains(managed.Stdout, miseConfigDir(paths)) {
		t.Fatalf("global machine configuration is absent from %s", managed.Stdout)
	}
}
