package main

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/term"

	config "github.com/azohra/config/internal/config"
	"github.com/azohra/config/internal/ui"
)

const usage = `config — keep this Mac and its snapshot in sync

Usage:
  config
  config --status
  config --version
  config path
  config update
  config bootstrap <repository>

Config inspects first, proposes a plan, requires explicit choices for app state,
then offers to commit and push the resulting snapshot.

Bootstrap clones an authenticated Git repository into Config's managed storage,
installs the permanent Config command, and resumes restore until every declared
step has completed. Path prints the managed repository's canonical location.

Update verifies its canonical mise substrate, installs the latest verified
Config release, then continues from that release to update mise, declared tools,
packages, and clean repositories. It runs only when explicitly invoked.`

var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	args := os.Args[1:]
	if len(args) == 1 && args[0] == "--version" {
		fmt.Println("config", version)
		return nil
	}
	if len(args) > 0 && slices.Contains([]string{"-h", "--help", "help"}, args[0]) {
		if len(args) != 1 {
			return errors.New("help takes no arguments")
		}
		fmt.Println(usage)
		return nil
	}
	paths, err := config.NewPaths("", "")
	if err != nil {
		return err
	}
	if len(args) > 0 && args[0] == "path" {
		if len(args) != 1 {
			return errors.New("usage: config path")
		}
		fmt.Println(paths.Root)
		return nil
	}
	if len(args) > 0 && args[0] == "install" {
		if len(args) != 1 {
			return errors.New("usage: config install")
		}
		return config.InstallCurrent(paths)
	}
	if len(args) > 0 && args[0] == "update" {
		if len(args) != 1 {
			return errors.New("usage: config update")
		}
		return config.NewUpdater(paths, os.Stdout, version).Update()
	}
	var machine config.Machine
	restorePending := false
	if len(args) > 0 && args[0] == "bootstrap" {
		if len(args) != 2 {
			return errors.New("usage: config bootstrap <repository>")
		}
		machine, restorePending, err = config.MaterializeRepository(paths, args[1], os.Stdout, os.Stderr)
		if err == nil {
			err = config.InstallCurrent(paths)
		}
		args = nil
	} else {
		machine, err = config.LoadMachine(paths)
	}
	if err != nil {
		return err
	}
	runner := config.NewMachineRunner(paths)
	if restorePending {
		return config.RestorePending(paths, machine, os.Stdout)
	}
	if len(args) > 0 {
		switch args[0] {
		case "--status":
			if len(args) != 1 {
				return fmt.Errorf("unknown option: %s", args[1])
			}
			return status(paths, machine, runner)
		case "--apply":
			if len(args) != 2 {
				return errors.New("invalid apply plan")
			}
			selections, err := config.DecodeSelections(args[1])
			if err != nil {
				return err
			}
			report := config.NewInspector(paths, machine, runner).Inspect()
			if err := config.ValidateSelections(report, selections); err != nil {
				return err
			}
			return config.NewApplier(paths, machine, os.Stdout).Apply(selections)
		case "--snapshot":
			if len(args) != 1 {
				return errors.New("invalid snapshot request")
			}
			return config.NewSnapshotter(paths, machine, os.Stdout).Save()
		default:
			if !strings.HasPrefix(args[0], "-") {
				return errors.New("unknown command; run config help for available commands")
			}
			return fmt.Errorf("unknown option: %s", args[0])
		}
	}
	if !terminal(os.Stdin) || !terminal(os.Stdout) {
		return status(paths, machine, runner)
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	_, err = tea.NewProgram(ui.New(paths, machine, executable)).Run()
	return err
}

// status is the one status surface. Without a terminal Config prints the same
// report it prints for --status, so it owes the same answer about whether the
// machine needs attention.
func status(paths config.Paths, machine config.Machine, runner config.Runner) error {
	report := config.NewInspector(paths, machine, runner).Inspect()
	config.WriteStatus(os.Stdout, report)
	if report.NeedsAttention() {
		return errors.New("configuration needs attention")
	}
	return nil
}

// terminal asks whether a stream is a terminal, not whether it is a character
// device: /dev/null is a character device, and redirecting to it must not
// start the interactive interface.
func terminal(file *os.File) bool {
	return term.IsTerminal(file.Fd())
}
