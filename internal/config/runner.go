package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Result struct {
	Stdout string
	Stderr string
	Err    error
}

func (r Result) Output() string {
	return strings.TrimSpace(r.Stdout)
}

type Runner interface {
	Run(ctx context.Context, name string, args ...string) Result
	Exists(name string) bool
}

type OSRunner struct {
	Dir         string
	Environment []string
	Unset       []string
	Executables map[string]string
}

// CommandWaitDelay bounds the wait on a command's output pipes after the
// command itself is done with them. Both launchers use it, so the two halves
// of the process contract cannot drift apart.
const CommandWaitDelay = 2 * time.Second

func (r OSRunner) Run(ctx context.Context, name string, args ...string) Result {
	cmd := exec.CommandContext(ctx, r.executable(name), args...)
	cmd.Dir = r.Dir
	cmd.Env = childEnvironment(r.Environment, r.Unset)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	settle := InterruptGroup(cmd, CommandWaitDelay)
	err := cmd.Run()
	settle(err)
	return Result{Stdout: stdout.String(), Stderr: stderr.String(), Err: CommandFailure(cmd, err)}
}

// InterruptGroup makes a command killable as a process group and bounds the
// wait on its output pipes. Without both, a deadline is unenforceable: the
// default cancellation kills the process Config started, while Wait goes on
// waiting for every descendant that inherited its pipes.
//
// It returns a function to call with the command's own error once Wait has
// returned. That cancels the pending group kill, so a signal cannot arrive
// after the group is gone and land on a process that reused its identifier.
func InterruptGroup(cmd *exec.Cmd, delay time.Duration) func(error) {
	var (
		mu         sync.Mutex
		escalation *time.Timer
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = delay
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		group := -cmd.Process.Pid
		err := syscall.Kill(group, syscall.SIGINT)
		// A shell starts a background command with SIGINT ignored, and the
		// escalation os/exec performs when WaitDelay elapses reaches only the
		// process Config started. Without this the group survives a cancel.
		mu.Lock()
		escalation = time.AfterFunc(delay, func() { _ = syscall.Kill(group, syscall.SIGKILL) })
		mu.Unlock()
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	return func(err error) {
		// ErrWaitDelay means the pipes outlived the command, which is the one
		// case the group kill is for. Every other ending has already reaped
		// the group, so the pending signal has nothing left to reach.
		if errors.Is(err, exec.ErrWaitDelay) {
			return
		}
		mu.Lock()
		if escalation != nil {
			escalation.Stop()
		}
		mu.Unlock()
	}
}

// CommandFailure reports why a command failed, or nil when it did not. A
// command that exits successfully but leaves a descendant holding its output
// pipes makes Wait return ErrWaitDelay once the delay elapses: the command did
// what it was asked, and only the plumbing outlived it.
func CommandFailure(cmd *exec.Cmd, err error) error {
	if errors.Is(err, exec.ErrWaitDelay) && cmd.ProcessState != nil && cmd.ProcessState.Success() {
		return nil
	}
	return err
}

func (r OSRunner) Exists(name string) bool {
	_, err := exec.LookPath(r.executable(name))
	return err == nil
}

func (r OSRunner) executable(name string) string {
	if executable := r.Executables[name]; executable != "" {
		return executable
	}
	return name
}

func run(r Runner, name string, args ...string) Result {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return r.Run(ctx, name, args...)
}

// Failure reports why a command failed in the command's own words. An
// exec.ExitError renders as "exit status 1" and says nothing a reader can act
// on, while the tool has usually already written the reason to stderr, which
// OSRunner buffers rather than showing.
func (r Result) Failure() error {
	if r.Err == nil {
		return nil
	}
	for line := range strings.SplitSeq(r.Stderr, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return fmt.Errorf("%s (%w)", line, r.Err)
		}
	}
	return r.Err
}

// ExitCode extracts the process exit code from a Result error, or -1 when the
// process did not run to completion (timeout, missing binary).
func (r Result) ExitCode() int {
	var exit *exec.ExitError
	if errors.As(r.Err, &exit) {
		return exit.ExitCode()
	}
	return -1
}

type LiveRunner struct {
	Dir         string
	Environment []string
	Unset       []string
	Executables map[string]string
	Stdin       io.Reader
	Stdout      io.Writer
	Stderr      io.Writer
}

func newLiveRunner(dir string) LiveRunner {
	return LiveRunner{
		Dir: dir, Unset: gitLocalEnvironment,
		Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr,
	}
}

func NewMachineRunner(paths Paths) OSRunner {
	return OSRunner{
		Dir:         paths.Root,
		Environment: miseEnvironment(paths),
		Unset:       gitLocalEnvironment,
		Executables: map[string]string{"mise": misePath(paths)},
	}
}

func newMachineLiveRunner(paths Paths) LiveRunner {
	runner := newLiveRunner(paths.Root)
	runner.Environment = miseEnvironment(paths)
	runner.Unset = gitLocalEnvironment
	runner.Executables = map[string]string{"mise": misePath(paths)}
	return runner
}

func (r LiveRunner) Command(name string, args ...string) error {
	if executable := r.Executables[name]; executable != "" {
		name = executable
	}
	cmd := exec.Command(name, args...)
	cmd.Dir = r.Dir
	cmd.Env = childEnvironment(r.Environment, r.Unset)
	cmd.Stdin = r.Stdin
	cmd.Stdout = r.Stdout
	cmd.Stderr = r.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", strings.Join(append([]string{name}, args...), " "), err)
	}
	return nil
}

// childEnvironment overlays explicit values without leaving duplicate names
// whose winner would depend on the child process's environment parser, and
// drops the names in unset. An explicit override wins: a value Config supplies
// is a decision, while unset only removes what the caller happened to export.
func childEnvironment(overrides, unset []string) []string {
	if len(overrides) == 0 && len(unset) == 0 {
		return nil // os/exec inherits the current process for a nil Env.
	}
	replaced := make(map[string]bool, len(overrides))
	for _, override := range overrides {
		name, _, ok := strings.Cut(override, "=")
		if ok {
			replaced[name] = true
		}
	}
	removed := make(map[string]bool, len(unset))
	for _, name := range unset {
		if !replaced[name] {
			removed[name] = true
		}
	}
	environment := make([]string, 0, len(os.Environ())+len(overrides))
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if !replaced[name] && !removed[name] {
			environment = append(environment, entry)
		}
	}
	return append(environment, overrides...)
}

// gitLocalEnvironment are the variables Git resolves against one specific
// repository. Config runs Git to ask which repository it is looking at, so a
// value the caller exported would answer about a different one.
var gitLocalEnvironment = []string{
	"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_CONFIG", "GIT_CONFIG_PARAMETERS",
	"GIT_CONFIG_COUNT", "GIT_OBJECT_DIRECTORY", "GIT_DIR", "GIT_WORK_TREE",
	"GIT_IMPLICIT_WORK_TREE", "GIT_GRAFT_FILE", "GIT_INDEX_FILE",
	"GIT_NO_REPLACE_OBJECTS", "GIT_REPLACE_REF_BASE", "GIT_PREFIX",
	"GIT_SHALLOW_FILE", "GIT_COMMON_DIR",
}

// newGitRunner reads a Git repository at dir and nothing else.
func newGitRunner(dir string) OSRunner {
	return OSRunner{Dir: dir, Unset: gitLocalEnvironment}
}
