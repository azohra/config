package config

import (
	"errors"
	"fmt"
	"io"
	"os"
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

func (l Logger) line(kind OperationEventKind, message string) {
	if sink, ok := l.Out.(operationEventSink); ok {
		_ = sink.OperationEvent(OperationEvent{Kind: kind, Text: message})
		return
	}
	glyph, _ := kind.Glyph()
	fmt.Fprintf(l.Out, "%s%s %s\n", stepIndent, glyph, message)
}

func (l Logger) Section(name string) {
	if sink, ok := l.Out.(operationEventSink); ok {
		_ = sink.OperationEvent(OperationEvent{Kind: OperationSection, Text: name})
		return
	}
	fmt.Fprintf(l.Out, "\n%s\n", name)
}
func (l Logger) OK(message string)    { l.line(OperationOK, message) }
func (l Logger) Info(message string)  { l.line(OperationInfo, message) }
func (l Logger) Warn(message string)  { l.line(OperationWarn, message) }
func (l Logger) Error(message string) { l.line(OperationError, message) }
func (l Logger) Version(version string) {
	if sink, ok := l.Out.(operationEventSink); ok {
		_ = sink.OperationEvent(OperationEvent{Kind: OperationVersion, Text: version})
	}
}

type Applier struct {
	Paths           Paths
	Machine         Machine
	Runner          Runner
	Live            LiveRunner
	Mise            Runner
	MiseLive        LiveRunner
	Skills          Runner
	SkillsLive      commandRunner
	InstallMise     func() error
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
	installer := testedMiseInstaller(paths)
	live := newMachineLiveRunner(paths)
	live.Stdout, live.Stderr = out, out
	miseLive := newMiseLiveRunner(paths)
	miseLive.Stdout, miseLive.Stderr = out, out
	skillsLive := newAgentSkillsLiveRunner(paths)
	skillsLive.Stdout, skillsLive.Stderr = out, out
	return Applier{
		Paths:           paths,
		Machine:         machine,
		Runner:          runner,
		Live:            live,
		Mise:            NewMiseRunner(paths),
		MiseLive:        miseLive,
		Skills:          newAgentSkillsRunner(paths),
		SkillsLive:      skillsLive,
		InstallMise:     installer.Install,
		FinderFavorites: newFinderFavoritesStore(),
		Log:             Logger{Out: out},
		Bidir:           newBidirectional(paths),
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
	var steps []step
	miseReady := false
	if len(e.Machine.RepositoryHooks) > 0 {
		steps = append(steps, step{repositoryHooksID, repositoryHooksName, e.reconcileRepositoryHooks})
	}
	if len(macOSFacts(e.Machine)) > 0 {
		steps = append(steps, step{macOSID, macOSName, func(Action) error {
			e.convergeMacOS()
			return nil
		}})
	}
	if e.Machine.Mise {
		steps = append(steps, step{miseID, miseName, func(Action) error {
			err := e.prepareMise()
			miseReady = err == nil
			return err
		}})
	}
	if e.Machine.AgentSkills != nil {
		steps = append(steps, step{agentSkillsID, agentSkillsName, func(Action) error {
			return e.agentSkillManager().Reconcile()
		}})
	}
	if e.Machine.MCPServers != nil {
		steps = append(steps, step{mcpServersID, mcpServersName, func(Action) error {
			return e.mcpServerManager().Reconcile()
		}})
	}
	if e.Machine.FinderFavorites {
		steps = append(steps, step{finderFavoritesID, finderFavoritesName, e.reconcileFinderFavorites})
	}
	for _, preference := range e.Machine.Preferences {
		steps = append(steps, step{preference.ID, preference.Name, func(action Action) error {
			return e.reconcilePreference(preference, action)
		}})
	}
	if e.Machine.ChromePWAs {
		steps = append(steps, step{chromePWAsID, chromePWAsName, e.reconcileChromePWAs})
	}
	if e.Machine.Mise {
		steps = append(steps, step{miseID, "Mise defaults", func(Action) error {
			if !miseReady {
				return advisoryError{"Mise setup did not finish; defaults remain pending"}
			}
			return e.finishMise()
		}})
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
	if e.Machine.FinderFavorites {
		baselines = append(baselines, baselineStep{finderFavoritesID, finderFavoritesName, func() error {
			return e.Bidir.MarkFinderFavoritesIfCurrent(e.FinderFavorites)
		}})
	}
	if e.Machine.ChromePWAs {
		baselines = append(baselines, baselineStep{chromePWAsID, chromePWAsName, e.Bidir.MarkChromePWAsIfCurrent})
	}
	for _, baseline := range baselines {
		if err := baseline.mark(); err != nil && converged[baseline.id] {
			failures = append(failures, fmt.Errorf("%s: %w", baseline.name, err))
		}
	}
	return errors.Join(failures...)
}

func (e Applier) agentSkillManager() agentSkillManager {
	return agentSkillManager{
		Paths: e.Paths, Skills: *e.Machine.AgentSkills, Probe: e.Skills,
		Live: e.SkillsLive, Log: e.Log,
	}
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
		// Whether to relaunch is a fact about the Mac before the quit, and the
		// quit destroys it. Without this, a run killed after quitting leaves
		// the application closed and the next restore, seeing it closed, never
		// opens it again.
		if err := setMarker(e.Paths, relaunchMarker(preference.Bundle)); err != nil {
			return err
		}
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
	if running || markerSet(e.Paths, relaunchMarker(preference.Bundle)) {
		if err := e.Live.Command("open", "-b", preference.Bundle); err != nil {
			return err
		}
		clearMarker(e.Paths, relaunchMarker(preference.Bundle))
		e.Log.OK(preference.Name + " relaunched")
	}
	return nil
}

func (e Applier) prepareMise() error {
	if err := ensureTestedMise(e.Mise, e.InstallMise); err != nil {
		return err
	}
	if err := ensureMiseConfigBinding(e.Paths); err != nil {
		return err
	}
	if len(e.Machine.RepositoryHooks) > 0 {
		if _, err := e.applyRepositoryHookTargets(false); err != nil {
			return fmt.Errorf("prepare repository hook template: %w", err)
		}
	}
	// Dirty declared repositories remain visible in status. Skipping them here
	// lets independent machine resources converge without touching local work.
	live := e.MiseLive
	if len(e.Machine.RepositoryHooks) > 0 {
		live.Environment = append(live.Environment, repositoryHookTemplateEnvironment(e.Paths)...)
	}
	if err := live.Command("mise", "bootstrap", "--yes", "--skip-dirty", "--skip", "macos-defaults,task,final-hook"); err != nil {
		return err
	}
	if len(e.Machine.RepositoryHooks) > 0 {
		changed, err := e.applyRepositoryHookTargets(true)
		if err != nil {
			return fmt.Errorf("reconcile repository hooks: %w", err)
		}
		if changed > 0 {
			e.Log.OK(FormatCount(changed, "hook copy refreshed", "hook copies refreshed"))
		}
	}
	e.Log.OK("mise tools and resources prepared")
	return nil
}

// convergeMacOS applies the declared native settings and reports what it could
// not do without failing the resource. macOS records nothing in the repository,
// so one unreadable probe must not stop a restore.
func (e Applier) convergeMacOS() {
	changed, err := e.converge(macOSFacts(e.Machine))
	if changed > 0 {
		e.Log.OK("declared macOS settings applied")
	}
	if err != nil {
		e.Log.Warn(err.Error())
	}
}

// finishMise applies defaults after native app restoration, so Dock declarations
// can refer to Chrome PWA bundles. Keep the bootstrap task and final hook last.
func (e Applier) finishMise() error {
	if err := requireTestedMise(e.Mise); err != nil {
		return err
	}
	if err := requireMiseConfigBinding(e.Paths); err != nil {
		return err
	}
	if err := e.MiseLive.Command("mise", "bootstrap", "--yes", "--only", "macos-defaults,task,final-hook"); err != nil {
		return err
	}
	e.Log.OK("mise defaults and final hooks applied")
	return nil
}
