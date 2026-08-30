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
	if err == nil {
		_, err = decodePlist(data)
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
			Label:    "Saved settings valid",
			Severity: Failure,
			Detail:   err.Error(),
		}}
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
	if _, err := decodePlist(data); err != nil {
		return fmt.Errorf("export %s: %w", p.Name, err)
	}
	return atomicWrite(p.snapshotPath(paths), data, 0o600)
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
