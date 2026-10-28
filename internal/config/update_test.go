package config

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestUpdaterDoesNotApplyAPlanWithoutWork(t *testing.T) {
	updater := Updater{
		Version: "dev",
	}
	plan := UpdatePlan{

		Groups: []UpdateGroup{{Name: "Config", State: UpdateSkipped}},
	}
	if err := updater.Apply(plan); err != nil {
		t.Fatal(err)
	}
}

func TestReleasedUpdaterInstallsAndReexecutesTheVerifiedPermanentRelease(t *testing.T) {
	paths := testPaths(t)
	releaseMise := releaseMisePath(paths)
	canonicalConfig := configCommandPath(paths)
	releaseDirectory := t.TempDir()
	releaseConfig := filepath.Join(releaseDirectory, "config")
	releaseCache := filepath.Join(configReleaseRoot(paths), "cache")
	releaseState := filepath.Join(configReleaseRoot(paths), "state")
	logPath := filepath.Join(t.TempDir(), "commands")
	t.Setenv("UPDATE_TEST_LOG", logPath)
	t.Setenv("UPDATE_TEST_RELEASE_DIR", releaseDirectory)
	t.Setenv("PATH", filepath.Dir(canonicalConfig)+string(os.PathListSeparator)+os.Getenv("PATH"))
	writeUpdateExecutable(t, releaseMise, `#!/bin/sh
printf '%s|%s|%s|%s|%s|%s|%s|%s|%s\n' "$*" "$MISE_CONFIG_DIR" "$MISE_AUTO_UPDATE" "$MISE_NO_CONFIG" "$MISE_GITHUB_GITHUB_ATTESTATIONS" "$MISE_MINIMUM_RELEASE_AGE" "$MISE_CACHE_DIR" "$MISE_STATE_DIR" "$MISE_USE_VERSIONS_HOST" >> "$UPDATE_TEST_LOG"
if [ "$1" = --version ]; then
  printf '%s\n' '`+releaseMiseVersion+`'
fi
if [ "$2" = latest ]; then
  printf '0.5.0\n'
fi
if [ "$2" = where ]; then
  printf '%s\n' "$UPDATE_TEST_RELEASE_DIR"
fi
`)
	writeUpdateExecutable(t, releaseConfig, `#!/bin/sh
printf 'candidate %s|%s|%s|%s|%s|%s|%s|%s|%s\n' "$*" "$MISE_CONFIG_DIR" "$MISE_AUTO_UPDATE" "$MISE_NO_CONFIG" "$MISE_GITHUB_GITHUB_ATTESTATIONS" "$MISE_MINIMUM_RELEASE_AGE" "$MISE_CACHE_DIR" "$MISE_STATE_DIR" "$MISE_USE_VERSIONS_HOST" >> "$UPDATE_TEST_LOG"
if [ "$1" = --version ]; then
  printf 'config v0.5.0\n'
fi
`)
	writeUpdateExecutable(t, canonicalConfig, "#!/bin/sh\nprintf 'config v0.5.0\\n'\n")

	var output bytes.Buffer
	updater := newUpdateTestUpdater(paths, &output, "v0.4.0")
	updater.OperationEvents = true

	updater.ReleaseMise.Stdout, updater.ReleaseMise.Stderr = &output, &output

	var reexecPath string
	var reexecArgs, reexecEnvironment []string
	updater.Reexec = func(path string, args, environment []string) error {
		reexecPath = path
		reexecArgs = slices.Clone(args)
		reexecEnvironment = slices.Clone(environment)
		return nil
	}
	if err := updater.Apply(testUpdatePlan("v0.5.0")); err != nil {
		t.Fatal(err)
	}

	commands, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"--version||0|1|||||",
		"--no-config install github:azohra/config@0.5.0||0|1|false|0s|" + releaseCache + "|" + releaseState + "|0",
		"--no-config where github:azohra/config@0.5.0||0|1|false|0s|" + releaseCache + "|" + releaseState + "|0",
		"candidate --version||0|1|false|0s|" + releaseCache + "|" + releaseState + "|0",
		"candidate install||0|1|false|0s|" + releaseCache + "|" + releaseState + "|0",
		"",
	}, "\n")
	if string(commands) != want {
		t.Fatalf("commands =\n%s\nwant =\n%s", commands, want)
	}
	if _, err := os.Lstat(paths.InHome(".local", "bin", "mise")); !os.IsNotExist(err) {
		t.Fatalf("release acquisition changed the machine Mise resource: %v", err)
	}
	if reexecPath != canonicalConfig || !slices.Equal(reexecArgs, []string{canonicalConfig, "update", "--yes"}) {
		t.Fatalf("re-exec = %q %q", reexecPath, reexecArgs)
	}
	if environmentValue(reexecEnvironment, UpdateReexecEnv) != "v0.5.0" {
		t.Fatalf("re-exec environment does not bind %s to the installed release", UpdateReexecEnv)
	}
	if environmentValue(reexecEnvironment, OperationEventsEnv) != "1" {
		t.Fatal("re-exec did not preserve the operation event stream")
	}
	if !strings.Contains(output.String(), "Config v0.5.0 installed") {
		t.Fatalf("output missing installed release:\n%s", output.String())
	}
}

func TestReleasedUpdaterDoesNotReinstallTheCurrentCanonicalCommand(t *testing.T) {
	paths := testPaths(t)
	canonical := configCommandPath(paths)
	logPath := filepath.Join(t.TempDir(), "commands")
	t.Setenv("UPDATE_TEST_LOG", logPath)
	writeUpdateExecutable(t, canonical, "#!/bin/sh\nprintf 'config v0.5.0\\n'\n")
	writeUpdateExecutable(t, releaseMisePath(paths), `#!/bin/sh
printf '%s\n' "$*" >> "$UPDATE_TEST_LOG"
if [ "$1" = --version ]; then printf '`+releaseMiseVersion+`\n'; fi
if [ "$2" = latest ]; then printf '0.5.0\n'; fi
`)
	var output bytes.Buffer
	updater := newUpdateTestUpdater(paths, &output, "v0.5.0")

	updater.CurrentExecutable = func() (string, error) { return canonical, nil }
	updater.Reexec = func(string, []string, []string) error {
		t.Fatal("current canonical Config attempted to re-exec")
		return nil
	}
	if err := updater.Apply(testUpdatePlan("v0.5.0")); err != nil {
		t.Fatal(err)
	}
	commands, err := os.ReadFile(logPath)
	if !os.IsNotExist(err) || len(commands) != 0 {
		t.Fatalf("current Config ran release commands %q: %v", commands, err)
	}
	if !strings.Contains(output.String(), "Config v0.5.0 is current") {
		t.Fatalf("current result was not narrated:\n%s", output.String())
	}
}

func TestReleasedUpdaterStopsWhenReleaseAcquisitionFails(t *testing.T) {
	paths := testPaths(t)
	logPath := filepath.Join(t.TempDir(), "commands")
	t.Setenv("UPDATE_TEST_LOG", logPath)
	writeUpdateExecutable(t, releaseMisePath(paths), `#!/bin/sh
printf '%s\n' "$*" >> "$UPDATE_TEST_LOG"
if [ "$1" = --version ]; then
  printf '%s\n' '`+releaseMiseVersion+`'
fi
if [ "$2" = latest ]; then
  exit 23
fi
`)

	var output bytes.Buffer
	updater := newUpdateTestUpdater(paths, &output, "v0.4.0")
	updater.ReleaseMise.Stdout, updater.ReleaseMise.Stderr = &output, &output
	updater.Reexec = func(string, []string, []string) error {
		t.Fatal("failed acquisition attempted to re-exec")
		return nil
	}
	plan, err := updater.Plan()
	if err != nil || !plan.Blocked || !strings.Contains(plan.Groups[0].Summary, "release discovery unavailable") {
		t.Fatalf("Plan() = %+v, %v, want blocked release discovery", plan, err)
	}
	commands, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	want := "--version\n--no-config cache clear\n--no-config latest " + configReleaseBackend + "\n"
	if string(commands) != want {
		t.Fatalf("commands = %q, want %q", commands, want)
	}
}

func TestReleasedUpdaterRefusesAnUnverifiedInstalledBuild(t *testing.T) {
	paths := testPaths(t)
	releaseDirectory := t.TempDir()
	t.Setenv("UPDATE_TEST_RELEASE_DIR", releaseDirectory)
	writeUpdateExecutable(t, releaseMisePath(paths), `#!/bin/sh
if [ "$1" = --version ]; then
  printf '%s\n' '`+releaseMiseVersion+`'
fi
if [ "$2" = latest ]; then
  printf '0.4.0\n'
fi
if [ "$2" = where ]; then
  printf '%s\n' "$UPDATE_TEST_RELEASE_DIR"
fi
`)
	writeUpdateExecutable(t, filepath.Join(releaseDirectory, "config"), "#!/bin/sh\nif [ \"$1\" = --version ]; then\n  printf 'config v0.4.0\\n'\nfi\n")
	writeUpdateExecutable(t, configCommandPath(paths), "#!/bin/sh\nprintf 'config dev\\n'\n")

	var output bytes.Buffer
	updater := newUpdateTestUpdater(paths, &output, "v0.4.0")
	updater.ReleaseMise.Stdout, updater.ReleaseMise.Stderr = &output, &output
	updater.Reexec = func(string, []string, []string) error {
		t.Fatal("unverified build attempted to re-exec")
		return nil
	}
	err := updater.Apply(testUpdatePlan("v0.4.0"))
	if err == nil || !strings.Contains(err.Error(), "installed Config version is unreadable") {
		t.Fatalf("Update() error = %v, want installed version failure", err)
	}
}

func TestReleasedUpdaterVerifiesTheExactInstalledRelease(t *testing.T) {
	paths := testPaths(t)
	releaseDirectory := t.TempDir()
	t.Setenv("UPDATE_TEST_RELEASE_DIR", releaseDirectory)
	writeUpdateExecutable(t, releaseMisePath(paths), `#!/bin/sh
if [ "$1" = --version ]; then
  printf '%s\n' '`+releaseMiseVersion+`'
fi
if [ "$2" = latest ]; then
  printf '0.5.0\n'
fi
if [ "$2" = where ]; then
  printf '%s\n' "$UPDATE_TEST_RELEASE_DIR"
fi
`)
	writeUpdateExecutable(t, filepath.Join(releaseDirectory, "config"), "#!/bin/sh\nif [ \"$1\" = --version ]; then\n  printf 'config v0.5.0\\n'\nfi\n")
	writeUpdateExecutable(t, configCommandPath(paths), "#!/bin/sh\nprintf 'config v0.4.0\\n'\n")

	var output bytes.Buffer
	updater := newUpdateTestUpdater(paths, &output, "v0.4.0")
	updater.ReleaseMise.Stdout, updater.ReleaseMise.Stderr = &output, &output
	updater.Reexec = func(string, []string, []string) error {
		t.Fatal("mismatched installed release attempted to re-exec")
		return nil
	}
	err := updater.Apply(testUpdatePlan("v0.5.0"))
	if err == nil || !strings.Contains(err.Error(), "installed Config is v0.4.0, want resolved release v0.5.0") {
		t.Fatalf("Update() error = %v, want exact installed release failure", err)
	}
}

func TestReleasedUpdaterVerifiesTheAcquiredExecutableBeforeInstall(t *testing.T) {
	paths := testPaths(t)
	releaseDirectory := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "commands")
	t.Setenv("UPDATE_TEST_LOG", logPath)
	t.Setenv("UPDATE_TEST_RELEASE_DIR", releaseDirectory)
	writeUpdateExecutable(t, releaseMisePath(paths), `#!/bin/sh
if [ "$1" = --version ]; then
  printf '%s\n' '`+releaseMiseVersion+`'
fi
if [ "$2" = latest ]; then
  printf '0.5.0\n'
fi
if [ "$2" = where ]; then
  printf '%s\n' "$UPDATE_TEST_RELEASE_DIR"
fi
`)
	writeUpdateExecutable(t, filepath.Join(releaseDirectory, "config"), `#!/bin/sh
printf '%s\n' "$*" >> "$UPDATE_TEST_LOG"
if [ "$1" = --version ]; then
  printf 'config v0.4.0\n'
fi
`)

	var output bytes.Buffer
	updater := newUpdateTestUpdater(paths, &output, "v0.4.0")
	updater.ReleaseMise.Stdout, updater.ReleaseMise.Stderr = &output, &output
	updater.Reexec = func(string, []string, []string) error {
		t.Fatal("mismatched release executable attempted to re-exec")
		return nil
	}
	err := updater.Apply(testUpdatePlan("v0.5.0"))
	if err == nil || !strings.Contains(err.Error(), "release executable is v0.4.0, want resolved release v0.5.0") {
		t.Fatalf("Update() error = %v, want acquired executable version failure", err)
	}
	commands, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(commands) != "--version\n" {
		t.Fatalf("release executable commands = %q, want version check without install", commands)
	}
}

func TestReleasedUpdaterRefusesADowngradeBeforeInstall(t *testing.T) {
	paths := testPaths(t)
	logPath := filepath.Join(t.TempDir(), "commands")
	t.Setenv("UPDATE_TEST_LOG", logPath)
	writeUpdateExecutable(t, releaseMisePath(paths), `#!/bin/sh
printf '%s\n' "$*" >> "$UPDATE_TEST_LOG"
if [ "$1" = --version ]; then
  printf '%s\n' '`+releaseMiseVersion+`'
fi
if [ "$2" = latest ]; then
  printf '0.3.0\n'
fi
`)

	var output bytes.Buffer
	updater := newUpdateTestUpdater(paths, &output, "v0.4.0")
	updater.ReleaseMise.Stdout, updater.ReleaseMise.Stderr = &output, &output
	updater.Reexec = func(string, []string, []string) error {
		t.Fatal("downgrade attempted to re-exec")
		return nil
	}
	err := updater.Apply(testUpdatePlan("v0.3.0"))
	if err == nil || !strings.Contains(err.Error(), "older release v0.3.0") {
		t.Fatalf("Update() error = %v, want downgrade refusal", err)
	}
	commands, readErr := os.ReadFile(logPath)
	if !os.IsNotExist(readErr) || len(commands) != 0 {
		t.Fatalf("downgrade plan ran commands %q: %v", commands, readErr)
	}
}

func TestReexecutedUpdaterRequiresTheCanonicalCommand(t *testing.T) {
	paths := testPaths(t)
	t.Setenv(UpdateReexecEnv, "v0.5.0")
	writeUpdateExecutable(t, paths.InHome(".local", "bin", "mise"), "#!/bin/sh\nexit 0\n")
	writeUpdateExecutable(t, configCommandPath(paths), "#!/bin/sh\nexit 0\n")
	other := filepath.Join(t.TempDir(), "config")
	writeUpdateExecutable(t, other, "#!/bin/sh\nexit 0\n")

	updater := newUpdateTestUpdater(paths, &bytes.Buffer{}, "v0.5.0")
	updater.CurrentExecutable = func() (string, error) { return other, nil }
	err := updater.Apply(testUpdatePlan("v0.5.0"))
	if err == nil || !strings.Contains(err.Error(), "outside the canonical command") {
		t.Fatalf("Update() error = %v, want canonical command failure", err)
	}
}

func TestReexecutedUpdaterRequiresItsVersionBoundMarker(t *testing.T) {
	paths := testPaths(t)
	t.Setenv(UpdateReexecEnv, "v0.4.0")
	writeUpdateExecutable(t, paths.InHome(".local", "bin", "mise"), "#!/bin/sh\nexit 0\n")
	writeUpdateExecutable(t, configCommandPath(paths), "#!/bin/sh\nexit 0\n")

	updater := newUpdateTestUpdater(paths, &bytes.Buffer{}, "v0.5.0")
	err := updater.Apply(testUpdatePlan("v0.5.0"))
	if err == nil || !strings.Contains(err.Error(), `resumed with version "v0.4.0", but this is v0.5.0`) {
		t.Fatalf("Update() error = %v, want marker version failure", err)
	}
}

func TestStableConfigVersionIsCanonical(t *testing.T) {
	for _, test := range []struct {
		version string
		want    bool
	}{
		{"v0.5.0", true},
		{"v10.20.30", true},
		{"v00.5.0", false},
		{"v0.05.0", false},
		{"v0.5.00", false},
		{"v+1.5.0", false},
		{"0.5.0", false},
		{"v0.5", false},
		{"v0.5.0-beta.1", false},
	} {
		if got := stableConfigVersion(test.version); got != test.want {
			t.Errorf("stableConfigVersion(%q) = %v, want %v", test.version, got, test.want)
		}
	}
}

func writeUpdateExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func newUpdateTestUpdater(paths Paths, out *bytes.Buffer, version string) Updater {
	updater := NewUpdater(paths, out, version)

	return updater
}

func testUpdatePlan(resolvedVersion string) UpdatePlan {
	return UpdatePlan{ResolvedVersion: resolvedVersion, Groups: []UpdateGroup{{Name: "Config", State: UpdateAvailable}}}
}

func environmentValue(environment []string, name string) string {
	prefix := name + "="
	for _, entry := range environment {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix)
		}
	}
	return ""
}
