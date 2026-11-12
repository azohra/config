package config

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	configReleaseBackend = "github:azohra/config"
	UpdateReexecEnv      = "AZOHRA_CONFIG_UPDATE_REEXEC_VERSION"
)

type Updater struct {
	Paths            Paths
	Version          string
	Config           string
	ReleaseCache     string
	ReleaseState     string
	ReleaseData      string
	ReleaseMiseProbe Runner

	Installed   Runner
	ReleaseMise LiveRunner

	InstallReleaseMise func() error

	Log               Logger
	CurrentExecutable func() (string, error)

	Reexec          func(string, []string, []string) error
	OperationEvents bool
	Native          *NativeMise
}

func NewUpdater(paths Paths, out io.Writer, version string) Updater {
	command := configCommandPath(paths)
	releaseRoot := configReleaseRoot(paths)
	releaseMise := releaseMisePath(paths)
	releaseEnvironment := []string{"MISE_AUTO_UPDATE=0", "MISE_NO_CONFIG=1", "NO_COLOR=1"}
	live := newLiveRunner(paths.Home)
	live.Environment = releaseEnvironment
	live.Unset = append(live.Unset, miseLocalEnvironment...)
	live.Executables = map[string]string{"mise": releaseMise}
	releaseInstaller := releaseMiseInstallerAt(releaseMise)
	live.Stdout, live.Stderr = out, out
	_, operationEvents := out.(*OperationEventWriter)
	return Updater{
		Paths:        paths,
		Version:      version,
		Config:       command,
		ReleaseCache: filepath.Join(releaseRoot, "cache"),
		ReleaseState: filepath.Join(releaseRoot, "state"),
		ReleaseData:  filepath.Join(releaseRoot, "data"),
		ReleaseMiseProbe: OSRunner{
			Dir:         paths.Home,
			Environment: releaseEnvironment,
			Unset:       miseLocalEnvironment,
			Executables: map[string]string{"mise": releaseMise},
		},
		Installed: OSRunner{Dir: paths.Home, Executables: map[string]string{
			"config": command,
		}},
		ReleaseMise:        live,
		InstallReleaseMise: releaseInstaller.Install,
		Log:                Logger{Out: out},
		CurrentExecutable:  os.Executable,
		Reexec:             syscall.Exec,
		OperationEvents:    operationEvents,
	}
}

func configReleaseRoot(paths Paths) string {
	return filepath.Join(filepath.Dir(paths.StateDir), "release")
}

func releaseMisePath(paths Paths) string {
	return filepath.Join(configReleaseRoot(paths), "mise")
}

// Apply executes the plan the caller displayed and records what each step
// did. Config release discovery is not repeated; a newer release is acquired
// by its exact version.
func (u Updater) Apply(plan UpdatePlan) error {
	if err := plan.validate(u.Version); err != nil {
		return err
	}
	if !plan.HasWork() {
		return nil
	}
	run := beginUpdateRun(u.Paths, plan.Scope)
	err := u.apply(plan, &run)
	if err != nil && !run.Failed() {
		// The update stopped before a step could fail.
		run.Error = err.Error()
	}
	u.record(&run)
	return err
}

func (u Updater) record(run *UpdateRun) {
	run.FinishedAt = time.Now()
	if err := run.save(u.Paths); err != nil {
		u.Log.Warn("This update was not recorded: " + err.Error())
	}
}

func (u Updater) apply(plan UpdatePlan, run *UpdateRun) error {
	release := plan.Groups[0]
	if !plan.Scope.includesConfig() || u.Version == "dev" || release.State == UpdateSkipped {
		return u.applyNative(plan, run)
	}
	if !stableConfigVersion(u.Version) {
		return fmt.Errorf("Config build version %q cannot update itself", u.Version)
	}
	if resumedVersion, resumed := os.LookupEnv(UpdateReexecEnv); resumed {
		if resumedVersion != u.Version {
			return fmt.Errorf("Config update resumed with version %q, but this is %s", resumedVersion, u.Version)
		}
		if err := u.requireCanonicalExecutable(); err != nil {
			return err
		}
		if err := os.Unsetenv(UpdateReexecEnv); err != nil {
			return err
		}
		return u.applyNative(plan, run)
	}
	u.Log.Section("Config")
	resolvedVersion := plan.ResolvedVersion
	if compareConfigVersions(resolvedVersion, u.Version) < 0 {
		return fmt.Errorf("refusing to replace Config %s with older release %s", u.Version, resolvedVersion)
	}
	if resolvedVersion == u.Version && u.requireCanonicalExecutable() == nil {
		u.Log.OK("Config " + u.Version + " is current")
		return u.applyNative(plan, run)
	}
	started := time.Now()
	step := StepResult{ID: configStepID, Name: "Config", Status: StepDone}
	for _, item := range release.Items {
		step.Items = append(step.Items, ItemResult{UpdateItem: item, Status: ItemUpdated})
	}
	installedVersion, unlock, err := u.acquireRelease(resolvedVersion)
	step.Duration = time.Since(started).Round(time.Millisecond)
	if err != nil {
		step.Status, step.Error = StepFailed, err.Error()
		for index := range step.Items {
			step.Items[index].Status = ItemFailed
		}
		run.Steps = append(run.Steps, step)
		return err
	}
	run.Steps = append(run.Steps, step)
	u.Log.OK("Config " + installedVersion + " installed")
	u.Log.Version(installedVersion)
	// The installed release continues this record after the handoff.
	u.record(run)
	overrides := []string{UpdateReexecEnv + "=" + installedVersion, UpdateRunEnv + "=" + run.name()}
	if u.OperationEvents {
		overrides = append(overrides, OperationEventsEnv+"=1")
	}
	environment := childEnvironment(overrides, nil)
	unlock()
	scope := UpdateConfig
	if plan.awaitsHandoff() {
		scope = UpdateAll
	}
	args := []string{u.Config, "update", scope.String(), "--yes"}
	if err := u.Reexec(u.Config, args, environment); err != nil {
		return fmt.Errorf("re-exec Config %s: %w", installedVersion, err)
	}
	return nil
}

// acquireRelease installs the resolved release at the canonical command under
// the checkout lock, and returns the lock's release for the handoff.
func (u Updater) acquireRelease(version string) (string, func(), error) {
	unlock, err := LockCheckout(u.Paths)
	if err != nil {
		return "", nil, err
	}
	installed, err := u.installAndVerify(version)
	if err != nil {
		unlock()
		return "", nil, err
	}
	return installed, unlock, nil
}

func (u Updater) installAndVerify(version string) (string, error) {
	if err := u.prepareReleaseMise(); err != nil {
		return "", err
	}
	if err := u.installRelease(version); err != nil {
		return "", fmt.Errorf("Config: %w", err)
	}
	installed, err := u.installedVersion()
	if err != nil {
		return "", fmt.Errorf("Config: %w", err)
	}
	if installed != version {
		return "", fmt.Errorf("installed Config is %s, want resolved release %s", installed, version)
	}
	return installed, nil
}

func (u Updater) nativeMise() NativeMise {
	if u.Native != nil {
		return *u.Native
	}
	return NewNativeMise(u.Paths, u.Log.Out)
}

// applyNative runs each planned native step and records it. A failed step does
// not stop the ones after it, except that linked files wait for a repository
// update that failed: their sources may be stale.
func (u Updater) applyNative(plan UpdatePlan, run *UpdateRun) error {
	if plan.Scope == UpdateConfig {
		return nil
	}
	_, err := LoadMachine(u.Paths)
	if err != nil {
		return err
	}
	if !machineUsesMise(u.Paths) {
		return nil
	}
	native := u.nativeMise()
	digest, err := native.configurationDigest(context.Background())
	if err != nil {
		return err
	}
	if digest != plan.Configuration {
		return errors.New("machine configuration changed; review the update plan again")
	}
	var failures []error
	repositoriesFailed := false
	for _, group := range plan.Groups {
		if nativeStepFor(group.ID).id == "" || group.State != UpdateAvailable && group.State != UpdatePending {
			continue
		}
		if group.ID == filesStepID && repositoriesFailed {
			run.Steps = append(run.Steps, StepResult{ID: group.ID, Name: group.Name, Status: StepSkipped, Error: "the repository update failed"})
			u.Log.Warn(group.Name + ": skipped because the repository update failed")
			continue
		}
		step := native.run(group, plan.Scope)
		run.Steps = append(run.Steps, step)
		if step.Status == StepFailed {
			failures = append(failures, fmt.Errorf("%s: %s", step.Name, step.Error))
			repositoriesFailed = repositoriesFailed || group.ID == repositoriesStepID
		}
	}
	return errors.Join(failures...)
}

func (u Updater) prepareReleaseMise() error {
	u.Log.Section("Config release transport")
	if err := ensureReleaseMise(u.ReleaseMiseProbe, u.InstallReleaseMise); err != nil {
		u.Log.Error(err.Error())
		return fmt.Errorf("Config release transport: %w", err)
	}
	u.Log.OK("verified release adapter ready")
	return nil
}

func (u Updater) installedVersion() (string, error) {
	if err := requireExecutableFile(u.Config); err != nil {
		return "", fmt.Errorf("installed Config command is unavailable at %s", u.Config)
	}
	result := run(u.Installed, "config", "--version")
	if result.Err != nil {
		return "", fmt.Errorf("read installed Config version: %w", result.Failure())
	}
	version, ok := configVersionOutput(result.Stdout)
	if !ok {
		return "", errors.New("installed Config version is unreadable")
	}
	return version, nil
}

func (u Updater) resolveReleaseContext(ctx context.Context) (string, error) {
	if !filepath.IsAbs(u.ReleaseCache) || !filepath.IsAbs(u.ReleaseState) || !filepath.IsAbs(u.ReleaseData) {
		return "", errors.New("Config release cache paths are invalid")
	}
	if _, err := u.releaseOutputContext(ctx, "mise", "--no-config", "cache", "clear"); err != nil {
		return "", fmt.Errorf("refresh Config release metadata: %w", err)
	}
	output, err := u.releaseOutputContext(ctx, "mise", "--no-config", "latest", configReleaseBackend)
	if err != nil {
		return "", fmt.Errorf("resolve latest Config release: %w", err)
	}
	fields := strings.Fields(output)
	if len(fields) != 1 {
		return "", errors.New("latest Config release version is unreadable")
	}
	version := "v" + fields[0]
	if !stableConfigVersion(version) {
		return "", errors.New("latest Config release version is unreadable")
	}
	return version, nil
}

func (u Updater) installRelease(version string) error {
	exactTool := configReleaseBackend + "@" + strings.TrimPrefix(version, "v")
	releaseRunner := u.releaseRunner()
	if err := releaseRunner.Command("mise", "--no-config", "install", exactTool); err != nil {
		return fmt.Errorf("install Config release %s: %w", version, err)
	}

	installation, err := u.releaseOutput("mise", "--no-config", "where", exactTool)
	if err != nil {
		return fmt.Errorf("locate Config release %s: %w", version, err)
	}
	installation = strings.TrimSpace(installation)
	if installation == "" || strings.ContainsAny(installation, "\r\n") || !filepath.IsAbs(installation) {
		return fmt.Errorf("Config release %s installation path is unreadable", version)
	}
	executable := filepath.Join(filepath.Clean(installation), "config")
	if err := requireExecutableFile(executable); err != nil {
		return fmt.Errorf("Config release %s executable is unavailable at %s", version, executable)
	}

	output, err := u.releaseOutput(executable, "--version")
	if err != nil {
		return fmt.Errorf("read Config release %s executable version: %w", version, err)
	}
	executableVersion, ok := configVersionOutput(output)
	if !ok {
		return fmt.Errorf("Config release %s executable version is unreadable", version)
	}
	if executableVersion != version {
		return fmt.Errorf("Config release executable is %s, want resolved release %s", executableVersion, version)
	}

	releaseRunner = u.releaseRunner()
	if err := releaseRunner.Command(executable, "install"); err != nil {
		return fmt.Errorf("install Config release %s command: %w", version, err)
	}
	// The canonical command is installed; the acquired copy has done its work.
	_ = os.RemoveAll(u.ReleaseData)
	return nil
}

func (u Updater) releaseOutput(name string, args ...string) (string, error) {
	return u.releaseOutputContext(context.Background(), name, args...)
}

func (u Updater) releaseOutputContext(ctx context.Context, name string, args ...string) (string, error) {
	var output, diagnostics strings.Builder
	releaseRunner := u.releaseRunner()
	releaseRunner.Stdout = &output
	releaseRunner.Stderr = &diagnostics
	releaseRunner.Stdin = nil
	if err := releaseRunner.CommandContext(ctx, name, args...); err != nil {
		if detail := strings.TrimSpace(diagnostics.String()); detail != "" {
			return "", fmt.Errorf("%s: %w", detail, err)
		}
		return "", err
	}
	return output.String(), nil
}

func (u Updater) releaseRunner() LiveRunner {
	runner := u.ReleaseMise
	runner.Environment = append(runner.Environment,
		"MISE_GITHUB_GITHUB_ATTESTATIONS=false",
		"MISE_GITHUB_SLSA=false",
		"MISE_MINIMUM_RELEASE_AGE=0s",
		"MISE_CACHE_DIR="+u.ReleaseCache,
		"MISE_STATE_DIR="+u.ReleaseState,
		// The acquired release stays out of the machine's own mise installs
		// and shims.
		"MISE_DATA_DIR="+u.ReleaseData,
		"MISE_USE_VERSIONS_HOST=0",
	)
	return runner
}

func configVersionOutput(output string) (string, bool) {
	fields := strings.Fields(output)
	if len(fields) != 2 || fields[0] != "config" || !stableConfigVersion(fields[1]) {
		return "", false
	}
	return fields[1], true
}

func (u Updater) requireCanonicalExecutable() error {
	if err := requireExecutableFile(u.Config); err != nil {
		return fmt.Errorf("resumed Config command is unavailable at %s", u.Config)
	}
	current, err := u.CurrentExecutable()
	if err != nil {
		return fmt.Errorf("locate resumed Config: %w", err)
	}
	currentInfo, err := os.Stat(current)
	if err != nil {
		return fmt.Errorf("inspect resumed Config: %w", err)
	}
	canonicalInfo, err := os.Stat(u.Config)
	if err != nil {
		return fmt.Errorf("inspect canonical Config: %w", err)
	}
	if !os.SameFile(currentInfo, canonicalInfo) {
		return fmt.Errorf("Config update resumed outside the canonical command at %s", u.Config)
	}
	return nil
}

func requireExecutableFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		return errors.New("not an executable regular file")
	}
	return nil
}

func stableConfigVersion(version string) bool {
	_, ok := configVersionNumbers(version)
	return ok
}

func compareConfigVersions(left, right string) int {
	leftNumbers, _ := configVersionNumbers(left)
	rightNumbers, _ := configVersionNumbers(right)
	for index := range leftNumbers {
		if leftNumbers[index] < rightNumbers[index] {
			return -1
		}
		if leftNumbers[index] > rightNumbers[index] {
			return 1
		}
	}
	return 0
}

func configVersionNumbers(version string) ([3]int, bool) {
	var numbers [3]int
	if !strings.HasPrefix(version, "v") {
		return numbers, false
	}
	parts := strings.Split(strings.TrimPrefix(version, "v"), ".")
	if len(parts) != len(numbers) {
		return numbers, false
	}
	for index, part := range parts {
		if part == "" || len(part) > 1 && part[0] == '0' {
			return numbers, false
		}
		for _, character := range part {
			if character < '0' || character > '9' {
				return numbers, false
			}
		}
		value, err := strconv.Atoi(part)
		if err != nil {
			return numbers, false
		}
		numbers[index] = value
	}
	return numbers, true
}
