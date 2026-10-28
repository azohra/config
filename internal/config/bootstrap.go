package config

import (
	"errors"
	"fmt"
	"io"
)

const (
	restoreMacOSStep = "resource/" + macOSID
)

type freshRestoreStep struct {
	id   string
	name string
	run  func() error
}

// Restore starts or resumes native restoration in a checkout acquired by the
// machine's setup tool. Existing completed restores remain complete.
func Restore(paths Paths, machine Machine, out io.Writer) error {
	if _, err := validateMaterializedRepository(paths, machine.Repository.URL); err != nil {
		return err
	}
	_, found, err := checkoutRestoreID(paths)
	if err != nil {
		return err
	}
	if !found {
		if _, err := beginRestore(paths, paths, machine); err != nil {
			return err
		}
	}
	_, pending, err := pendingRestore(paths, machine, out)
	if err != nil {
		return err
	}
	if !pending {
		return nil
	}
	return RestorePending(paths, machine, out)
}

// RestorePending resumes the restore attached to this managed checkout.
func RestorePending(paths Paths, machine Machine, out io.Writer) error {
	progress, pending, err := pendingRestore(paths, machine, out)
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

// restorePending attempts every unfinished capability. A missing provider or
// an unreadable backup is reported without stopping independent resources.
// Successful steps are recorded before the next one starts, so a later
// bootstrap retries only the work that remains.
func restorePending(applier Applier, progress *restoreProgress) error {
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
// capabilities. A native setup driver runs these before applying dependent defaults.
func freshRestoreSteps(applier Applier) []freshRestoreStep {
	steps := make([]freshRestoreStep, 0, len(applier.Machine.Preferences)+6)
	if len(macOSFacts(applier.Machine)) > 0 {
		steps = append(steps, freshRestoreStep{
			id: restoreMacOSStep, name: macOSName,
			run: func() error {
				applier.Log.Section(macOSName)
				applier.convergeMacOS()
				return nil
			},
		})
	}

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

	return steps
}
