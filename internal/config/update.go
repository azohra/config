package config

import (
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
	Version           string
	Mise              string
	Config            string
	ReleaseCache      string
	ReleaseState      string
	MiseProbe         Runner
	Installed         Runner
	Substrate         LiveRunner
	Machine           LiveRunner
	Log               Logger
	CurrentExecutable func() (string, error)
	ValidateMachine   func() error
	Reexec            func(string, []string, []string) error
}

func NewUpdater(paths Paths, out io.Writer, version string) Updater {
	command := ConfigCommandPath(paths)
	substrateEnvironment := []string{"MISE_AUTO_UPDATE=0", "MISE_NO_CONFIG=1"}
	live := NewLiveRunner(paths.Home)
	live.Environment = substrateEnvironment
	live.Executables = map[string]string{"mise": misePath(paths)}
	return Updater{
		Version: version,
		Mise:    misePath(paths),
		Config:  command,
		ReleaseCache: filepath.Join(
			filepath.Dir(paths.StateDir), "release-mise",
		),
		ReleaseState: filepath.Join(
			filepath.Dir(paths.StateDir), "release-mise-state",
		),
		MiseProbe: OSRunner{
			Dir:         paths.Home,
			Environment: substrateEnvironment,
			Executables: map[string]string{"mise": misePath(paths)},
		},
		Installed: OSRunner{Dir: paths.Home, Executables: map[string]string{
			"config": command,
		}},
		Substrate:         live,
		Machine:           NewMachineLiveRunner(paths),
		Log:               Logger{Out: out},
		CurrentExecutable: os.Executable,
		ValidateMachine: func() error {
			_, err := LoadMachine(paths)
			return err
		},
		Reexec: syscall.Exec,
	}
}

func (u Updater) Update(scope UpdateScope) error {
	if !scope.valid() {
		return errors.New("invalid update scope")
	}
	if err := requireExecutableFile(u.Mise); err != nil {
		return fmt.Errorf("mise unavailable at %s", u.Mise)
	}
	if u.Version == "dev" {
		if err := u.ValidateMachine(); err != nil {
			return err
		}
		return u.updateMachine(scope)
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
		if err := u.ValidateMachine(); err != nil {
			return err
		}
		if err := os.Unsetenv(updateReexecEnv); err != nil {
			return fmt.Errorf("clear Config update state: %w", err)
		}
		return u.updateMachine(scope)
	}

	// The current release first restores the mise version it was tested with.
	// That gives release acquisition a known substrate even when mise drifted.
	if err := u.updateMise(); err != nil {
		return err
	}
	u.Log.Section("Config")
	resolvedVersion, err := u.resolveRelease()
	if err != nil {
		u.Log.Error(err.Error())
		return fmt.Errorf("Config: %w", err)
	}
	if compareConfigVersions(resolvedVersion, u.Version) < 0 {
		err := fmt.Errorf("refusing to replace Config %s with older release %s", u.Version, resolvedVersion)
		u.Log.Error(err.Error())
		return fmt.Errorf("Config: %w", err)
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

	environment := ChildEnvironment([]string{updateReexecEnv + "=" + installedVersion})
	arguments := append([]string{u.Config, "update"}, scope.arguments()...)
	if err := u.Reexec(u.Config, arguments, environment); err != nil {
		return fmt.Errorf("re-exec Config %s: %w", installedVersion, err)
	}
	return nil
}

func (u Updater) updateMachine(scope UpdateScope) error {
	if err := u.updateMise(); err != nil {
		return err
	}

	type updateStep struct {
		name    string
		success string
		args    []string
	}
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

	var failures []error
	for _, step := range steps {
		u.Log.Section(step.name)
		if err := u.Machine.Command("mise", step.args...); err != nil {
			u.Log.Error(err.Error())
			failures = append(failures, fmt.Errorf("%s: %w", step.name, err))
			continue
		}
		u.Log.OK(step.success)
	}
	return errors.Join(failures...)
}

func (u Updater) updateMise() error {
	u.Log.Section("mise")
	if err := u.Substrate.Command("mise", "--no-config", "self-update", testedMiseVersion, "--yes", "--no-plugins"); err != nil {
		u.Log.Error(err.Error())
		return fmt.Errorf("mise: %w", err)
	}
	if err := requireTestedMise(u.MiseProbe); err != nil {
		u.Log.Error(err.Error())
		return fmt.Errorf("mise: %w", err)
	}
	u.Log.OK("standalone mise set to " + testedMiseVersion)
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

func (u Updater) resolveRelease() (string, error) {
	if u.ReleaseCache == "" || !filepath.IsAbs(u.ReleaseCache) ||
		u.ReleaseState == "" || !filepath.IsAbs(u.ReleaseState) {
		return "", errors.New("Config release cache paths are invalid")
	}
	if _, err := u.releaseOutput("mise", "--no-config", "cache", "clear"); err != nil {
		return "", fmt.Errorf("refresh Config release metadata: %w", err)
	}
	output, err := u.releaseOutput("mise", "--no-config", "latest", configReleaseBackend)
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
	var output strings.Builder
	releaseRunner := u.releaseRunner()
	releaseRunner.Stdout = &output
	if err := releaseRunner.Command(name, args...); err != nil {
		return "", err
	}
	return output.String(), nil
}

func (u Updater) releaseRunner() LiveRunner {
	runner := u.Substrate
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
