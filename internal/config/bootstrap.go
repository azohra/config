package config

import (
	"fmt"
	"io"
)

// Bootstrap is the fresh-clone restore sequence. It converges setup before
// restoring each declared capability; Chrome PWAs precede the Dock so saved
// shortcuts exist before a declared Dock layout is rebuilt.
func RestoreFresh(paths Paths, machine Machine, out io.Writer) error {
	applier := NewApplier(paths, machine, out)
	if err := applier.Apply([]Selection{{ID: setupID, Action: Apply}}); err != nil {
		return err
	}
	if err := applier.RestorePreferences(); err != nil {
		return err
	}
	var selections []Selection
	if machine.ChromePWAs {
		_, _, hasSaved, err := applier.Bidir.chromePWASaved()
		if err != nil {
			return err
		}
		if hasSaved {
			selections = append(selections, Selection{ID: chromePWAsID, Action: Apply})
		}
	}
	if machine.Dock {
		_, _, _, hasSaved, err := applier.Bidir.dockSaved()
		if err != nil {
			return err
		}
		if hasSaved {
			selections = append(selections, Selection{ID: dockID, Action: Apply})
		}
	}
	if len(selections) > 0 {
		if err := applier.Apply(selections); err != nil {
			return err
		}
	}
	fmt.Fprintln(out)
	WriteStatus(out, NewInspector(paths, machine, NewMachineRunner(paths)).Inspect())
	return nil
}
