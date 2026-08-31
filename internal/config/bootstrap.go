package config

import (
	"errors"
	"fmt"
	"io"
)

const restoreSetupStep = "setup"

type freshRestoreStep struct {
	id   string
	name string
	run  func() error
}

// RestorePending resumes the restore attached to this managed checkout.
func RestorePending(paths Paths, machine Machine, out io.Writer) error {
	progress, pending, err := pendingRestore(paths, machine)
	if err != nil {
		return err
	}
	if !pending {
		return errors.New("managed checkout has no pending bootstrap restore")
	}
	err = restorePending(NewApplier(paths, machine, out), &progress)
	if err == nil {
		err = progress.finish(machine)
	}
	fmt.Fprintln(out)
	WriteStatus(out, NewInspector(paths, machine, NewMachineRunner(paths)).Inspect())
	return err
}

// restorePending converges setup, then restores each unfinished capability.
// A backup that cannot be read is reported without stopping the capabilities
// beside it. Successful steps are recorded before the next one starts, so a
// later bootstrap retries only the work that remains.
func restorePending(applier Applier, progress *restoreProgress) error {
	// The one deliberate stop. Mise installs the applications every later
	// step restores into, so nothing below can converge without it.
	if !progress.done(restoreSetupStep) {
		if err := applier.Apply([]Selection{{ID: setupID, Action: Apply}}); err != nil {
			return err
		}
		if err := progress.markDone(restoreSetupStep, applier.Machine); err != nil {
			return fmt.Errorf("record machine setup restore: %w", err)
		}
	}

	var failures []error
	for _, step := range freshRestoreSteps(applier) {
		if progress.done(step.id) {
			continue
		}
		if err := step.run(); err != nil {
			var advisory advisoryError
			if errors.As(err, &advisory) {
				applier.Log.Warn(advisory.message)
			}
			failures = append(failures, fmt.Errorf("%s: %w", step.name, err))
			continue
		}
		if err := progress.markDone(step.id, applier.Machine); err != nil {
			failures = append(failures, fmt.Errorf("record %s restore: %w", step.name, err))
			return errors.Join(failures...)
		}
	}
	return errors.Join(failures...)
}

// freshRestoreSteps is the ordered extension point for Config-owned restore
// capabilities. Chrome PWAs precede the Dock so saved shortcuts exist before
// a declared Dock layout is rebuilt.
func freshRestoreSteps(applier Applier) []freshRestoreStep {
	steps := make([]freshRestoreStep, 0, len(applier.Machine.Preferences)+3)
	if applier.Machine.FinderFavorites {
		steps = append(steps, freshRestoreStep{
			id:   "resource/" + finderFavoritesID,
			name: finderFavoritesName,
			run: func() error {
				applier.Log.Section(finderFavoritesName)
				_, _, _, hasSaved, err := applier.Bidir.finderFavoritesSaved()
				if err != nil {
					return err
				}
				if !hasSaved {
					return nil
				}
				if err := applier.applyFinderFavorites(); err != nil {
					return err
				}
				return applier.Bidir.MarkFinderFavoritesIfCurrent(applier.FinderFavorites)
			},
		})
	}
	for _, preference := range applier.Machine.Preferences {
		preference := preference
		steps = append(steps, freshRestoreStep{
			id:   "preference/" + preference.ID,
			name: preference.Name,
			run: func() error {
				applier.Log.Section(preference.Name)
				return applier.restorePreference(preference)
			},
		})
	}
	if applier.Machine.ChromePWAs {
		steps = append(steps, freshRestoreStep{
			id:   "resource/" + chromePWAsID,
			name: chromePWAsName,
			run: func() error {
				applier.Log.Section(chromePWAsName)
				_, _, hasSaved, err := applier.Bidir.chromePWASaved()
				if err != nil {
					return err
				}
				if !hasSaved {
					return nil
				}
				if err := applier.applyChromePWAs(); err != nil {
					return err
				}
				return applier.Bidir.MarkChromePWAsIfCurrent()
			},
		})
	}
	if applier.Machine.Dock {
		steps = append(steps, freshRestoreStep{
			id:   "resource/" + dockID,
			name: dockName,
			run: func() error {
				applier.Log.Section(dockName)
				_, _, _, hasSaved, err := applier.Bidir.dockSaved()
				if err != nil {
					return err
				}
				if !hasSaved {
					return nil
				}
				if err := applier.applyDock(); err != nil {
					return err
				}
				return applier.Bidir.MarkDockIfCurrent()
			},
		})
	}
	return steps
}
