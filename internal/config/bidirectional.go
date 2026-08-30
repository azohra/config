package config

type Bidirectional struct {
	Paths     Paths
	Runner    Runner
	Baselines Baselines
}

func NewBidirectional(paths Paths, runner Runner) Bidirectional {
	return Bidirectional{Paths: paths, Runner: runner, Baselines: Baselines{Dir: paths.StateDir}}
}

// bidirectionalWords name one capability's two sides. The rest of a
// bidirectional resource — which actions a state allows, in what order, and
// how the state reads — does not vary between capabilities, so it is decided
// here rather than once per capability.
type bidirectionalWords struct {
	saved   string // "the saved layout"
	live    string // "the Dock on this Mac"
	capture string // "Save this Mac's layout"
	restore string // "Restore the saved layout"
}

// offer records the state and what it permits. A live edit leads with the
// capture, because keeping this Mac's version is the likelier answer when
// only this Mac moved; every other divergence leads with the restore.
func (w bidirectionalWords) offer(resource *Resource, state State) {
	resource.State = state
	switch state {
	case Current:
		resource.Summary = "this Mac matches " + w.saved
		return
	case SavedChanged:
		resource.Summary = w.saved + " changed"
	case LiveChanged:
		resource.Summary = w.live + " changed"
	case Conflict:
		resource.Summary = w.saved + " and this Mac both changed"
	case Unknown:
		resource.Summary = "this Mac and " + w.saved + " differ"
	}
	resource.ActionLabels = map[Action]string{Capture: w.capture, Apply: w.restore}
	if state == LiveChanged {
		resource.Actions = []Action{Capture, Apply}
		return
	}
	resource.Actions = []Action{Apply, Capture}
}
