package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
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
  config update [software | repositories]
  config prune [--dry-run | --yes]
  config bootstrap <repository>

Config inspects first, proposes a plan, requires explicit choices for app state,
then offers to commit and push the resulting snapshot.

Bootstrap clones an authenticated Git repository into Config's managed storage,
installs the permanent Config command, and resumes restore until every declared
step has completed. Path prints the managed repository's canonical location.

Update verifies its canonical mise substrate and installs the latest verified
Config release before updating the selected resources. With no selection it
updates declared tools, packages, and clean repositories. Software omits the
networked repository phase; repositories runs only that phase.

Prune previews stale mise inventory and Config-owned local state. Without a
terminal it is preview-only; --yes applies the exact plan after checking it
again.`

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
	defer config.OnInterrupt(os.Stderr)()
	// An argument error is an argument error. Deciding these after the machine
	// document loads reported a typo on a Mac with no managed checkout as a
	// missing repository, and prescribed bootstrap.
	if err := checkArguments(args); err != nil {
		return err
	}
	// Every command that writes takes the checkout lock. The terminal
	// interface does not: it launches these same commands as children, and
	// each one takes the lock for the work it does.
	if len(args) > 0 && slices.Contains(
		[]string{"install", "update", "bootstrap", "prune", "--apply", "--snapshot"}, args[0]) {
		release, lockErr := config.LockCheckout(paths)
		if lockErr != nil {
			return lockErr
		}
		defer release()
	}
	if len(args) > 0 && args[0] == "path" {
		fmt.Println(paths.Root)
		return nil
	}
	if len(args) > 0 && args[0] == "install" {
		return config.InstallCurrent(paths, version, os.Stdout)
	}
	if len(args) > 0 && args[0] == "update" {
		scope := config.UpdateAll
		if len(args) == 2 {
			switch args[1] {
			case "software":
				scope = config.UpdateSoftware
			case "repositories":
				scope = config.UpdateRepositories
			}
		}
		return config.NewUpdater(paths, os.Stdout, version).Update(scope)
	}
	var machine config.Machine
	restorePending := false
	if len(args) > 0 && args[0] == "bootstrap" {
		machine, restorePending, err = config.MaterializeRepository(paths, args[1], os.Stdout, os.Stderr)
		if err == nil {
			err = config.InstallCurrent(paths, version, os.Stdout)
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
		case "prune":
			return prune(paths, machine, args[1:])
		case "--status":
			return status(paths, machine, runner)
		case "--apply":
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
			return config.NewSnapshotter(paths, machine, os.Stdout).Save()
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

type pruneOptions struct {
	dryRun bool
	yes    bool
}

func parsePruneOptions(args []string) (pruneOptions, error) {
	var options pruneOptions
	for _, argument := range args {
		switch argument {
		case "--dry-run":
			if options.dryRun {
				return pruneOptions{}, errors.New("usage: config prune [--dry-run | --yes]")
			}
			options.dryRun = true
		case "--yes":
			if options.yes {
				return pruneOptions{}, errors.New("usage: config prune [--dry-run | --yes]")
			}
			options.yes = true
		default:
			return pruneOptions{}, errors.New("usage: config prune [--dry-run | --yes]")
		}
	}
	if options.dryRun && options.yes {
		return pruneOptions{}, errors.New("usage: config prune [--dry-run | --yes]")
	}
	return options, nil
}

func prune(paths config.Paths, machine config.Machine, args []string) error {
	options, err := parsePruneOptions(args)
	if err != nil {
		return err
	}
	pruner := config.NewPruner(paths, machine, os.Stdout)
	plan, err := pruner.Plan()
	if err != nil {
		return err
	}
	config.WritePrunePlan(os.Stdout, plan)
	if plan.Empty() || options.dryRun {
		return nil
	}
	if !options.yes {
		if !terminal(os.Stdin) || !terminal(os.Stdout) {
			fmt.Fprintln(os.Stdout, "\nNo changes made; run config prune --yes to apply this plan.")
			return nil
		}
		confirmed, err := confirmPrune(os.Stdin, os.Stdout)
		if err != nil {
			return err
		}
		if !confirmed {
			fmt.Fprintln(os.Stdout, "No changes made.")
			return nil
		}
	}
	return pruner.Apply(plan)
}

func confirmPrune(in io.Reader, out io.Writer) (bool, error) {
	fmt.Fprint(out, "\nPrune these items? [y/N] ")
	answer, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
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

// checkArguments decides every argument error before Config reads anything on
// disk, so a typo is reported as a typo whatever state the Mac is in.
func checkArguments(args []string) error {
	if len(args) == 0 {
		return nil
	}
	switch args[0] {
	case "path", "install", "--status", "--snapshot":
		if len(args) != 1 {
			return fmt.Errorf("usage: config %s", args[0])
		}
	case "update":
		if len(args) > 2 || (len(args) == 2 && args[1] != "software" && args[1] != "repositories") {
			return errors.New("usage: config update [software | repositories]")
		}
	case "bootstrap":
		if len(args) != 2 {
			return errors.New("usage: config bootstrap <repository>")
		}
	case "--apply":
		if len(args) != 2 {
			return errors.New("usage: config --apply <plan>")
		}
	case "prune":
		// prune reads its own flags.
	default:
		if strings.HasPrefix(args[0], "-") {
			return fmt.Errorf("unknown option: %s", args[0])
		}
		return errors.New("unknown command; run config help for available commands")
	}
	return nil
}
