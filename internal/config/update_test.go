package config

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestUpdaterDevelopmentBuildUsesCanonicalMiseAndContinuesIndependentSteps(t *testing.T) {
	paths := testPaths(t)
	canonical := misePath(paths)
	if err := os.MkdirAll(filepath.Dir(canonical), 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "commands")
	t.Setenv("UPDATE_TEST_LOG", logPath)
	script := `#!/bin/sh
	printf '%s|%s|%s|%s|%s|%s|%s\n' "$*" "$MISE_CONFIG_DIR" "$MISE_GLOBAL_CONFIG_ROOT" "$MISE_CEILING_PATHS" "$MISE_AUTO_UPDATE" "$MISE_NO_CONFIG" "$MISE_GITHUB_GITHUB_ATTESTATIONS" >> "$UPDATE_TEST_LOG"
if [ "$1" = --version ]; then
  printf '%s\n' '` + testedMiseVersion + `'
  exit 0
fi
if [ "$1" = upgrade ]; then
  exit 23
fi
`
	if err := os.WriteFile(canonical, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	updater := newUpdateTestUpdater(paths, &output, "dev")
	updater.Substrate.Stdout, updater.Substrate.Stderr = &output, &output
	updater.Machine.Stdout, updater.Machine.Stderr = &output, &output
	err := updater.Update()
	if err == nil || !strings.Contains(err.Error(), "Tools") {
		t.Fatalf("Update() error = %v, want Tools failure", err)
	}

	commands, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	selection := filepath.Join(paths.Root, "mise")
	want := strings.Join([]string{
		"--no-config self-update " + testedMiseVersion + " --yes --no-plugins||||0|1|",
		"--version||||0|1|",
		"upgrade --yes|" + selection + "|" + paths.Root + "|" + paths.Root + "|0||",
		"bootstrap packages upgrade --yes|" + selection + "|" + paths.Root + "|" + paths.Root + "|0||",
		"bootstrap repos update --yes --skip-dirty|" + selection + "|" + paths.Root + "|" + paths.Root + "|0||",
		"",
	}, "\n")
	if string(commands) != want {
		t.Fatalf("commands =\n%s\nwant =\n%s", commands, want)
	}
	for _, message := range []string{"standalone mise set to " + testedMiseVersion, "declared packages updated", "clean repositories updated"} {
		if !strings.Contains(output.String(), message) {
			t.Fatalf("output missing %q:\n%s", message, output.String())
		}
	}
}

func TestUpdaterVerifiesMiseBeforeFurtherMutations(t *testing.T) {
	paths := testPaths(t)
	canonical := misePath(paths)
	if err := os.MkdirAll(filepath.Dir(canonical), 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "commands")
	t.Setenv("UPDATE_TEST_LOG", logPath)
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$UPDATE_TEST_LOG"
if [ "$1" = --version ]; then
  printf '%s\n' '2026.8.15'
fi
`
	if err := os.WriteFile(canonical, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	updater := newUpdateTestUpdater(paths, &output, "dev")
	updater.Substrate.Stdout, updater.Substrate.Stderr = &output, &output
	updater.Machine.Stdout, updater.Machine.Stderr = &output, &output
	err := updater.Update()
	if err == nil || !strings.Contains(err.Error(), "2026.8.15 is unsupported") {
		t.Fatalf("Update() error = %v, want unsupported mise version", err)
	}
	commands, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	want := "--no-config self-update " + testedMiseVersion + " --yes --no-plugins\n--version\n"
	if string(commands) != want {
		t.Fatalf("commands = %q, want %q", commands, want)
	}
}

func TestUpdaterStopsWhenMiseCompatibilityUpdateFails(t *testing.T) {
	paths := testPaths(t)
	canonical := misePath(paths)
	if err := os.MkdirAll(filepath.Dir(canonical), 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "commands")
	t.Setenv("UPDATE_TEST_LOG", logPath)
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$UPDATE_TEST_LOG"
exit 23
`
	if err := os.WriteFile(canonical, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	updater := newUpdateTestUpdater(paths, &output, "dev")
	updater.Substrate.Stdout, updater.Substrate.Stderr = &output, &output
	updater.Machine.Stdout, updater.Machine.Stderr = &output, &output
	err := updater.Update()
	if err == nil || !strings.Contains(err.Error(), "mise") {
		t.Fatalf("Update() error = %v, want mise failure", err)
	}
	commands, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	want := "--no-config self-update " + testedMiseVersion + " --yes --no-plugins\n"
	if string(commands) != want {
		t.Fatalf("commands = %q, want %q", commands, want)
	}
}

func TestUpdaterRequiresCanonicalMise(t *testing.T) {
	paths := testPaths(t)
	err := newUpdateTestUpdater(paths, &bytes.Buffer{}, "dev").Update()
	if err == nil || !strings.Contains(err.Error(), misePath(paths)) {
		t.Fatalf("Update() error = %v, want the canonical path", err)
	}
}

func TestReleasedUpdaterInstallsAndReexecutesTheVerifiedPermanentRelease(t *testing.T) {
	paths := testPaths(t)
	canonicalMise := misePath(paths)
	canonicalConfig := ConfigCommandPath(paths)
	releaseDirectory := t.TempDir()
	releaseConfig := filepath.Join(releaseDirectory, "config")
	releaseCache := filepath.Join(filepath.Dir(paths.StateDir), "release-mise")
	releaseState := filepath.Join(filepath.Dir(paths.StateDir), "release-mise-state")
	logPath := filepath.Join(t.TempDir(), "commands")
	t.Setenv("UPDATE_TEST_LOG", logPath)
	t.Setenv("UPDATE_TEST_RELEASE_DIR", releaseDirectory)
	t.Setenv("PATH", filepath.Dir(canonicalConfig)+string(os.PathListSeparator)+os.Getenv("PATH"))
	writeUpdateExecutable(t, canonicalMise, `#!/bin/sh
printf '%s|%s|%s|%s|%s|%s|%s|%s|%s\n' "$*" "$MISE_CONFIG_DIR" "$MISE_AUTO_UPDATE" "$MISE_NO_CONFIG" "$MISE_GITHUB_GITHUB_ATTESTATIONS" "$MISE_MINIMUM_RELEASE_AGE" "$MISE_CACHE_DIR" "$MISE_STATE_DIR" "$MISE_USE_VERSIONS_HOST" >> "$UPDATE_TEST_LOG"
if [ "$1" = --version ]; then
  printf '%s\n' '`+testedMiseVersion+`'
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
	updater.ValidateMachine = func() error {
		t.Fatal("fresh released update parsed the machine before re-exec")
		return nil
	}
	updater.Substrate.Stdout, updater.Substrate.Stderr = &output, &output
	updater.Machine.Stdout, updater.Machine.Stderr = &output, &output
	var reexecPath string
	var reexecArgs, reexecEnvironment []string
	updater.Reexec = func(path string, args, environment []string) error {
		reexecPath = path
		reexecArgs = slices.Clone(args)
		reexecEnvironment = slices.Clone(environment)
		return nil
	}
	if err := updater.Update(); err != nil {
		t.Fatal(err)
	}

	commands, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"--no-config self-update " + testedMiseVersion + " --yes --no-plugins||0|1|||||",
		"--version||0|1|||||",
		"--no-config cache clear||0|1|true|0s|" + releaseCache + "|" + releaseState + "|0",
		"--no-config latest " + configReleaseBackend + "||0|1|true|0s|" + releaseCache + "|" + releaseState + "|0",
		"--no-config install github:azohra/config@0.5.0||0|1|true|0s|" + releaseCache + "|" + releaseState + "|0",
		"--no-config where github:azohra/config@0.5.0||0|1|true|0s|" + releaseCache + "|" + releaseState + "|0",
		"candidate --version||0|1|true|0s|" + releaseCache + "|" + releaseState + "|0",
		"candidate install||0|1|true|0s|" + releaseCache + "|" + releaseState + "|0",
		"",
	}, "\n")
	if string(commands) != want {
		t.Fatalf("commands =\n%s\nwant =\n%s", commands, want)
	}
	if reexecPath != canonicalConfig || !slices.Equal(reexecArgs, []string{canonicalConfig, "update"}) {
		t.Fatalf("re-exec = %q %q", reexecPath, reexecArgs)
	}
	if environmentValue(reexecEnvironment, updateReexecEnv) != "v0.5.0" {
		t.Fatalf("re-exec environment does not bind %s to the installed release", updateReexecEnv)
	}
	if !strings.Contains(output.String(), "Config v0.5.0 installed") {
		t.Fatalf("output missing installed release:\n%s", output.String())
	}
}

func TestReleasedUpdaterStopsWhenReleaseAcquisitionFails(t *testing.T) {
	paths := testPaths(t)
	logPath := filepath.Join(t.TempDir(), "commands")
	t.Setenv("UPDATE_TEST_LOG", logPath)
	writeUpdateExecutable(t, misePath(paths), `#!/bin/sh
printf '%s\n' "$*" >> "$UPDATE_TEST_LOG"
if [ "$1" = --version ]; then
  printf '%s\n' '`+testedMiseVersion+`'
fi
if [ "$2" = latest ]; then
  exit 23
fi
`)

	var output bytes.Buffer
	updater := newUpdateTestUpdater(paths, &output, "v0.4.0")
	updater.Substrate.Stdout, updater.Substrate.Stderr = &output, &output
	updater.Reexec = func(string, []string, []string) error {
		t.Fatal("failed acquisition attempted to re-exec")
		return nil
	}
	err := updater.Update()
	if err == nil || !strings.Contains(err.Error(), "Config") {
		t.Fatalf("Update() error = %v, want release acquisition failure", err)
	}
	commands, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	want := "--no-config self-update " + testedMiseVersion + " --yes --no-plugins\n--version\n--no-config cache clear\n--no-config latest " + configReleaseBackend + "\n"
	if string(commands) != want {
		t.Fatalf("commands = %q, want %q", commands, want)
	}
}

func TestReleasedUpdaterRefusesAnUnverifiedInstalledBuild(t *testing.T) {
	paths := testPaths(t)
	releaseDirectory := t.TempDir()
	t.Setenv("UPDATE_TEST_RELEASE_DIR", releaseDirectory)
	writeUpdateExecutable(t, misePath(paths), `#!/bin/sh
if [ "$1" = --version ]; then
  printf '%s\n' '`+testedMiseVersion+`'
fi
if [ "$2" = latest ]; then
  printf '0.4.0\n'
fi
if [ "$2" = where ]; then
  printf '%s\n' "$UPDATE_TEST_RELEASE_DIR"
fi
`)
	writeUpdateExecutable(t, filepath.Join(releaseDirectory, "config"), "#!/bin/sh\nif [ \"$1\" = --version ]; then\n  printf 'config v0.4.0\\n'\nfi\n")
	writeUpdateExecutable(t, ConfigCommandPath(paths), "#!/bin/sh\nprintf 'config dev\\n'\n")

	var output bytes.Buffer
	updater := newUpdateTestUpdater(paths, &output, "v0.4.0")
	updater.Substrate.Stdout, updater.Substrate.Stderr = &output, &output
	updater.Reexec = func(string, []string, []string) error {
		t.Fatal("unverified build attempted to re-exec")
		return nil
	}
	err := updater.Update()
	if err == nil || !strings.Contains(err.Error(), "installed Config version is unreadable") {
		t.Fatalf("Update() error = %v, want installed version failure", err)
	}
}

func TestReleasedUpdaterVerifiesTheExactInstalledRelease(t *testing.T) {
	paths := testPaths(t)
	releaseDirectory := t.TempDir()
	t.Setenv("UPDATE_TEST_RELEASE_DIR", releaseDirectory)
	writeUpdateExecutable(t, misePath(paths), `#!/bin/sh
if [ "$1" = --version ]; then
  printf '%s\n' '`+testedMiseVersion+`'
fi
if [ "$2" = latest ]; then
  printf '0.5.0\n'
fi
if [ "$2" = where ]; then
  printf '%s\n' "$UPDATE_TEST_RELEASE_DIR"
fi
`)
	writeUpdateExecutable(t, filepath.Join(releaseDirectory, "config"), "#!/bin/sh\nif [ \"$1\" = --version ]; then\n  printf 'config v0.5.0\\n'\nfi\n")
	writeUpdateExecutable(t, ConfigCommandPath(paths), "#!/bin/sh\nprintf 'config v0.4.0\\n'\n")

	var output bytes.Buffer
	updater := newUpdateTestUpdater(paths, &output, "v0.4.0")
	updater.Substrate.Stdout, updater.Substrate.Stderr = &output, &output
	updater.Reexec = func(string, []string, []string) error {
		t.Fatal("mismatched installed release attempted to re-exec")
		return nil
	}
	err := updater.Update()
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
	writeUpdateExecutable(t, misePath(paths), `#!/bin/sh
if [ "$1" = --version ]; then
  printf '%s\n' '`+testedMiseVersion+`'
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
	updater.Substrate.Stdout, updater.Substrate.Stderr = &output, &output
	updater.Reexec = func(string, []string, []string) error {
		t.Fatal("mismatched release executable attempted to re-exec")
		return nil
	}
	err := updater.Update()
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
	writeUpdateExecutable(t, misePath(paths), `#!/bin/sh
printf '%s\n' "$*" >> "$UPDATE_TEST_LOG"
if [ "$1" = --version ]; then
  printf '%s\n' '`+testedMiseVersion+`'
fi
if [ "$2" = latest ]; then
  printf '0.3.0\n'
fi
`)

	var output bytes.Buffer
	updater := newUpdateTestUpdater(paths, &output, "v0.4.0")
	updater.Substrate.Stdout, updater.Substrate.Stderr = &output, &output
	updater.Reexec = func(string, []string, []string) error {
		t.Fatal("downgrade attempted to re-exec")
		return nil
	}
	err := updater.Update()
	if err == nil || !strings.Contains(err.Error(), "older release v0.3.0") {
		t.Fatalf("Update() error = %v, want downgrade refusal", err)
	}
	commands, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	want := "--no-config self-update " + testedMiseVersion + " --yes --no-plugins\n--version\n--no-config cache clear\n--no-config latest " + configReleaseBackend + "\n"
	if string(commands) != want || strings.Contains(string(commands), "config install") {
		t.Fatalf("downgrade commands = %q, want no install after resolution", commands)
	}
}

func TestReexecutedUpdaterRunsMachineUpdatesWithoutRecursion(t *testing.T) {
	paths := testPaths(t)
	canonicalConfig := ConfigCommandPath(paths)
	logPath := filepath.Join(t.TempDir(), "commands")
	t.Setenv("UPDATE_TEST_LOG", logPath)
	t.Setenv(updateReexecEnv, "v0.5.0")
	writeUpdateExecutable(t, misePath(paths), `#!/bin/sh
printf '%s\n' "$*" >> "$UPDATE_TEST_LOG"
if [ "$1" = --version ]; then
  printf '%s\n' '`+testedMiseVersion+`'
fi
`)
	writeUpdateExecutable(t, canonicalConfig, "#!/bin/sh\nprintf 'config v0.5.0\\n'\n")

	var output bytes.Buffer
	updater := newUpdateTestUpdater(paths, &output, "v0.5.0")
	updater.Substrate.Stdout, updater.Substrate.Stderr = &output, &output
	updater.Machine.Stdout, updater.Machine.Stderr = &output, &output
	updater.CurrentExecutable = func() (string, error) { return canonicalConfig, nil }
	updater.Reexec = func(string, []string, []string) error {
		t.Fatal("resumed update attempted to re-exec")
		return nil
	}
	if err := updater.Update(); err != nil {
		t.Fatal(err)
	}
	commands, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"--no-config self-update " + testedMiseVersion + " --yes --no-plugins",
		"--version",
		"upgrade --yes",
		"bootstrap packages upgrade --yes",
		"bootstrap repos update --yes --skip-dirty",
		"",
	}, "\n")
	if string(commands) != want {
		t.Fatalf("commands =\n%s\nwant =\n%s", commands, want)
	}
	if _, exists := os.LookupEnv(updateReexecEnv); exists {
		t.Fatalf("%s survived the re-exec boundary", updateReexecEnv)
	}
}

func TestReexecutedUpdaterRequiresTheCanonicalCommand(t *testing.T) {
	paths := testPaths(t)
	t.Setenv(updateReexecEnv, "v0.5.0")
	writeUpdateExecutable(t, misePath(paths), "#!/bin/sh\nexit 0\n")
	writeUpdateExecutable(t, ConfigCommandPath(paths), "#!/bin/sh\nexit 0\n")
	other := filepath.Join(t.TempDir(), "config")
	writeUpdateExecutable(t, other, "#!/bin/sh\nexit 0\n")

	updater := newUpdateTestUpdater(paths, &bytes.Buffer{}, "v0.5.0")
	updater.CurrentExecutable = func() (string, error) { return other, nil }
	err := updater.Update()
	if err == nil || !strings.Contains(err.Error(), "outside the canonical command") {
		t.Fatalf("Update() error = %v, want canonical command failure", err)
	}
}

func TestReexecutedUpdaterRequiresItsVersionBoundMarker(t *testing.T) {
	paths := testPaths(t)
	t.Setenv(updateReexecEnv, "v0.4.0")
	writeUpdateExecutable(t, misePath(paths), "#!/bin/sh\nexit 0\n")
	writeUpdateExecutable(t, ConfigCommandPath(paths), "#!/bin/sh\nexit 0\n")

	updater := newUpdateTestUpdater(paths, &bytes.Buffer{}, "v0.5.0")
	err := updater.Update()
	if err == nil || !strings.Contains(err.Error(), `resumed with version "v0.4.0", but this is v0.5.0`) {
		t.Fatalf("Update() error = %v, want marker version failure", err)
	}
}

func TestDevelopmentUpdaterValidatesTheMachineBeforeMutation(t *testing.T) {
	paths := testPaths(t)
	logPath := filepath.Join(t.TempDir(), "commands")
	t.Setenv("UPDATE_TEST_LOG", logPath)
	writeUpdateExecutable(t, misePath(paths), "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$UPDATE_TEST_LOG\"\n")

	updater := NewUpdater(paths, &bytes.Buffer{}, "dev")
	err := updater.Update()
	if err == nil {
		t.Fatal("development update accepted a missing machine document")
	}
	if commands, readErr := os.ReadFile(logPath); !os.IsNotExist(readErr) || len(commands) != 0 {
		t.Fatalf("invalid machine ran commands %q: %v", commands, readErr)
	}
}

func TestReexecutedUpdaterValidatesTheMachineBeforeMutation(t *testing.T) {
	paths := testPaths(t)
	canonicalConfig := ConfigCommandPath(paths)
	logPath := filepath.Join(t.TempDir(), "commands")
	t.Setenv("UPDATE_TEST_LOG", logPath)
	t.Setenv(updateReexecEnv, "v0.5.0")
	writeUpdateExecutable(t, misePath(paths), "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$UPDATE_TEST_LOG\"\n")
	writeUpdateExecutable(t, canonicalConfig, "#!/bin/sh\nexit 0\n")

	updater := NewUpdater(paths, &bytes.Buffer{}, "v0.5.0")
	updater.CurrentExecutable = func() (string, error) { return canonicalConfig, nil }
	err := updater.Update()
	if err == nil {
		t.Fatal("resumed update accepted a missing machine document")
	}
	if commands, readErr := os.ReadFile(logPath); !os.IsNotExist(readErr) || len(commands) != 0 {
		t.Fatalf("invalid machine ran commands %q: %v", commands, readErr)
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
	updater.ValidateMachine = func() error { return nil }
	return updater
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
