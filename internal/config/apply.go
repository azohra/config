package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
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
	Paths           Paths
	Machine         Machine
	Runner          Runner
	Live            LiveRunner
	FinderFavorites finderFavoritesStore
	Log             Logger
	Bidir           Bidirectional
	// QuitPoll is how long to wait between asking whether an application has
	// quit. Zero means the default.
	QuitPoll time.Duration
}

// quitAttempts and defaultQuitPoll give an application ten seconds to close
// before restore refuses to import over it.
const (
	quitAttempts    = 20
	defaultQuitPoll = 500 * time.Millisecond
)

func NewApplier(paths Paths, machine Machine, out io.Writer) Applier {
	runner := NewMachineRunner(paths)
	return Applier{
		Paths:           paths,
		Machine:         machine,
		Runner:          runner,
		Live:            NewMachineLiveRunner(paths),
		FinderFavorites: newFinderFavoritesStore(),
		Log:             Logger{Out: out},
		Bidir:           NewBidirectional(paths, runner),
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
	if e.Machine.FinderFavorite != nil {
		steps = append(steps, step{finderFavoriteID, finderFavoriteName, e.reconcileFinderFavorite})
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
	if installed.Err != nil {
		return fmt.Errorf("find %s: %w", preference.Name, installed.Failure())
	}
	if installed.Output() == "" {
		return advisoryError{preference.Name + " is not installed; restore remains pending"}
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
		poll := e.QuitPoll
		if poll == 0 {
			poll = defaultQuitPoll
		}
		for range quitAttempts {
			active, runningErr := preferenceIsRunning(e.Runner, preference)
			if runningErr != nil {
				return runningErr
			}
			if !active {
				break
			}
			time.Sleep(poll)
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
	if err := requireTestedMise(e.Runner); err != nil {
		return err
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
	_, all, _, hasSaved, err := e.Bidir.dockSaved()
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
	store := e.Bidir.dockStore()
	original, err := store.Read()
	if err != nil {
		return fmt.Errorf("read Dock layout: %w", err)
	}
	liveApps := dockAppPaths(original)
	if slices.Equal(all, liveApps) {
		e.Log.OK("layout already current")
		return nil
	}
	updated, err := reconcileDockTiles(original, all)
	if err != nil {
		return err
	}
	if err := store.Write(updated); err != nil {
		return fmt.Errorf("write Dock layout: %w", err)
	}
	verified, err := store.Read()
	if err != nil {
		return restoreDockAfterFailure(store, original, fmt.Errorf("read applied Dock layout: %w", err))
	}
	if actual := dockAppPaths(verified); !slices.Equal(actual, all) {
		verification := fmt.Errorf("applied Dock apps are %v; expected %v", actual, all)
		return restoreDockAfterFailure(store, original, verification)
	}
	if opaque := dockOpaqueTiles(verified); !reflect.DeepEqual(opaque, dockOpaqueTiles(original)) {
		return restoreDockAfterFailure(store, original, errors.New("applied Dock layout changed non-app tiles"))
	}
	if err := e.Live.Command("killall", "Dock"); err != nil {
		return restoreDockAfterFailure(store, original, fmt.Errorf("restart Dock: %w", err))
	}
	e.Log.OK("saved layout restored")
	return nil
}
