package config

import (
	"fmt"
	"io"
	"strings"
)

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
	return Snapshotter{
		Paths:   paths,
		Machine: machine,
		Runner:  runner,
		Live:    NewLiveRunner(paths.Root),
		Log:     Logger{Out: out},
		Validate: func() error {
			return inspector.Inspect().PreflightError()
		},
	}
}

func (s Snapshotter) Save(message string) error {
	s.Log.Section("Snapshot")
	status := snapshotStatus(s.Paths, s.Machine, s.Runner)
	if status.PolicyError != "" {
		return fmt.Errorf("cannot save: %s", status.PolicyError)
	}
	dirty := status.Dirty > 0
	if dirty && strings.TrimSpace(message) == "" {
		return fmt.Errorf("a snapshot message is required")
	}
	destination := s.Machine.Repository.Destination()
	pushArgs := []string{"push", "--quiet", managedRemote, s.Machine.Repository.Branch}
	if status.Behind > 0 {
		return fmt.Errorf("%s has %s absent from this Mac", destination, FormatCount(status.Behind, "commit", "commits"))
	}
	if !dirty && status.Upstream != "" && status.Ahead == 0 {
		s.Log.OK("already backed up to " + destination)
		return nil
	}
	if s.Validate != nil {
		s.Log.Info("checking machine state")
		if err := s.Validate(); err != nil {
			return err
		}
		s.Log.OK("machine state valid")
	}
	if dirty {
		if err := s.Live.Command("git", "add", "-A"); err != nil {
			return err
		}
		if err := s.Live.Command("git", "commit", "--quiet", "-m", message); err != nil {
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
