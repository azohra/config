package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// The glyphs this package writes and the UI colors: the Logger's step lines,
// a resource's state, a status row. Spelled here so producer and consumer
// change together; a glyph inside rendered UI copy is that copy's own.
const (
	GlyphOK     = "✓"
	GlyphInfo   = "→"
	GlyphWarn   = "!"
	GlyphError  = "✗"
	GlyphChoice = "↔"

	stepIndent = "  "
)

type Logger struct {
	Out io.Writer
}

func (l Logger) line(symbol, message string) {
	fmt.Fprintf(l.Out, "%s%s %s\n", stepIndent, symbol, message)
}

func (l Logger) Section(name string)  { fmt.Fprintf(l.Out, "\n%s\n", name) }
func (l Logger) OK(message string)    { l.line(GlyphOK, message) }
func (l Logger) Info(message string)  { l.line(GlyphInfo, message) }
func (l Logger) Warn(message string)  { l.line(GlyphWarn, message) }
func (l Logger) Error(message string) { l.line(GlyphError, message) }

// StepGlyph reports the glyph of a Logger step line, and accepts only the
// glyphs a Logger writes. A reader that presents this package's output — the
// app's operation pane — classifies lines through here rather than respelling
// the shape the Logger just wrote.
func StepGlyph(line string) (string, bool) {
	rest, indented := strings.CutPrefix(line, stepIndent)
	if !indented {
		return "", false
	}
	glyph, _, spaced := strings.Cut(rest, " ")
	if !spaced {
		return "", false
	}
	switch glyph {
	case GlyphOK, GlyphInfo, GlyphWarn, GlyphError:
		return glyph, true
	}
	return "", false
}

type Applier struct {
	Paths   Paths
	Machine Machine
	Runner  Runner
	Live    LiveRunner
	Log     Logger
	Bidir   Bidirectional
}

func NewApplier(paths Paths, machine Machine, out io.Writer) Applier {
	runner := NewMachineRunner(paths)
	return Applier{
		Paths:   paths,
		Machine: machine,
		Runner:  runner,
		Live:    NewMachineLiveRunner(paths),
		Log:     Logger{Out: out},
		Bidir:   NewBidirectional(paths, runner),
	}
}

type advisoryError struct{ message string }

func (e advisoryError) Error() string { return e.message }

func (e Applier) Apply(selections []Selection) error {
	actions := make(map[string]Action)
	for _, selection := range selections {
		if selection.Action != Skip {
			actions[selection.ID] = selection.Action
		}
	}
	type step struct {
		id   string
		name string
		fn   func(Action) error
	}
	steps := []step{
		{setupID, setupName, func(Action) error { return e.applyMise() }},
	}
	for _, preference := range e.Machine.Preferences {
		steps = append(steps, step{preference.ID, preference.Name, func(action Action) error {
			return e.reconcilePreference(preference, action)
		}})
	}
	if e.Machine.ChromePWAs {
		steps = append(steps, step{chromePWAsID, chromePWAsName, e.reconcileChromePWAs})
	}
	if e.Machine.Dock {
		steps = append(steps, step{dockID, dockName, e.reconcileDock})
	}
	var failures []error
	converged := make(map[string]bool)
	for _, step := range steps {
		action, selected := actions[step.id]
		if !selected {
			continue
		}
		e.Log.Section(step.name)
		if err := step.fn(action); err != nil {
			var advisory advisoryError
			if errors.As(err, &advisory) {
				e.Log.Warn(advisory.message)
				continue
			}
			e.Log.Error(err.Error())
			failures = append(failures, fmt.Errorf("%s: %w", step.name, err))
			continue
		}
		converged[step.id] = true
	}
	// Establish baselines whenever both sides agree. A step that converged
	// must verify; one that failed or declined already said why, and cannot
	// leave the two sides agreeing.
	type baselineStep struct {
		id   string
		name string
		mark func() error
	}
	var baselines []baselineStep
	if e.Machine.ChromePWAs {
		baselines = append(baselines, baselineStep{chromePWAsID, chromePWAsName, e.Bidir.MarkChromePWAsIfCurrent})
	}
	if e.Machine.Dock {
		baselines = append(baselines, baselineStep{dockID, dockName, e.Bidir.MarkDockIfCurrent})
	}
	for _, baseline := range baselines {
		if err := baseline.mark(); err != nil && converged[baseline.id] {
			failures = append(failures, fmt.Errorf("%s: %w", baseline.name, err))
		}
	}
	return errors.Join(failures...)
}

func (e Applier) reconcilePreference(preference PreferenceBackup, action Action) error {
	if action != Capture {
		return nil
	}
	if err := preference.Backup(e.Paths, e.Runner); err != nil {
		return err
	}
	e.Log.OK("current settings backed up")
	return nil
}

// RestorePreferences is intentionally separate from normal Apply. Only the
// fresh bootstrap path calls it; an established Mac always owns current app
// preferences.
func (e Applier) RestorePreferences() error {
	for _, preference := range e.Machine.Preferences {
		e.Log.Section(preference.Name)
		if err := e.restorePreference(preference); err != nil {
			return err
		}
	}
	return nil
}

func (e Applier) restorePreference(preference PreferenceBackup) error {
	data, err := os.ReadFile(preference.snapshotPath(e.Paths))
	if errors.Is(err, os.ErrNotExist) {
		e.Log.Warn("no saved settings to restore")
		return nil
	}
	if err != nil {
		return fmt.Errorf("read saved settings: %w", err)
	}
	if _, err := decodePlist(data); err != nil {
		return fmt.Errorf("validate saved settings: %w", err)
	}
	// Spotlight lookup: answers "is this bundle installed" without launching
	// the app or revealing it in Finder.
	installed := run(e.Runner, "mdfind", "kMDItemCFBundleIdentifier == '"+preference.Bundle+"'")
	if installed.Output() == "" {
		e.Log.Warn(preference.Name + " is not installed")
		return nil
	}
	running, err := preferenceIsRunning(e.Runner, preference)
	if err != nil {
		return err
	}
	if running {
		e.Log.Info("quitting " + preference.Name + " before import")
		if err := e.Live.Command("osascript", "-e", `tell application id "`+preference.Bundle+`" to quit`); err != nil {
			return err
		}
		for range 20 {
			active, runningErr := preferenceIsRunning(e.Runner, preference)
			if runningErr != nil {
				return runningErr
			}
			if !active {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		active, runningErr := preferenceIsRunning(e.Runner, preference)
		if runningErr != nil {
			return runningErr
		}
		if active {
			return fmt.Errorf("%s did not quit; saved settings were not imported", preference.Name)
		}
	}
	if err := e.Live.Command("defaults", "import", preference.Domain, preference.snapshotPath(e.Paths)); err != nil {
		return err
	}
	e.Log.OK("saved settings imported")
	if running {
		if err := e.Live.Command("open", "-b", preference.Bundle); err != nil {
			return err
		}
		e.Log.OK(preference.Name + " relaunched")
	}
	return nil
}

// Mise's declared packages establish the commands every later phase consumes.
func (e Applier) applyMise() error {
	if !e.Runner.Exists("mise") {
		return fmt.Errorf("mise unavailable at %s", misePath(e.Paths))
	}
	// Dirty declared repositories remain visible in status. Skipping them here
	// lets independent machine resources converge without touching local work.
	if err := e.Live.Command("mise", "bootstrap", "--yes", "--skip-dirty"); err != nil {
		return err
	}
	changed, err := e.converge(miseFacts(e.Machine))
	if err != nil {
		return err
	}
	if changed > 0 {
		e.Log.OK("declared macOS settings applied")
	}
	e.Log.OK("mise bootstrap state current")
	return nil
}

func (e Applier) reconcileDock(action Action) error {
	switch action {
	case Capture:
		if err := e.Bidir.CaptureDock(); err != nil {
			return err
		}
		e.Log.OK("live layout captured")
		return nil
	case Apply:
		return e.applyDock()
	default:
		return nil
	}
}

func (e Applier) applyDock() error {
	saved, all, _, hasSaved, err := e.Bidir.dockSaved()
	if err != nil {
		return err
	}
	if !hasSaved {
		return advisoryError{"no saved Dock layout; Dock left untouched"}
	}
	for _, app := range all {
		if info, statErr := os.Stat(app); statErr != nil || !info.IsDir() {
			return advisoryError{fmt.Sprintf("%s is unavailable; Dock left untouched", filepath.Base(app))}
		}
	}
	live, liveApps, _ := e.Bidir.dockLive()
	if string(saved) == string(live) {
		e.Log.OK("layout already current")
		return nil
	}
	operations := planDock(all, liveApps)
	for _, operation := range operations {
		args := []string{"--" + operation.Action, operation.Path}
		if operation.Position > 0 {
			args = append(args, "--position", strconv.Itoa(operation.Position))
		}
		args = append(args, "--no-restart")
		if err := e.Live.Command("dockutil", args...); err != nil {
			return err
		}
	}
	_ = e.Live.Command("killall", "Dock")
	e.Log.OK(FormatCount(len(operations), "Dock change", "Dock changes") + " applied")
	return nil
}
