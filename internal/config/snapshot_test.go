package config

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func gitTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func snapshotFixture(t *testing.T) (Snapshotter, string, string) {
	t.Helper()
	root := t.TempDir()
	remote := filepath.Join(t.TempDir(), "remote.git")
	gitTest(t, root, "init", "--quiet", "--initial-branch=main")
	gitTest(t, root, "config", "user.name", "Config Test")
	gitTest(t, root, "config", "user.email", "config@example.com")
	gitTest(t, root, "config", "commit.gpgsign", "false")
	gitTest(t, t.TempDir(), "init", "--quiet", "--bare", remote)
	gitTest(t, root, "remote", "add", "origin", remote)

	home := t.TempDir()
	paths := Paths{Root: root, Home: home, StateDir: stateDir(home)}
	files := map[string]string{
		"settings": "initial\n",
	}
	for name, content := range files {
		if err := os.WriteFile(paths.InRoot(filepath.FromSlash(name)), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runner := OSRunner{Dir: root}
	gitTest(t, root, "add", "-A")
	gitTest(t, root, "commit", "--quiet", "-m", "Initial snapshot")
	gitTest(t, root, "push", "--quiet", "--set-upstream", "origin", "main")

	var output bytes.Buffer
	live := newLiveRunner(root)
	live.Stdout, live.Stderr = &output, &output
	machine := testMachine()
	machine.Repository.URL = remote
	snapshotter := Snapshotter{
		Paths: paths, Machine: machine, Runner: runner, Live: live,
		Log:      Logger{Out: &output},
		Validate: func() error { return nil },
	}
	return snapshotter, root, remote
}

func TestSnapshotSaveAndIdempotence(t *testing.T) {
	snapshotter, root, remote := snapshotFixture(t)
	if err := os.WriteFile(filepath.Join(root, "settings"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := snapshotter.Save(); err != nil {
		t.Fatal(err)
	}
	if got := gitTest(t, root, "log", "-1", "--format=%s"); got != "Update machine snapshot" {
		t.Fatalf("commit subject = %q", got)
	}
	if got := gitTest(t, root, "status", "--short"); got != "" {
		t.Fatalf("worktree dirty: %s", got)
	}
	if local, upstream := gitTest(t, root, "rev-parse", "HEAD"), gitTest(t, remote, "rev-parse", "refs/heads/main"); local != upstream {
		t.Fatal("remote does not match local HEAD")
	}
	if err := snapshotter.Save(); err != nil {
		t.Fatalf("idempotent save: %v", err)
	}
}

func TestSnapshotRefusesAnythingButTheDeclaredMainUpstream(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(t *testing.T, snapshotter *Snapshotter, root string)
		want   string
	}{
		{
			name: "branch",
			mutate: func(t *testing.T, _ *Snapshotter, root string) {
				gitTest(t, root, "switch", "--quiet", "-c", "feature")
			},
			want: "branch is feature; expected main",
		},
		{
			name: "missing upstream",
			mutate: func(t *testing.T, _ *Snapshotter, root string) {
				gitTest(t, root, "config", "--unset", "branch.main.remote")
				gitTest(t, root, "config", "--unset", "branch.main.merge")
			},
			want: "branch has no upstream; expected origin/main",
		},
		{
			name: "wrong remote",
			mutate: func(_ *testing.T, snapshotter *Snapshotter, _ string) {
				snapshotter.Machine.Repository.URL = "https://example.com/owner/machine.git"
			},
			want: "remote origin is not https://example.com/owner/machine.git",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshotter, root, _ := snapshotFixture(t)
			before := gitTest(t, root, "rev-parse", "HEAD")
			test.mutate(t, &snapshotter, root)
			if err := os.WriteFile(filepath.Join(root, "settings"), []byte("changed\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			err := snapshotter.Save()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("save error = %v, want %q", err, test.want)
			}
			if after := gitTest(t, root, "rev-parse", "HEAD"); after != before {
				t.Fatal("rejected target created a commit")
			}
		})
	}
}

func TestSnapshotRefusesARunnerInAnotherRepository(t *testing.T) {
	snapshotter, root, _ := snapshotFixture(t)
	other := t.TempDir()
	gitTest(t, other, "init", "--quiet", "--initial-branch=main")
	snapshotter.Paths.Root = other
	if err := snapshotter.Save(); err == nil || !strings.Contains(err.Error(), "repository root does not match Config's managed checkout") {
		t.Fatalf("wrong-root error = %v", err)
	}
	if got := gitTest(t, root, "status", "--short"); got != "" {
		t.Fatalf("wrong-root check changed the real repository: %s", got)
	}
}

func TestSnapshotRejectedPushLeavesLocalCommit(t *testing.T) {
	snapshotter, root, remote := snapshotFixture(t)
	hook := filepath.Join(remote, "hooks", "pre-receive")
	if err := os.WriteFile(hook, []byte("#!/usr/bin/env bash\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "settings"), []byte("rejected\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := snapshotter.Save(); err == nil {
		t.Fatal("rejected push succeeded")
	}
	if got := gitTest(t, root, "status", "--short"); got != "" {
		t.Fatalf("rejected push left a dirty worktree: %s", got)
	}
	if ahead := gitTest(t, root, "rev-list", "--count", "origin/main..HEAD"); ahead != "1" {
		t.Fatalf("ahead count = %s, want 1", ahead)
	}
}

func TestSnapshotSaveKeepsCommandsQuiet(t *testing.T) {
	snapshotter, root, _ := snapshotFixture(t)
	var chatter bytes.Buffer
	live := newLiveRunner(root)
	live.Stdout, live.Stderr = &chatter, &chatter
	snapshotter.Live = live
	if err := os.WriteFile(filepath.Join(root, "settings"), []byte("quiet\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := snapshotter.Save(); err != nil {
		t.Fatal(err)
	}
	if chatter.Len() > 0 {
		t.Fatalf("save leaked command output: %q", chatter.String())
	}
}

func TestSnapshotHonorsRepositoryCommitHooks(t *testing.T) {
	snapshotter, root, _ := snapshotFixture(t)
	hook := filepath.Join(root, ".git", "hooks", "commit-msg")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	before := gitTest(t, root, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(root, "settings"), []byte("blocked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := snapshotter.Save(); err == nil {
		t.Fatal("repository commit hook was bypassed")
	}
	if after := gitTest(t, root, "rev-parse", "HEAD"); after != before {
		t.Fatal("rejected commit advanced HEAD")
	}
}

// recordingRunner answers nothing and remembers what it was asked.
type recordingRunner struct {
	mu       sync.Mutex
	commands []string
}

func (r *recordingRunner) Run(_ context.Context, name string, args ...string) Result {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.commands = append(r.commands, strings.Join(append([]string{name}, args...), " "))
	return Result{}
}

func (*recordingRunner) Exists(string) bool { return true }

// Save gates on the resources a snapshot records. Computing live platform
// resources anyway makes every save wait on probes whose answers are then
// thrown away.
func TestSnapshotValidationSkipsLivePlatformResources(t *testing.T) {
	runner := &recordingRunner{}
	machine := testMachine()
	machine.FinderFavorites = true
	inspector := NewInspector(testPaths(t), machine, runner)
	inspector.FinderFavorites = &fakeFinderFavorites{}
	report := inspector.InspectSnapshot()

	for _, id := range []string{miseID, macOSID} {
		if _, found := report.Resource(id); found {
			t.Fatalf("the snapshot gate inspected %s: %+v", id, report.Resources)
		}
	}
	for _, command := range runner.commands {
		if strings.HasPrefix(command, "mise ") {
			t.Fatalf("the snapshot gate ran %q", command)
		}
	}
	// The resources it does gate on still have to be there.
	for _, id := range []string{finderFavoritesID, dockID, chromePWAsID, machine.Preferences[0].ID} {
		if _, found := report.Resource(id); !found {
			t.Fatalf("%s is missing from the snapshot gate: %+v", id, report.Resources)
		}
	}
	// A full inspection still reports everything.
	for _, id := range []string{miseID, macOSID} {
		if _, found := inspector.Inspect().Resource(id); !found {
			t.Fatalf("a full inspection dropped %s", id)
		}
	}
}

// The gate NewSnapshotter installs is the one Save runs, so the choice of
// inspection has to hold there and not only in InspectSnapshot.
func TestNewSnapshotterGateNeverReachesForMise(t *testing.T) {
	paths := testPaths(t)
	canonical := misePath(paths)
	if err := os.MkdirAll(filepath.Dir(canonical), 0o755); err != nil {
		t.Fatal(err)
	}
	invocations := filepath.Join(t.TempDir(), "mise-calls")
	script := "#!/bin/sh\nprintf 'called\\n' >> " + invocations + "\n"
	if err := os.WriteFile(canonical, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	snapshotter := NewSnapshotter(paths, testMachine(), io.Discard)
	if snapshotter.Validate == nil {
		t.Fatal("NewSnapshotter installed no gate")
	}
	_ = snapshotter.Validate()

	if _, err := os.Stat(invocations); !os.IsNotExist(err) {
		data, _ := os.ReadFile(invocations)
		t.Fatalf("the snapshot gate ran mise %d time(s)", strings.Count(string(data), "called"))
	}
}

func TestSnapshotRefusesToCommitWhenThePreflightGateFails(t *testing.T) {
	snapshotter, root, remote := snapshotFixture(t)
	before := gitTest(t, root, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(root, "settings"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	snapshotter.Validate = func() error { return errors.New("machine preflight failed") }
	err := snapshotter.Save()
	if err == nil || !strings.Contains(err.Error(), "machine preflight failed") {
		t.Fatalf("Save past a failing gate: %v", err)
	}
	if head := gitTest(t, root, "rev-parse", "HEAD"); head != before {
		t.Fatal("a failing gate still produced a commit")
	}
	if pushed := gitTest(t, remote, "rev-parse", "refs/heads/main"); pushed != before {
		t.Fatal("a failing gate still pushed")
	}

	// A Snapshotter with no gate at all cannot prove what it would record.
	snapshotter.Validate = nil
	if err := snapshotter.Save(); err == nil {
		t.Fatal("Save ran with no machine state gate")
	}
	if head := gitTest(t, root, "rev-parse", "HEAD"); head != before {
		t.Fatal("a missing gate still produced a commit")
	}
}

// failingGitRunner answers every command through the real runner except the
// one it is told to break.
type failingGitRunner struct {
	inner  Runner
	broken string
}

func (r failingGitRunner) Run(ctx context.Context, name string, args ...string) Result {
	if len(args) > 0 && args[0] == r.broken {
		return Result{Stderr: "fatal: unable to read the index", Err: errors.New("exit status 128")}
	}
	return r.inner.Run(ctx, name, args...)
}

func (r failingGitRunner) Exists(name string) bool { return r.inner.Exists(name) }

func TestSnapshotRefusesWhenTheRepositoryCannotBeRead(t *testing.T) {
	// Every field snapshotStatus derives has a zero value that reads as a
	// clean tree in agreement with its upstream, and Save calls that success.
	for _, broken := range []string{"status", "rev-list"} {
		t.Run(broken, func(t *testing.T) {
			snapshotter, root, _ := snapshotFixture(t)
			if err := os.WriteFile(filepath.Join(root, "settings"), []byte("changed\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			snapshotter.Runner = failingGitRunner{inner: snapshotter.Runner, broken: broken}
			err := snapshotter.Save()
			if err == nil {
				t.Fatal("Save reported success over a repository it could not read")
			}
			if !strings.Contains(err.Error(), "unreadable") {
				t.Fatalf("Save error = %v", err)
			}
		})
	}
}

func TestSnapshotDoesNotCommitWhatAKilledCaptureStranded(t *testing.T) {
	// atomicWrite stages beside its target inside the managed checkout, and
	// git add -A stages everything, so a capture killed mid-rename would put
	// its temporary file in the repository's permanent history.
	snapshotter, root, _ := snapshotFixture(t)
	stranded := filepath.Join(root, "snapshots", "dock.apps.tmp.4127")
	if err := os.MkdirAll(filepath.Dir(stranded), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stranded, []byte("half a layout\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "settings"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := snapshotter.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stranded); !os.IsNotExist(err) {
		t.Fatal("the stranded write survived the save")
	}
	if tracked := gitTest(t, root, "ls-files", "snapshots"); strings.Contains(tracked, ".tmp.") {
		t.Fatalf("a staging file reached the snapshot: %s", tracked)
	}
}
