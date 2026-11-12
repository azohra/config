package config

// UpdateScope names the steps an update plans and runs.
type UpdateScope string

const (
	UpdateConfig       UpdateScope = ""
	UpdateAll          UpdateScope = "all"
	UpdateSoftware     UpdateScope = "software"
	UpdateRepositories UpdateScope = "repositories"
	UpdateMise         UpdateScope = "mise"
	UpdateTools        UpdateScope = "tools"
	UpdateApps         UpdateScope = "apps"
	UpdateRepair       UpdateScope = "repair"
)

// Step ids. A plan's steps carry them, and a selection can name them.
const (
	configStepID       = "config"
	machineStepID      = "machine"
	miseStepID         = "mise"
	toolsStepID        = "tools"
	appsStepID         = "apps"
	repositoriesStepID = "repositories"
	filesStepID        = "files"
	setupStepID        = "setup"
)

// ParseUpdateScope reads a scope as typed on the command line.
func ParseUpdateScope(name string) (UpdateScope, bool) {
	if name == "config" {
		return UpdateConfig, true
	}
	scope := UpdateScope(name)
	return scope, name != "" && scope.valid()
}

// String is the scope as typed on the command line.
func (s UpdateScope) String() string {
	if s == UpdateConfig {
		return "config"
	}
	return string(s)
}

func (s UpdateScope) Label() string {
	switch s {
	case UpdateAll:
		return "Update everything"
	case UpdateSoftware:
		return "Update software"
	case UpdateRepositories:
		return "Update repositories"
	case UpdateMise:
		return "Update mise"
	case UpdateTools:
		return "Update tools"
	case UpdateApps:
		return "Update apps and packages"
	case UpdateRepair:
		return "Repair machine"
	default:
		return "Update Config"
	}
}

// Command is the CLI spelling that reviews the scope.
func (s UpdateScope) Command() string {
	if s == UpdateRepair {
		return "config repair"
	}
	return "config update " + s.String()
}

func (s UpdateScope) includesConfig() bool { return s == UpdateConfig || s == UpdateAll }

// steps are the native steps a scope plans, in the order they run.
func (s UpdateScope) steps() []string {
	switch s {
	case UpdateAll:
		return []string{miseStepID, toolsStepID, appsStepID, repositoriesStepID, filesStepID}
	case UpdateSoftware:
		return []string{miseStepID, toolsStepID, appsStepID}
	case UpdateRepositories:
		return []string{repositoriesStepID, filesStepID}
	case UpdateMise, UpdateTools, UpdateApps:
		return []string{string(s)}
	case UpdateRepair:
		return []string{setupStepID}
	}
	return nil
}

func (s UpdateScope) valid() bool { return s == UpdateConfig || len(s.steps()) > 0 }

func (s UpdateScope) Valid() bool { return s.valid() }
