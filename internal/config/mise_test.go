package config

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestMiseVersionContract(t *testing.T) {
	for _, test := range []struct {
		output  string
		minimum string
		want    bool
	}{
		{"2026.8.13 macos-arm64", "2026.8.13", true},
		{"mise 2026.9.0", "2026.8.13", true},
		{"v2026.8.12", "2026.8.13", false},
		{"2025.12.99", "2026.8.13", false},
		{"not a version", "2026.8.13", false},
	} {
		if got := miseVersionAtLeast(test.output, test.minimum); got != test.want {
			t.Errorf("miseVersionAtLeast(%q, %q) = %v, want %v", test.output, test.minimum, got, test.want)
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
}

func (r *miseStubRunner) Run(_ context.Context, name string, args ...string) Result {
	r.mu.Lock()
	r.commands = append(r.commands, strings.Join(append([]string{name}, args...), " "))
	r.mu.Unlock()
	switch {
	case name == "mise" && slices.Equal(args, []string{"--version"}):
		return Result{Stdout: minimumMiseVersion}
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
// repository, to answer a freshness question config update already owns.
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
}
