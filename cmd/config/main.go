package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"syscall"

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
  config update [software | repositories] [--dry-run | --yes]
  config prune [--dry-run | --yes]
  config bootstrap <repository>

Config inspects first, proposes a plan, requires explicit choices for app state,
then offers to commit and push the resulting snapshot.

Bootstrap clones an authenticated Git repository into Config's managed storage,
installs the permanent Config command, and resumes restore until every declared
step has completed. Path prints the managed repository's canonical location.

Update previews what can be discovered before making changes. Without a
terminal it is preview-only; --yes applies after checking again. Config updates
itself through a separate cache-owned adapter before reading the machine
contract. With no selection it updates declared tools, packages, agent skills,
and clean repositories. Software omits repositories; repositories runs only
that phase.

Prune previews stale Mise inventory, spent Mise downloads, and Config-owned
local state. Without a terminal it is preview-only; --yes applies the exact
plan after checking it again.`

var version = "dev"

func main() {
	out := io.Writer(os.Stdout)
	var events *config.OperationEventWriter
	if os.Getenv(config.OperationEventsEnv) == "1" {
		events = config.NewOperationEventWriter(os.Stdout)
		out = events
		_ = os.Unsetenv(config.OperationEventsEnv)
	}
	if err := run(out); err != nil {
		if events != nil {
			_ = events.OperationEvent(config.OperationEvent{Kind: config.OperationError, Text: "error: " + err.Error()})
			_ = events.Close()
		} else {
			fmt.Fprintln(os.Stderr, "error:", err)
		}
		os.Exit(1)
	}
	if events != nil {
		_ = events.Close()
	}
}

const reopenResultEnv = "AZOHRA_CONFIG_REOPEN_RESULT"

func run(out io.Writer) error {
	args := os.Args[1:]
	if len(args) == 1 && args[0] == "--version" {
		fmt.Fprintln(out, "config", version)
		return nil
	}
	if len(args) > 0 && slices.Contains([]string{"-h", "--help", "help"}, args[0]) {
		if len(args) != 1 {
			return errors.New("help takes no arguments")
		}
		fmt.Fprintln(out, usage)
		return nil
	}
	paths, err := config.NewPaths("")
	if err != nil {
		return err
	}
	defer config.OnInterrupt(os.Stderr)()
	// Deciding these after the machine document loads reported a typo on a Mac
	// with no managed checkout as a missing repository, and prescribed
	// bootstrap.
	if err := checkArguments(args); err != nil {
		return err
	}
	// Every command that writes the managed checkout takes its lock. The
	// terminal interface does not: it launches these same commands as
	// children, and each one takes the lock for the work it does. Neither
	// does install, which writes Config's own command and nothing in the
	// checkout — update holds this lock and runs the acquired release's
	// install as a child, so contending here would deadlock the update
	// against itself.
	if len(args) > 0 && slices.Contains(
		[]string{"update", "bootstrap", "prune", "--apply", "--snapshot", "--run-update"}, args[0]) {
		release, lockErr := config.LockCheckout(paths)
		if lockErr != nil {
			return lockErr
		}
		defer release()
	}
	if len(args) > 0 && args[0] == "path" {
		fmt.Fprintln(out, paths.Root)
		return nil
	}
	// A release calls this on its successor: the running Config acquires a
	// newer executable and runs that binary's own install. The spelling is a
	// contract with every Config already on a Mac, so it stays bare while
	// Config's other internal arguments carry a leading --.
	if len(args) > 0 && args[0] == "install" {
		return config.InstallCurrent(paths, version, out)
	}
	if len(args) > 0 && args[0] == "update" {
		return update(paths, args[1:], out)
	}
	if len(args) > 0 && args[0] == "--run-update" {
		return runConfirmedUpdate(paths, args[1:], out)
	}
	var machine config.Machine
	restorePending := false
	if len(args) > 0 && args[0] == "bootstrap" {
		machine, restorePending, err = config.MaterializeRepository(paths, args[1], out, out)
		if err == nil {
			err = config.InstallCurrent(paths, version, out)
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
		return config.RestorePending(paths, machine, out)
	}
	if len(args) > 0 {
		switch args[0] {
		case "prune":
			return prune(paths, machine, args[1:], out)
		case "--status":
			return status(paths, machine, runner, out)
		case "--apply":
			selections, err := config.DecodeSelections(args[1])
			if err != nil {
				return err
			}
			report := config.NewInspector(paths, machine, runner).Inspect()
			if err := config.ValidateSelections(report, selections); err != nil {
				return err
			}
			return config.NewApplier(paths, machine, out).Apply(selections)
		case "--snapshot":
			return config.NewSnapshotter(paths, machine, out).Save()
		}
	}
	if !terminal(os.Stdin) || !terminal(os.Stdout) {
		return status(paths, machine, runner, out)
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	reopenResult := os.Getenv(reopenResultEnv) == "1"
	_ = os.Unsetenv(reopenResultEnv)
	final, err := tea.NewProgram(ui.New(paths, machine, executable, version, reopenResult)).Run()
	if err != nil {
		return err
	}
	model, ok := final.(ui.Model)
	if !ok || !model.RestartRequested() {
		return nil
	}
	environment := appendWithoutEnvironment(os.Environ(), reopenResultEnv)
	environment = append(environment, reopenResultEnv+"=1")
	return syscall.Exec(executable, []string{executable}, environment)
}

func appendWithoutEnvironment(environment []string, name string) []string {
	prefix := name + "="
	filtered := make([]string, 0, len(environment))
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

type updateOptions struct {
	scope  config.UpdateScope
	dryRun bool
	yes    bool
}

func parseUpdateOptions(args []string) (updateOptions, error) {
	options := updateOptions{scope: config.UpdateAll}
	scopeSeen := false
	for _, argument := range args {
		switch argument {
		case "software", "repositories":
			if scopeSeen {
				return updateOptions{}, errors.New("usage: config update [software | repositories] [--dry-run | --yes]")
			}
			scopeSeen = true
			if argument == "software" {
				options.scope = config.UpdateSoftware
			} else {
				options.scope = config.UpdateRepositories
			}
		case "--dry-run":
			if options.dryRun {
				return updateOptions{}, errors.New("usage: config update [software | repositories] [--dry-run | --yes]")
			}
			options.dryRun = true
		case "--yes":
			if options.yes {
				return updateOptions{}, errors.New("usage: config update [software | repositories] [--dry-run | --yes]")
			}
			options.yes = true
		default:
			return updateOptions{}, errors.New("usage: config update [software | repositories] [--dry-run | --yes]")
		}
	}
	if options.dryRun && options.yes {
		return updateOptions{}, errors.New("usage: config update [software | repositories] [--dry-run | --yes]")
	}
	return options, nil
}

func update(paths config.Paths, args []string, out io.Writer) error {
	options, err := parseUpdateOptions(args)
	if err != nil {
		return err
	}
	updater := config.NewUpdater(paths, out, version)
	plan, err := updater.Plan(options.scope)
	if err != nil {
		return err
	}
	config.WriteUpdatePlan(out, plan)
	if plan.Blocked {
		return errors.New("Config: update is unavailable")
	}
	if !plan.HasWork() || options.dryRun {
		return nil
	}
	if !options.yes {
		if !terminal(os.Stdin) || !terminal(os.Stdout) {
			command := "config update"
			switch options.scope {
			case config.UpdateSoftware:
				command += " software"
			case config.UpdateRepositories:
				command += " repositories"
			}
			fmt.Fprintf(out, "\nNo changes made; run %s --yes to apply this plan.\n", command)
			return nil
		}
		confirmed, err := confirm(os.Stdin, out, "Run this update?")
		if err != nil {
			return err
		}
		if !confirmed {
			fmt.Fprintln(out, "No changes made.")
			return nil
		}
	}
	return updater.Apply(plan)
}

func runConfirmedUpdate(paths config.Paths, args []string, out io.Writer) error {
	if len(args) != 2 {
		return errors.New("invalid internal update request")
	}
	options, err := parseUpdateOptions([]string{args[0]})
	if err != nil || options.scope == config.UpdateAll {
		return errors.New("invalid internal update scope")
	}
	updater := config.NewUpdater(paths, out, version)
	plan, err := updater.Plan(options.scope)
	if err != nil {
		return err
	}
	if plan.Fingerprint() != args[1] {
		return errors.New("update plan changed; return to the review screen and check again")
	}
	return updater.Apply(plan)
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

func prune(paths config.Paths, machine config.Machine, args []string, out io.Writer) error {
	options, err := parsePruneOptions(args)
	if err != nil {
		return err
	}
	pruner := config.NewPruner(paths, machine, out)
	plan, err := pruner.Plan()
	if err != nil {
		return err
	}
	config.WritePrunePlan(out, plan)
	if plan.Empty() || options.dryRun {
		return nil
	}
	if !options.yes {
		if !terminal(os.Stdin) || !terminal(os.Stdout) {
			fmt.Fprintln(out, "\nNo changes made; run config prune --yes to apply this plan.")
			return nil
		}
		confirmed, err := confirmPrune(os.Stdin, out)
		if err != nil {
			return err
		}
		if !confirmed {
			fmt.Fprintln(out, "No changes made.")
			return nil
		}
	}
	return pruner.Apply(plan)
}

func confirmPrune(in io.Reader, out io.Writer) (bool, error) {
	return confirm(in, out, "Prune these items?")
}

func confirm(in io.Reader, out io.Writer, prompt string) (bool, error) {
	fmt.Fprintf(out, "\n%s [y/N] ", prompt)
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
func status(paths config.Paths, machine config.Machine, runner config.Runner, out io.Writer) error {
	report := config.NewInspector(paths, machine, runner).Inspect()
	config.WriteStatus(out, report)
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
		_, err := parseUpdateOptions(args[1:])
		return err
	case "bootstrap":
		if len(args) != 2 {
			return errors.New("usage: config bootstrap <repository>")
		}
	case "--apply":
		if len(args) != 2 {
			return errors.New("usage: config --apply <plan>")
		}
	case "--run-update":
		if len(args) != 3 {
			return errors.New("invalid internal update request")
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
