package config

type UpdateScope string

const (
	UpdateConfig       UpdateScope = ""
	UpdateAll          UpdateScope = "all"
	UpdateSoftware     UpdateScope = "software"
	UpdateRepositories UpdateScope = "repositories"
	UpdateRepair       UpdateScope = "repair"
)

func (s UpdateScope) Label() string {
	switch s {
	case UpdateAll:
		return "Update everything"
	case UpdateSoftware:
		return "Update software"
	case UpdateRepositories:
		return "Update repositories"
	case UpdateRepair:
		return "Repair machine"
	default:
		return "Update Config"
	}
}

// command is the CLI spelling that applies the scope.
func (s UpdateScope) command() string {
	switch s {
	case UpdateRepair:
		return "config repair --yes"
	case UpdateConfig:
		return "config update config --yes"
	default:
		return "config update " + string(s) + " --yes"
	}
}

func (s UpdateScope) valid() bool {
	return s == UpdateConfig || s == UpdateAll || s == UpdateSoftware || s == UpdateRepositories || s == UpdateRepair
}

func (s UpdateScope) Valid() bool { return s.valid() }
