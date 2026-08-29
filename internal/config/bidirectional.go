package config

type Bidirectional struct {
	Paths     Paths
	Runner    Runner
	Baselines Baselines
}

func NewBidirectional(paths Paths, runner Runner) Bidirectional {
	return Bidirectional{Paths: paths, Runner: runner, Baselines: Baselines{Dir: paths.StateDir}}
}
