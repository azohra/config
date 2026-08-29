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
	Executables map[string]string
}

func (r OSRunner) Run(ctx context.Context, name string, args ...string) Result {
	cmd := exec.CommandContext(ctx, r.executable(name), args...)
	cmd.Dir = r.Dir
	cmd.Env = ChildEnvironment(r.Environment)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return Result{Stdout: stdout.String(), Stderr: stderr.String(), Err: err}
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
	return runWithTimeout(r, 20*time.Second, name, args...)
}

func runWithTimeout(r Runner, timeout time.Duration, name string, args ...string) Result {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return r.Run(ctx, name, args...)
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
	Executables map[string]string
	Stdin       io.Reader
	Stdout      io.Writer
	Stderr      io.Writer
}

func NewLiveRunner(dir string) LiveRunner {
	return LiveRunner{Dir: dir, Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr}
}

func NewMachineRunner(paths Paths) OSRunner {
	return OSRunner{
		Dir:         paths.Root,
		Environment: MiseEnvironment(paths),
		Executables: map[string]string{"mise": misePath(paths)},
	}
}

func NewMachineLiveRunner(paths Paths) LiveRunner {
	runner := NewLiveRunner(paths.Root)
	runner.Environment = MiseEnvironment(paths)
	runner.Executables = map[string]string{"mise": misePath(paths)}
	return runner
}

func (r LiveRunner) Command(name string, args ...string) error {
	return r.command(r.Stdin, name, args...)
}

func (r LiveRunner) Input(input string, name string, args ...string) error {
	return r.command(strings.NewReader(input), name, args...)
}

func (r LiveRunner) command(stdin io.Reader, name string, args ...string) error {
	if executable := r.Executables[name]; executable != "" {
		name = executable
	}
	cmd := exec.Command(name, args...)
	cmd.Dir = r.Dir
	cmd.Env = ChildEnvironment(r.Environment)
	cmd.Stdin = stdin
	cmd.Stdout = r.Stdout
	cmd.Stderr = r.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", strings.Join(append([]string{name}, args...), " "), err)
	}
	return nil
}

// ChildEnvironment overlays explicit values without leaving duplicate names
// whose winner would depend on the child process's environment parser.
func ChildEnvironment(overrides []string) []string {
	if len(overrides) == 0 {
		return nil // os/exec inherits the current process for a nil Env.
	}
	replaced := make(map[string]bool, len(overrides))
	for _, override := range overrides {
		name, _, ok := strings.Cut(override, "=")
		if ok {
			replaced[name] = true
		}
	}
	environment := make([]string, 0, len(os.Environ())+len(overrides))
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if !replaced[name] {
			environment = append(environment, entry)
		}
	}
	return append(environment, overrides...)
}
