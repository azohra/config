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
)

const (
	configReleaseBackend = "github:azohra/config"
	updateReexecEnv      = "AZOHRA_CONFIG_UPDATE_REEXEC_VERSION"
)

// UpdateScope selects the machine resources handled after any Config release
// transition completes.
type UpdateScope int

const (
	UpdateAll UpdateScope = iota
	UpdateSoftware
	UpdateRepositories
)

func (s UpdateScope) valid() bool {
	return s >= UpdateAll && s <= UpdateRepositories
}

func (s UpdateScope) arguments() []string {
	switch s {
	case UpdateSoftware:
		return []string{"software"}
	case UpdateRepositories:
		return []string{"repositories"}
	default:
		return nil
	}
}

type Updater struct {
	Paths              Paths
	Version            string
	Config             string
	ReleaseCache       string
	ReleaseState       string
	ReleaseMiseProbe   Runner
	MachineMiseProbe   Runner
	MachineMisePlan    Runner
	Installed          Runner
	ReleaseMise        LiveRunner
	MachineMise        LiveRunner
	SkillsProbe        Runner
	SkillsLive         commandRunner
	InstallReleaseMise func() error
	InstallMachineMise func() error
	Log                Logger
	CurrentExecutable  func() (string, error)
	LoadMachine        func() (Machine, error)
	Reexec             func(string, []string, []string) error
	OperationEvents    bool
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
	releaseInstaller := testedMiseInstallerAt(releaseMise)
	machineInstaller := testedMiseInstaller(paths)
	machineLive := newMiseLiveRunner(paths)
	machineLive.Environment = append(machineLive.Environment, "NO_COLOR=1")
	skillsLive := newAgentSkillsLiveRunner(paths)
	skillsLive.Environment = append(skillsLive.Environment, "NO_COLOR=1")
	live.Stdout, live.Stderr = out, out
	machineLive.Stdout, machineLive.Stderr = out, out
	skillsLive.Stdout, skillsLive.Stderr = out, out
	_, operationEvents := out.(*OperationEventWriter)
	return Updater{
		Paths:        paths,
		Version:      version,
		Config:       command,
		ReleaseCache: filepath.Join(releaseRoot, "cache"),
		ReleaseState: filepath.Join(releaseRoot, "state"),
		ReleaseMiseProbe: OSRunner{
			Dir:         paths.Home,
			Environment: releaseEnvironment,
			Unset:       miseLocalEnvironment,
			Executables: map[string]string{"mise": releaseMise},
		},
		MachineMiseProbe: OSRunner{
			Dir:         paths.Home,
			Environment: releaseEnvironment,
			Unset:       miseLocalEnvironment,
			Executables: map[string]string{"mise": misePath(paths)},
		},
		MachineMisePlan: NewMiseRunner(paths),
		Installed: OSRunner{Dir: paths.Home, Executables: map[string]string{
			"config": command,
		}},
		ReleaseMise:        live,
		MachineMise:        machineLive,
		SkillsProbe:        newAgentSkillsRunner(paths),
		SkillsLive:         skillsLive,
		InstallReleaseMise: releaseInstaller.Install,
		InstallMachineMise: machineInstaller.Install,
		Log:                Logger{Out: out},
		CurrentExecutable:  os.Executable,
		LoadMachine:        func() (Machine, error) { return LoadMachine(paths) },
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

// Apply executes the immutable plan the caller displayed. Config release
// discovery is not repeated; a newer release is acquired by its exact version.
func (u Updater) Apply(plan UpdatePlan) error {
	scope := plan.Scope
	if err := plan.validate(u.Version); err != nil {
		return err
	}
	if !plan.HasWork() {
		return nil
	}
	if u.Version == "dev" {
		// Say it. Otherwise a machine whose canonical command is a local build
		// looks up to date forever.
		u.Log.Warn("this is an unversioned development build; skipping the Config release transition")
		machine, err := u.LoadMachine()
		if err != nil {
			return err
		}
		return u.updateMachine(scope, machine)
	}
	if !stableConfigVersion(u.Version) {
		return fmt.Errorf("Config build version %q cannot update itself", u.Version)
	}
	if resumedVersion, resumed := os.LookupEnv(updateReexecEnv); resumed {
		if resumedVersion != u.Version {
			return fmt.Errorf("Config update resumed with version %q, but this is %s", resumedVersion, u.Version)
		}
		if err := u.requireCanonicalExecutable(); err != nil {
			return err
		}
		machine, err := u.LoadMachine()
		if err != nil {
			return err
		}
		if err := os.Unsetenv(updateReexecEnv); err != nil {
			return fmt.Errorf("clear Config update state: %w", err)
		}
		return u.updateMachine(scope, machine)
	}

	u.Log.Section("Config")
	resolvedVersion := plan.ResolvedVersion
	if compareConfigVersions(resolvedVersion, u.Version) < 0 {
		err := fmt.Errorf("refusing to replace Config %s with older release %s", u.Version, resolvedVersion)
		u.Log.Error(err.Error())
		return fmt.Errorf("Config: %w", err)
	}
	if resolvedVersion == u.Version && u.requireCanonicalExecutable() == nil {
		u.Log.OK("Config " + u.Version + " is current")
		machine, err := u.LoadMachine()
		if err != nil {
			return err
		}
		return u.updateMachine(scope, machine)
	}
	// Release acquisition owns its private adapter. The machine Mise resource
	// is not read or changed until the installed Config resumes machine work.
	if err := u.prepareReleaseMise(); err != nil {
		return err
	}
	if err := u.installRelease(resolvedVersion); err != nil {
		u.Log.Error(err.Error())
		return fmt.Errorf("Config: %w", err)
	}
	installedVersion, err := u.installedVersion()
	if err != nil {
		u.Log.Error(err.Error())
		return fmt.Errorf("Config: %w", err)
	}
	if installedVersion != resolvedVersion {
		err := fmt.Errorf("installed Config is %s, want resolved release %s", installedVersion, resolvedVersion)
		u.Log.Error(err.Error())
		return fmt.Errorf("Config: %w", err)
	}
	u.Log.OK("Config " + installedVersion + " installed")
	u.Log.Version(installedVersion)

	overrides := []string{updateReexecEnv + "=" + installedVersion}
	if u.OperationEvents {
		overrides = append(overrides, OperationEventsEnv+"=1")
	}
	environment := childEnvironment(overrides, nil)
	arguments := append([]string{u.Config, "update"}, scope.arguments()...)
	arguments = append(arguments, "--yes")
	if err := u.Reexec(u.Config, arguments, environment); err != nil {
		return fmt.Errorf("re-exec Config %s: %w", installedVersion, err)
	}
	return nil
}

func (u Updater) updateMachine(scope UpdateScope, machine Machine) error {
	type updateStep struct {
		name    string
		success string
		args    []string
	}
	var failures []error
	if machine.Mise {
		if err := u.prepareMachineMise(); err != nil {
			failures = append(failures, err)
		} else {
			var steps []updateStep
			if scope == UpdateAll || scope == UpdateSoftware {
				steps = append(steps,
					updateStep{"Tools", "declared tools updated", []string{"upgrade", "--yes"}},
					updateStep{"Packages", "declared packages updated", []string{"bootstrap", "packages", "upgrade", "--yes"}},
				)
			}
			if scope == UpdateAll || scope == UpdateRepositories {
				steps = append(steps, updateStep{
					"Repositories", "clean repositories updated",
					[]string{"bootstrap", "repos", "update", "--yes", "--skip-dirty"},
				})
			}
			for _, step := range steps {
				u.Log.Section(step.name)
				if err := u.MachineMise.Command("mise", step.args...); err != nil {
					u.Log.Error(err.Error())
					failures = append(failures, fmt.Errorf("%s: %w", step.name, err))
					continue
				}
				u.Log.OK(step.success)
			}
		}
	}
	if machine.AgentSkills != nil && (scope == UpdateAll || scope == UpdateSoftware) {
		u.Log.Section(agentSkillsName)
		manager := agentSkillManager{
			Paths: u.Paths, Skills: *machine.AgentSkills, Probe: u.SkillsProbe,
			Live: u.SkillsLive, Log: u.Log,
		}
		if err := manager.Update(); err != nil {
			u.Log.Error(err.Error())
			failures = append(failures, fmt.Errorf("%s: %w", agentSkillsName, err))
		}
	}
	return errors.Join(failures...)
}

func (u Updater) prepareReleaseMise() error {
	u.Log.Section("Config release transport")
	if err := ensureTestedMise(u.ReleaseMiseProbe, u.InstallReleaseMise); err != nil {
		u.Log.Error(err.Error())
		return fmt.Errorf("Config release transport: %w", err)
	}
	u.Log.OK("verified release adapter ready")
	return nil
}

func (u Updater) prepareMachineMise() error {
	u.Log.Section(miseName)
	if err := ensureTestedMise(u.MachineMiseProbe, u.InstallMachineMise); err != nil {
		u.Log.Error(err.Error())
		return fmt.Errorf("mise: %w", err)
	}
	u.Log.OK("standalone mise " + testedMiseVersion + " ready")
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
	if u.ReleaseCache == "" || !filepath.IsAbs(u.ReleaseCache) ||
		u.ReleaseState == "" || !filepath.IsAbs(u.ReleaseState) {
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
		// Provenance is the whole point of this runner, so every knob that
		// relaxes it is pinned here rather than inherited from the caller.
		"MISE_GITHUB_GITHUB_ATTESTATIONS=true",
		"MISE_GITHUB_SLSA=true",
		"MISE_PROVENANCE_API_FAILURES_FATAL=true",
		"MISE_MINIMUM_RELEASE_AGE=0s",
		"MISE_CACHE_DIR="+u.ReleaseCache,
		"MISE_STATE_DIR="+u.ReleaseState,
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
