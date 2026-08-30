package config

import (
	"errors"
	"fmt"
	"io"
)

// RestoreFresh is the fresh-clone restore sequence.
func RestoreFresh(paths Paths, machine Machine, out io.Writer) error {
	err := restoreFresh(NewApplier(paths, machine, out))
	fmt.Fprintln(out)
	WriteStatus(out, NewInspector(paths, machine, NewMachineRunner(paths)).Inspect())
	return err
}

// restoreFresh converges setup, then restores each declared capability it has
// something saved for. Chrome PWAs precede the Dock so saved shortcuts exist
// before a declared Dock layout is rebuilt. A backup that cannot be read is
// reported without stopping the capabilities beside it: this machine has no
// earlier state to fall back on, so a partial restore beats none.
func restoreFresh(applier Applier) error {
	// The one deliberate stop. Mise installs the applications every later
	// step restores into, so nothing below can converge without it.
	if err := applier.Apply([]Selection{{ID: setupID, Action: Apply}}); err != nil {
		return err
	}
	var failures []error
	if err := applier.RestorePreferences(); err != nil {
		failures = append(failures, err)
	}
	var selections []Selection
	if applier.Machine.ChromePWAs {
		_, _, hasSaved, err := applier.Bidir.chromePWASaved()
		switch {
		case err != nil:
			failures = append(failures, fmt.Errorf("%s: %w", chromePWAsName, err))
		case hasSaved:
			selections = append(selections, Selection{ID: chromePWAsID, Action: Apply})
		}
	}
	if applier.Machine.Dock {
		_, _, _, hasSaved, err := applier.Bidir.dockSaved()
		switch {
		case err != nil:
			failures = append(failures, fmt.Errorf("%s: %w", dockName, err))
		case hasSaved:
			selections = append(selections, Selection{ID: dockID, Action: Apply})
		}
	}
	if len(selections) > 0 {
		if err := applier.Apply(selections); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
