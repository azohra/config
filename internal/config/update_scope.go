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

func (s UpdateScope) valid() bool {
	return s == UpdateConfig || s == UpdateAll || s == UpdateSoftware || s == UpdateRepositories || s == UpdateRepair
}

func (s UpdateScope) Valid() bool { return s.valid() }
