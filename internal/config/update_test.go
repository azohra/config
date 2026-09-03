package config

import (
	"bytes"
	"errors"
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
	updater.ReleaseMise.Stdout, updater.ReleaseMise.Stderr = &output, &output
	updater.MachineMise.Stdout, updater.MachineMise.Stderr = &output, &output
	err := updater.Apply(testUpdatePlan(UpdateAll, ""))
	if err == nil || !strings.Contains(err.Error(), "Tools") {
		t.Fatalf("Update() error = %v, want Tools failure", err)
	}

	commands, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	selection := miseConfigDir(paths)
	want := strings.Join([]string{
		"--version||||0|1|",
		"upgrade --yes|" + selection + "|" + paths.Home + "|" + paths.Home + "|0||",
		"bootstrap packages upgrade --yes|" + selection + "|" + paths.Home + "|" + paths.Home + "|0||",
		"bootstrap repos update --yes --skip-dirty|" + selection + "|" + paths.Home + "|" + paths.Home + "|0||",
		"",
	}, "\n")
	if string(commands) != want {
		t.Fatalf("commands =\n%s\nwant =\n%s", commands, want)
	}
	for _, message := range []string{"standalone mise " + testedMiseVersion + " ready", "declared packages updated", "clean repositories updated"} {
		if !strings.Contains(output.String(), message) {
			t.Fatalf("output missing %q:\n%s", message, output.String())
		}
	}
}

func TestUpdaterRunsOnlyTheSelectedMachineUpdateScope(t *testing.T) {
	for _, test := range []struct {
		name  string
		scope UpdateScope
		want  []string
	}{
		{
			name:  "software",
			scope: UpdateSoftware,
			want: []string{
				"--version",
				"upgrade --yes",
				"bootstrap packages upgrade --yes",
			},
		},
		{
			name:  "repositories",
			scope: UpdateRepositories,
			want: []string{
				"--version",
				"bootstrap repos update --yes --skip-dirty",
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			paths := testPaths(t)
			logPath := filepath.Join(t.TempDir(), "commands")
			t.Setenv("UPDATE_TEST_LOG", logPath)
			writeUpdateExecutable(t, misePath(paths), `#!/bin/sh
printf '%s\n' "$*" >> "$UPDATE_TEST_LOG"
if [ "$1" = --version ]; then
  printf '%s\n' '`+testedMiseVersion+`'
fi
`)

			updater := newUpdateTestUpdater(paths, &bytes.Buffer{}, "dev")
			if err := updater.Apply(testUpdatePlan(test.scope, "")); err != nil {
				t.Fatal(err)
			}
			commands, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			got := strings.Split(strings.TrimSpace(string(commands)), "\n")
			if !slices.Equal(got, test.want) {
				t.Fatalf("commands =\n%s\nwant =\n%s", strings.Join(got, "\n"), strings.Join(test.want, "\n"))
			}
		})
	}
}

func TestUpdateScopePreservesTheSelectionAcrossReexec(t *testing.T) {
	for _, test := range []struct {
		scope UpdateScope
		want  []string
	}{
		{UpdateAll, nil},
		{UpdateSoftware, []string{"software"}},
		{UpdateRepositories, []string{"repositories"}},
	} {
		if got := test.scope.arguments(); !slices.Equal(got, test.want) {
			t.Errorf("scope %d arguments = %v, want %v", test.scope, got, test.want)
		}
	}
}

func TestUpdaterRejectsAnUnknownScopeBeforeMutation(t *testing.T) {
	paths := testPaths(t)
	logPath := filepath.Join(t.TempDir(), "commands")
	t.Setenv("UPDATE_TEST_LOG", logPath)
	writeUpdateExecutable(t, misePath(paths), "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$UPDATE_TEST_LOG\"\n")

	err := newUpdateTestUpdater(paths, &bytes.Buffer{}, "dev").Apply(testUpdatePlan(UpdateScope(99), ""))
	if err == nil || !strings.Contains(err.Error(), "invalid update scope") {
		t.Fatalf("Update() error = %v, want invalid scope", err)
	}
	if commands, readErr := os.ReadFile(logPath); !os.IsNotExist(readErr) || len(commands) != 0 {
		t.Fatalf("invalid scope ran commands %q: %v", commands, readErr)
	}
}

func TestUpdaterDoesNotApplyAPlanWithoutWork(t *testing.T) {
	updater := Updater{
		Version: "dev",
		LoadMachine: func() (Machine, error) {
			t.Fatal("an empty update plan reached machine mutation")
			return Machine{}, nil
		},
	}
	plan := UpdatePlan{
		Scope:  UpdateSoftware,
		Groups: []UpdateGroup{{Name: "Config", Scope: UpdateAll, State: UpdateSkipped}},
	}
	if err := updater.Apply(plan); err != nil {
		t.Fatal(err)
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
	unsupported := unsupportedMiseVersions()[1]
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$UPDATE_TEST_LOG"
if [ "$1" = --version ]; then
  printf '%s\n' '` + unsupported + `'
fi
`
	if err := os.WriteFile(canonical, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	updater := newUpdateTestUpdater(paths, &output, "dev")
	updater.InstallMachineMise = nil
	updater.ReleaseMise.Stdout, updater.ReleaseMise.Stderr = &output, &output
	updater.MachineMise.Stdout, updater.MachineMise.Stderr = &output, &output
	err := updater.Apply(testUpdatePlan(UpdateAll, ""))
	if err == nil || !strings.Contains(err.Error(), unsupported+" is unsupported") {
		t.Fatalf("Update() error = %v, want unsupported mise version", err)
	}
	commands, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	want := "--version\n"
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
	updater.InstallMachineMise = func() error { return errors.New("install failed") }
	updater.ReleaseMise.Stdout, updater.ReleaseMise.Stderr = &output, &output
	updater.MachineMise.Stdout, updater.MachineMise.Stderr = &output, &output
	err := updater.Apply(testUpdatePlan(UpdateAll, ""))
	if err == nil || !strings.Contains(err.Error(), "mise") {
		t.Fatalf("Update() error = %v, want mise failure", err)
	}
	commands, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	want := "--version\n"
	if string(commands) != want {
		t.Fatalf("commands = %q, want %q", commands, want)
	}
}

func TestUpdaterRequiresCanonicalMise(t *testing.T) {
	paths := testPaths(t)
	updater := newUpdateTestUpdater(paths, &bytes.Buffer{}, "dev")
	updater.InstallMachineMise = nil
	err := updater.Apply(testUpdatePlan(UpdateAll, ""))
	if err == nil || !strings.Contains(err.Error(), "mise is unavailable") {
		t.Fatalf("Update() error = %v, want unavailable Mise", err)
	}
}

func TestUpdaterInstallsCanonicalMiseWhenMissing(t *testing.T) {
	paths := testPaths(t)
	installed := 0
	updater := newUpdateTestUpdater(paths, &bytes.Buffer{}, "dev")
	updater.InstallMachineMise = func() error {
		installed++
		writeUpdateExecutable(t, misePath(paths), `#!/bin/sh
if [ "$1" = --version ]; then
  printf '%s\n' '`+testedMiseVersion+`'
fi
`)
		return nil
	}
	if err := updater.Apply(testUpdatePlan(UpdateSoftware, "")); err != nil {
		t.Fatal(err)
	}
	if installed != 1 {
		t.Fatalf("Mise installer ran %d times, want once", installed)
	}
}

func TestUpdaterLeavesMachineMiseUntouchedWhenUndeclared(t *testing.T) {
	paths := testPaths(t)
	updater := newUpdateTestUpdater(paths, &bytes.Buffer{}, "dev")
	updater.LoadMachine = func() (Machine, error) { return Machine{}, nil }
	updater.InstallMachineMise = func() error {
		t.Fatal("an undeclared Mise resource was installed")
		return nil
	}
	if err := updater.Apply(testUpdatePlan(UpdateAll, "")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(misePath(paths)); !os.IsNotExist(err) {
		t.Fatalf("undeclared Mise resource changed: %v", err)
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
	updater.OperationEvents = true
	updater.LoadMachine = func() (Machine, error) {
		t.Fatal("fresh released update parsed the machine before re-exec")
		return Machine{}, nil
	}
	updater.ReleaseMise.Stdout, updater.ReleaseMise.Stderr = &output, &output
	updater.MachineMise.Stdout, updater.MachineMise.Stderr = &output, &output
	var reexecPath string
	var reexecArgs, reexecEnvironment []string
	updater.Reexec = func(path string, args, environment []string) error {
		reexecPath = path
		reexecArgs = slices.Clone(args)
		reexecEnvironment = slices.Clone(environment)
		return nil
	}
	if err := updater.Apply(testUpdatePlan(UpdateSoftware, "v0.5.0")); err != nil {
		t.Fatal(err)
	}

	commands, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"--version||0|1|||||",
		"--no-config install github:azohra/config@0.5.0||0|1|true|0s|" + releaseCache + "|" + releaseState + "|0",
		"--no-config where github:azohra/config@0.5.0||0|1|true|0s|" + releaseCache + "|" + releaseState + "|0",
		"candidate --version||0|1|true|0s|" + releaseCache + "|" + releaseState + "|0",
		"candidate install||0|1|true|0s|" + releaseCache + "|" + releaseState + "|0",
		"",
	}, "\n")
	if string(commands) != want {
		t.Fatalf("commands =\n%s\nwant =\n%s", commands, want)
	}
	if _, err := os.Lstat(misePath(paths)); !os.IsNotExist(err) {
		t.Fatalf("release acquisition changed the machine Mise resource: %v", err)
	}
	if reexecPath != canonicalConfig || !slices.Equal(reexecArgs, []string{canonicalConfig, "update", "software", "--yes"}) {
		t.Fatalf("re-exec = %q %q", reexecPath, reexecArgs)
	}
	if environmentValue(reexecEnvironment, updateReexecEnv) != "v0.5.0" {
		t.Fatalf("re-exec environment does not bind %s to the installed release", updateReexecEnv)
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
if [ "$1" = --version ]; then printf '`+testedMiseVersion+`\n'; fi
if [ "$2" = latest ]; then printf '0.5.0\n'; fi
`)
	var output bytes.Buffer
	updater := newUpdateTestUpdater(paths, &output, "v0.5.0")
	updater.LoadMachine = func() (Machine, error) { return Machine{}, nil }
	updater.CurrentExecutable = func() (string, error) { return canonical, nil }
	updater.Reexec = func(string, []string, []string) error {
		t.Fatal("current canonical Config attempted to re-exec")
		return nil
	}
	if err := updater.Apply(testUpdatePlan(UpdateSoftware, "v0.5.0")); err != nil {
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
  printf '%s\n' '`+testedMiseVersion+`'
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
	plan, err := updater.Plan(UpdateAll)
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
	writeUpdateExecutable(t, configCommandPath(paths), "#!/bin/sh\nprintf 'config dev\\n'\n")

	var output bytes.Buffer
	updater := newUpdateTestUpdater(paths, &output, "v0.4.0")
	updater.ReleaseMise.Stdout, updater.ReleaseMise.Stderr = &output, &output
	updater.Reexec = func(string, []string, []string) error {
		t.Fatal("unverified build attempted to re-exec")
		return nil
	}
	err := updater.Apply(testUpdatePlan(UpdateAll, "v0.4.0"))
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
	writeUpdateExecutable(t, configCommandPath(paths), "#!/bin/sh\nprintf 'config v0.4.0\\n'\n")

	var output bytes.Buffer
	updater := newUpdateTestUpdater(paths, &output, "v0.4.0")
	updater.ReleaseMise.Stdout, updater.ReleaseMise.Stderr = &output, &output
	updater.Reexec = func(string, []string, []string) error {
		t.Fatal("mismatched installed release attempted to re-exec")
		return nil
	}
	err := updater.Apply(testUpdatePlan(UpdateAll, "v0.5.0"))
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
	updater.ReleaseMise.Stdout, updater.ReleaseMise.Stderr = &output, &output
	updater.Reexec = func(string, []string, []string) error {
		t.Fatal("mismatched release executable attempted to re-exec")
		return nil
	}
	err := updater.Apply(testUpdatePlan(UpdateAll, "v0.5.0"))
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
  printf '%s\n' '`+testedMiseVersion+`'
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
	err := updater.Apply(testUpdatePlan(UpdateAll, "v0.3.0"))
	if err == nil || !strings.Contains(err.Error(), "older release v0.3.0") {
		t.Fatalf("Update() error = %v, want downgrade refusal", err)
	}
	commands, readErr := os.ReadFile(logPath)
	if !os.IsNotExist(readErr) || len(commands) != 0 {
		t.Fatalf("downgrade plan ran commands %q: %v", commands, readErr)
	}
}

func TestReexecutedUpdaterRunsMachineUpdatesWithoutRecursion(t *testing.T) {
	paths := testPaths(t)
	canonicalConfig := configCommandPath(paths)
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
	updater.ReleaseMise.Stdout, updater.ReleaseMise.Stderr = &output, &output
	updater.MachineMise.Stdout, updater.MachineMise.Stderr = &output, &output
	updater.CurrentExecutable = func() (string, error) { return canonicalConfig, nil }
	updater.Reexec = func(string, []string, []string) error {
		t.Fatal("resumed update attempted to re-exec")
		return nil
	}
	if err := updater.Apply(testUpdatePlan(UpdateAll, "v0.5.0")); err != nil {
		t.Fatal(err)
	}
	commands, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
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
	writeUpdateExecutable(t, configCommandPath(paths), "#!/bin/sh\nexit 0\n")
	other := filepath.Join(t.TempDir(), "config")
	writeUpdateExecutable(t, other, "#!/bin/sh\nexit 0\n")

	updater := newUpdateTestUpdater(paths, &bytes.Buffer{}, "v0.5.0")
	updater.CurrentExecutable = func() (string, error) { return other, nil }
	err := updater.Apply(testUpdatePlan(UpdateAll, "v0.5.0"))
	if err == nil || !strings.Contains(err.Error(), "outside the canonical command") {
		t.Fatalf("Update() error = %v, want canonical command failure", err)
	}
}

func TestReexecutedUpdaterRequiresItsVersionBoundMarker(t *testing.T) {
	paths := testPaths(t)
	t.Setenv(updateReexecEnv, "v0.4.0")
	writeUpdateExecutable(t, misePath(paths), "#!/bin/sh\nexit 0\n")
	writeUpdateExecutable(t, configCommandPath(paths), "#!/bin/sh\nexit 0\n")

	updater := newUpdateTestUpdater(paths, &bytes.Buffer{}, "v0.5.0")
	err := updater.Apply(testUpdatePlan(UpdateAll, "v0.5.0"))
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
	err := updater.Apply(testUpdatePlan(UpdateAll, ""))
	if err == nil {
		t.Fatal("development update accepted a missing machine document")
	}
	if commands, readErr := os.ReadFile(logPath); !os.IsNotExist(readErr) || len(commands) != 0 {
		t.Fatalf("invalid machine ran commands %q: %v", commands, readErr)
	}
}

func TestReexecutedUpdaterValidatesTheMachineBeforeMutation(t *testing.T) {
	paths := testPaths(t)
	canonicalConfig := configCommandPath(paths)
	logPath := filepath.Join(t.TempDir(), "commands")
	t.Setenv("UPDATE_TEST_LOG", logPath)
	t.Setenv(updateReexecEnv, "v0.5.0")
	writeUpdateExecutable(t, misePath(paths), "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$UPDATE_TEST_LOG\"\n")
	writeUpdateExecutable(t, canonicalConfig, "#!/bin/sh\nexit 0\n")

	updater := NewUpdater(paths, &bytes.Buffer{}, "v0.5.0")
	updater.CurrentExecutable = func() (string, error) { return canonicalConfig, nil }
	err := updater.Apply(testUpdatePlan(UpdateAll, "v0.5.0"))
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
	updater.LoadMachine = func() (Machine, error) { return Machine{Mise: true}, nil }
	return updater
}

func testUpdatePlan(scope UpdateScope, resolvedVersion string) UpdatePlan {
	state := UpdateCurrent
	if resolvedVersion == "" {
		state = UpdateSkipped
	}
	return UpdatePlan{
		Scope: scope, ResolvedVersion: resolvedVersion,
		Groups: []UpdateGroup{
			{Name: "Config", Scope: UpdateAll, State: state},
			{Name: "Machine update", Scope: scope, State: UpdatePending},
		},
	}
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
