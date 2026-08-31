package config

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func TestMiseVersionParsing(t *testing.T) {
	for _, test := range []struct {
		output string
		want   string
		ok     bool
	}{
		{"2026.8.14 macos-arm64", "2026.8.14", true},
		{"mise 2026.9.0", "2026.9.0", true},
		{"v2026.8.13", "2026.8.13", true},
		{"2026.8.14-beta.1", "", false},
		{"not a version", "", false},
	} {
		got, ok := miseVersion(test.output)
		if got != test.want || ok != test.ok {
			t.Errorf("miseVersion(%q) = %q, %v; want %q, %v", test.output, got, ok, test.want, test.ok)
		}
	}
}

// miseStubRunner answers the probes miseChecks makes and records every
// command, so a test can assert what Config does not ask mise for.
type miseStubRunner struct {
	mu           sync.Mutex
	commands     []string
	phase        map[string]Result
	files        []string
	repos        map[string]string
	origins      map[string]string
	missingTools string
	version      string
}

func (r *miseStubRunner) Run(_ context.Context, name string, args ...string) Result {
	r.mu.Lock()
	r.commands = append(r.commands, strings.Join(append([]string{name}, args...), " "))
	r.mu.Unlock()
	switch {
	case name == "mise" && slices.Equal(args, []string{"--version"}):
		if r.version != "" {
			return Result{Stdout: r.version}
		}
		return Result{Stdout: testedMiseVersion}
	case name == "mise" && slices.Equal(args, []string{"ls", "--missing", "-J"}):
		if r.missingTools == "" {
			return Result{Stdout: "{}"}
		}
		return Result{Stdout: r.missingTools}
	case name == "mise" && slices.Equal(args, []string{"config", "ls", "-J"}):
		entries := make([]string, len(r.files))
		for index, file := range r.files {
			entries[index] = fmt.Sprintf(`{"path":%q}`, file)
		}
		return Result{Stdout: "[" + strings.Join(entries, ",") + "]"}
	case name == "mise" && len(args) == 5 && args[0] == "config" && args[1] == "get":
		declaration, declared := r.repos[args[3]]
		if !declared {
			return Result{Err: exec.Command("/usr/bin/false").Run()}
		}
		return Result{Stdout: declaration}
	case name == "git" && len(args) == 5 && args[0] == "-C" && args[2] == "remote":
		origin, known := r.origins[filepath.Base(args[1])]
		if !known {
			return Result{Err: exec.Command("/usr/bin/false").Run()}
		}
		return Result{Stdout: origin + "\n"}
	case name == "mise" && len(args) > 2 && args[0] == "bootstrap" && args[len(args)-2] == "status":
		return r.phase[strings.Join(args[1:len(args)-2], " ")]
	default:
		return Result{Err: fmt.Errorf("unexpected command: %s %v", name, args)}
	}
}

func (*miseStubRunner) Exists(string) bool { return true }

func TestMiseChecksRequireTheTestedVersion(t *testing.T) {
	for _, version := range []string{"2026.8.13", "2026.8.15"} {
		checks := (Inspector{Runner: &miseStubRunner{version: version}}).miseChecks()
		if len(checks) != 1 || checks[0].OK ||
			checks[0].Label != "mise "+version+" is unsupported" ||
			!strings.Contains(checks[0].Detail, testedMiseVersion) {
			t.Fatalf("mise %s produced %+v", version, checks)
		}
	}
}

// A converged phase exits zero, which Result.ExitCode reports as -1 because
// there is no ExitError to read. Reading the code before the error turns
// every healthy phase into a missing binary.
func TestMiseChecksTreatConvergedPhasesAsCurrent(t *testing.T) {
	for _, check := range (Inspector{Runner: &miseStubRunner{}}).miseChecks() {
		if !check.OK {
			t.Fatalf("a fully converged machine reported %+v", check)
		}
	}
}

func TestMiseChecksNameTheBootstrapPhaseThatNeedsAttention(t *testing.T) {
	drifted := Result{Err: exec.Command("/usr/bin/false").Run()}
	runner := &miseStubRunner{phase: map[string]Result{"dotfiles": drifted}}
	checks := (Inspector{Runner: runner}).miseChecks()

	var found bool
	for _, check := range checks {
		switch check.Label {
		case "mise bootstrap state needs attention":
			found = true
			if check.Detail != "dotfiles" {
				t.Fatalf("check does not name the failing phase: %+v", check)
			}
		case "mise bootstrap unavailable":
			t.Fatalf("converged phases were reported as unavailable: %+v", check)
		}
	}
	if !found {
		t.Fatalf("a drifted phase produced no failure: %+v", checks)
	}
}

// The aggregate mise reports costs one network round trip per declared
// repository to answer a freshness question the repository update scope owns.
// Inspection must never reach for it.
func TestMiseChecksNeverAskMiseForRepositoryFreshness(t *testing.T) {
	runner := &miseStubRunner{}
	(Inspector{Runner: runner}).miseChecks()
	for _, command := range runner.commands {
		if strings.HasPrefix(command, "mise bootstrap repos status") ||
			strings.HasPrefix(command, "mise bootstrap status") {
			t.Fatalf("inspection asked mise for repository freshness: %q", command)
		}
	}
}

// mise exposes no bootstrap phase for tools, so asking by phase drops tool
// coverage unless something else asks. ls --missing exits zero either way.
func TestToolCheckReportsMissingDeclaredTools(t *testing.T) {
	if check := (Inspector{Runner: &miseStubRunner{}}).toolCheck(); !check.OK {
		t.Fatalf("a complete machine reported %+v", check)
	}
	runner := &miseStubRunner{missingTools: `{"node":[{"installed":false}],"bun":[{"installed":false}]}`}
	check := (Inspector{Runner: runner}).toolCheck()
	if check.OK || check.Label != "2 declared tools missing" || check.Detail != "bun, node" {
		t.Fatalf("toolCheck() = %+v", check)
	}
}

// A declared checkout that is absent, or that holds a different repository,
// is drift. Both answers are local: neither needs the network that made the
// aggregate expensive.
func TestRepositoryChecksCatchAbsentAndForeignCheckouts(t *testing.T) {
	paths := testPaths(t)
	for _, name := range []string{"present", "foreign"} {
		if err := os.MkdirAll(filepath.Join(paths.Home, "Development", name, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	runner := &miseStubRunner{
		files: []string{"/machine/mise/conf.d/repositories.toml", "/machine/mise/conf.d/dotfiles.toml"},
		repos: map[string]string{"/machine/mise/conf.d/repositories.toml": `
["~/Development/present"]
url = "git@github.com:example/present.git"

["~/Development/foreign"]
url = "git@github.com:example/foreign.git"

["~/Development/absent"]
url = "git@github.com:example/absent.git"
`},
		origins: map[string]string{
			"present": "git@github.com:example/present.git",
			"foreign": "https://github.com/someone/entirely-different.git",
		},
	}

	checks := (Inspector{Paths: paths, Runner: runner}).repositoryChecks()
	found := map[string]string{}
	for _, check := range checks {
		if check.OK {
			t.Fatalf("a passing check among failures: %+v", checks)
		}
		found[check.Label] = check.Detail
	}
	if found["1 declared repository missing"] != "absent" {
		t.Fatalf("absent checkout not reported: %+v", checks)
	}
	if found["1 checkout is another repository"] != "foreign" {
		t.Fatalf("foreign checkout not reported: %+v", checks)
	}

	// Point the foreign checkout at what it should be, and the drift clears.
	runner.origins["foreign"] = "git@github.com:example/foreign.git"
	if err := os.MkdirAll(filepath.Join(paths.Home, "Development", "absent", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner.origins["absent"] = "git@github.com:example/absent.git"
	checks = (Inspector{Paths: paths, Runner: runner}).repositoryChecks()
	if len(checks) != 1 || !checks[0].OK {
		t.Fatalf("every checkout correct but repositoryChecks() = %+v", checks)
	}
}

// Config names mise's phases, so mise growing one must fail the build rather
// than quietly going unreported. Every bootstrap subcommand that offers a
// status verb has to be covered, except repos, which Config answers itself.
func TestMisePhasesCoverEveryBootstrapPhase(t *testing.T) {
	// Skipping here would retire the guard silently. The check task runs the
	// suite through mise, so mise is on PATH whenever these tests run at all.
	// Keep mise's shared configuration ledger out of this test: the probe is
	// about command vocabulary, not configuration discovery.
	t.Setenv("MISE_STATE_DIR", t.TempDir())
	mise, err := exec.LookPath("mise")
	if err != nil {
		t.Fatal("mise is not on PATH, so this guard cannot run")
	}
	help, err := exec.Command(mise, "bootstrap", "--help").CombinedOutput()
	if err != nil {
		t.Fatalf("mise bootstrap --help: %v\n%s", err, help)
	}
	commands, inCommands := map[string]bool{}, false
	for _, line := range strings.Split(string(help), "\n") {
		if strings.HasPrefix(line, "Commands:") {
			inCommands = true
			continue
		}
		if inCommands {
			if strings.HasPrefix(line, "Flags:") || strings.HasPrefix(line, "Global flags:") {
				break
			}
			fields := strings.Fields(line)
			// A description continues on its own indented line; only a line
			// whose first field is flush with the command column names one.
			if len(fields) > 1 && strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "     ") {
				commands[fields[0]] = true
			}
		}
	}
	if len(commands) == 0 {
		t.Fatalf("no bootstrap subcommands parsed from:\n%s", help)
	}

	covered := map[string]bool{"repos": true}
	for _, phase := range misePhases {
		covered[phase[0]] = true
	}
	for command := range commands {
		if covered[command] || command == "help" {
			continue
		}
		// A command that takes arguments swallows "status" instead of
		// running it, so only a real status verb names itself in its usage.
		usage, err := exec.Command(mise, "bootstrap", command, "status", "--help").CombinedOutput()
		if err != nil || !strings.Contains(string(usage), "Usage: mise bootstrap "+command+" status") {
			continue
		}
		t.Errorf("mise bootstrap %s offers a status verb that misePhases does not cover", command)
	}

	// The other direction. Covering what mise offers still holds when mise
	// removes or renames a phase, and Config would go on asking for one that
	// no longer exists.
	for _, phase := range misePhases {
		if !commands[phase[0]] {
			t.Errorf("misePhases names %q, which mise %s no longer offers", phase[0], testedMiseVersion)
		}
	}
	if !commands["repos"] {
		t.Errorf("mise %s no longer offers the repos phase Config probes itself", testedMiseVersion)
	}
}

func TestTestedMiseVersionIsTheOnlyOneTheRepositoryNames(t *testing.T) {
	// The version Config accepts at runtime is restated by hand in the
	// workflows and the README. Nothing proved they agree, and they drift.
	for _, file := range []string{
		"../../.github/workflows/check.yml",
		"../../.github/workflows/release.yml",
		"../../README.md",
	} {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		found := miseVersionPattern.FindAllString(string(data), -1)
		if len(found) == 0 {
			t.Errorf("%s names no mise version", file)
		}
		for _, version := range found {
			if version != testedMiseVersion {
				t.Errorf("%s names mise %s, not the tested %s", file, version, testedMiseVersion)
			}
		}
	}
	// min_version is a floor for developing this repository, so it may lag the
	// tested version but must never lead it.
	data, err := os.ReadFile("../../mise.toml")
	if err != nil {
		t.Fatal(err)
	}
	floor := miseVersionPattern.FindString(string(data))
	if floor == "" {
		t.Fatal("mise.toml names no mise version")
	}
	if miseVersionOrder(floor) > miseVersionOrder(testedMiseVersion) {
		t.Errorf("mise.toml requires mise %s, ahead of the tested %s", floor, testedMiseVersion)
	}
}

// miseVersionOrder collapses a calendar version into one comparable number.
func miseVersionOrder(version string) int {
	order := 0
	for _, field := range strings.Split(version, ".") {
		number, err := strconv.Atoi(field)
		if err != nil {
			return -1
		}
		order = order*1000 + number
	}
	return order
}

var miseVersionPattern = regexp.MustCompile(`\b20[0-9]{2}\.[0-9]{1,2}\.[0-9]{1,2}\b`)

// miseRepositoriesRunner answers the two commands miseRepositories issues.
type miseRepositoriesRunner struct{ repos string }

func (r miseRepositoriesRunner) Run(_ context.Context, name string, args ...string) Result {
	if name != "mise" || len(args) < 2 || args[0] != "config" {
		return Result{Err: fmt.Errorf("unexpected command %s %s", name, strings.Join(args, " "))}
	}
	switch args[1] {
	case "ls":
		return Result{Stdout: `[{"path":"/machine/mise/config.toml"}]`}
	case "get":
		return Result{Stdout: r.repos}
	}
	return Result{Err: fmt.Errorf("unexpected command %s %s", name, strings.Join(args, " "))}
}

func (miseRepositoriesRunner) Exists(string) bool { return true }

func TestMiseRepositoriesRequireAnAbsolutePath(t *testing.T) {
	// os.Stat resolves a relative path against Config's working directory
	// while the declared-repository check resolves it elsewhere, so the two
	// halves of one check would answer about different directories.
	paths := testPaths(t)
	const url = "url = \"https://github.com/owner/machine.git\"\n"

	declared, err := miseRepositories(paths, miseRepositoriesRunner{repos: "[\"/opt/checkout\"]\n" + url})
	if err != nil {
		t.Fatal(err)
	}
	if len(declared) != 1 || declared[0].Path != "/opt/checkout" {
		t.Fatalf("absolute declaration = %#v", declared)
	}

	declared, err = miseRepositories(paths, miseRepositoriesRunner{repos: "[\"~/code/machine\"]\n" + url})
	if err != nil {
		t.Fatal(err)
	}
	if len(declared) != 1 || declared[0].Path != paths.InHome("code", "machine") {
		t.Fatalf("home declaration = %#v", declared)
	}

	if _, err := miseRepositories(paths, miseRepositoriesRunner{repos: "[\"relative/checkout\"]\n" + url}); err == nil {
		t.Fatal("a relative declared repository path was accepted")
	} else if !strings.Contains(err.Error(), "relative/checkout") {
		t.Fatalf("refusal does not name the declaration: %v", err)
	}
}
