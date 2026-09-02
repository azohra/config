package config

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

const snapshotCommitSubject = "Update machine snapshot"

type Snapshotter struct {
	Paths    Paths
	Machine  Machine
	Runner   Runner
	Live     LiveRunner
	Log      Logger
	Validate func() error
}

func NewSnapshotter(paths Paths, machine Machine, out io.Writer) Snapshotter {
	runner := NewMachineRunner(paths)
	inspector := NewInspector(paths, machine, runner)
	live := newMachineLiveRunner(paths)
	live.Stdout, live.Stderr = out, out
	return Snapshotter{
		Paths:   paths,
		Machine: machine,
		Runner:  runner,
		Live:    live,
		Log:     Logger{Out: out},
		Validate: func() error {
			return inspector.InspectSnapshot().PreflightError()
		},
	}
}

func (s Snapshotter) Save() error {
	s.Log.Section("Snapshot")
	status := snapshotStatus(s.Paths, s.Machine, s.Runner)
	if status.PolicyError != "" {
		return fmt.Errorf("cannot save: %s", status.PolicyError)
	}
	dirty := status.Dirty > 0
	destination := s.Machine.Repository.Destination()
	pushArgs := []string{"push", "--quiet", managedRemote, s.Machine.Repository.Branch}
	if status.Behind > 0 {
		return fmt.Errorf("%s has %s absent from this Mac", destination, FormatCount(status.Behind, "commit", "commits"))
	}
	if !dirty && status.Upstream != "" && status.Ahead == 0 {
		s.Log.OK("already backed up to " + destination)
		return nil
	}
	// The gate on an irreversible commit and push is not optional. A
	// Snapshotter without one cannot prove what it is about to record.
	if s.Validate == nil {
		return fmt.Errorf("cannot save: no machine state gate")
	}
	s.Log.Info("checking machine state")
	if err := s.Validate(); err != nil {
		return err
	}
	s.Log.OK("machine state valid")
	if dirty {
		// git add -A would commit whatever a killed capture stranded.
		s.sweepStrandedWrites()
		if err := s.Live.Command("git", "add", "-A"); err != nil {
			return err
		}
		if err := s.Live.Command("git", "commit", "--quiet", "-m", snapshotCommitSubject); err != nil {
			return err
		}
		s.Log.OK("commit created")
	}
	if err := s.Live.Command("git", pushArgs...); err != nil {
		s.Log.Warn("commit remains local")
		return fmt.Errorf("push to %s: %w", destination, err)
	}
	s.Log.OK("pushed to " + destination)
	saved := snapshotStatus(s.Paths, s.Machine, s.Runner)
	head := run(s.Runner, "git", "rev-parse", "HEAD")
	remote := run(s.Runner, "git", "rev-parse", saved.Upstream)
	if saved.Upstream == "" || head.Err != nil || remote.Err != nil || head.Output() != remote.Output() {
		return fmt.Errorf("saved commit does not match its upstream")
	}
	if saved.Dirty > 0 {
		return fmt.Errorf("configuration changed while the snapshot was being saved")
	}
	s.Log.OK("snapshot verified")
	return nil
}

// sweepStrandedWrites removes the staging files an interrupted atomic write
// leaves in the managed checkout. Each is named by Config and belongs to a run
// that is no longer here.
func (s Snapshotter) sweepStrandedWrites() {
	root := s.Paths.InRoot("snapshots")
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		if strandedWrite(entry.Name()) {
			_ = os.Remove(path)
		}
		return nil
	})
}
