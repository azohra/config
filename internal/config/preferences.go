package config

import (
	"fmt"
	"os"
)

type PreferenceBackup struct {
	ID     string `toml:"id"`
	Name   string `toml:"name"`
	Bundle string `toml:"bundle"`
	Domain string `toml:"domain"`
}

func (p PreferenceBackup) snapshotPath(paths Paths) string {
	return paths.InRoot("snapshots", "preferences", p.ID+".plist")
}

// Inspect checks the backup artifact, not the app's mutable contents. The app
// is authoritative on an existing Mac; bootstrap is the only restore path.
func (p PreferenceBackup) Inspect(paths Paths) Resource {
	resource := Resource{ID: p.ID, Name: p.Name}
	data, err := os.ReadFile(p.snapshotPath(paths))
	var values map[string]any
	if err == nil {
		values, err = decodePlist(data)
	}
	if err == nil && len(values) == 0 {
		// An empty saved domain records nothing. Report it the way a missing
		// artifact is reported so Capture stays on offer.
		err = os.ErrNotExist
	}
	if os.IsNotExist(err) {
		resource.State = Uncaptured
		resource.Summary = "settings have not been captured"
		resource.Actions = []Action{Capture}
		resource.ActionLabels = map[Action]string{Capture: "Capture current settings"}
		return resource
	}
	if err != nil {
		resource.State = Unavailable
		resource.Summary = "saved settings are invalid"
		resource.Checks = []Check{{
			Label:  "Saved settings valid",
			Detail: err.Error(),
		}}
		// Capture is the only way to replace an artifact Config cannot read.
		// Without it the resource fails preflight forever and the product
		// offers no action that clears it.
		resource.Actions = []Action{Capture}
		resource.ActionLabels = map[Action]string{Capture: "Recapture current settings"}
		return resource
	}
	resource.State = Current
	resource.Summary = "preference backup available"
	resource.Checks = []Check{{Label: "Preference backup valid", OK: true}}
	return resource
}

// Backup copies the complete current defaults domain. No keys are interpreted,
// filtered, or preserved separately: the app is the source of truth.
func (p PreferenceBackup) Backup(paths Paths, runner Runner) error {
	result := run(runner, "defaults", "export", p.Domain, "-")
	if result.Err != nil {
		return fmt.Errorf("export %s: %w", p.Name, result.Failure())
	}
	data := []byte(result.Stdout)
	values, err := decodePlist(data)
	if err != nil {
		return fmt.Errorf("export %s: %w", p.Name, err)
	}
	// defaults exits 0 for a domain that does not exist and prints an empty
	// dictionary. Storing that would record "captured" over settings that were
	// never read, so a domain with nothing in it is a refusal, not a backup.
	if len(values) == 0 {
		return fmt.Errorf("export %s: %s holds no settings", p.Name, p.Domain)
	}
	return AtomicWrite(p.snapshotPath(paths), data, 0o600)
}

func preferenceIsRunning(runner Runner, preference PreferenceBackup) (bool, error) {
	result := run(runner, "osascript", "-e", `application id "`+preference.Bundle+`" is running`)
	if result.Err != nil {
		return false, fmt.Errorf("inspect %s process: %w", preference.Name, result.Failure())
	}
	switch result.Output() {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("inspect %s process: unexpected result %q", preference.Name, result.Output())
	}
}
