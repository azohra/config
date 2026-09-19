package config

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type State string

const (
	Current      State = "current"
	Uncaptured   State = "uncaptured"
	Drift        State = "drift"
	SavedChanged State = "saved-changed"
	LiveChanged  State = "live-changed"
	Conflict     State = "conflict"
	Unknown      State = "unknown"
	Unavailable  State = "unavailable"
)

type Action string

const (
	Skip    Action = "skip"
	Apply   Action = "apply"
	Capture Action = "capture"
)

type Check struct {
	Label  string
	OK     bool
	Detail string
}

type Resource struct {
	ID            string
	Name          string
	State         State
	Summary       string
	Checks        []Check
	Details       []string
	Actions       []Action
	ActionLabels  map[Action]string
	Bidirectional bool
	// Authoritative marks a resource that converges live machine settings and
	// records nothing in the repository. Its health belongs in status, but it
	// cannot make a snapshot wrong, so it does not gate a save.
	Authoritative bool
}

func (r Resource) Allows(action Action) bool {
	return slices.Contains(r.Actions, action)
}

// NeedsDecision is the unresolved-choice verdict. A capability that has never
// been captured owns no snapshot content, so it cannot make a commit wrong and
// is not a choice anyone is being asked to make.
func (r Resource) NeedsDecision() bool {
	return r.Bidirectional && r.State != Uncaptured && len(r.Actions) > 0
}

func (r Resource) Failed() int {
	n := 0
	for _, check := range r.Checks {
		if !check.OK {
			n++
		}
	}
	return n
}

// Symbol is the one state-to-severity glyph every status surface renders.
func (r Resource) Symbol() string {
	switch {
	case r.Failed() > 0 || r.State == Drift:
		return GlyphError
	case r.NeedsDecision():
		return GlyphChoice
	case r.State == Uncaptured || r.State == Unavailable:
		return GlyphWarn
	default:
		return GlyphOK
	}
}

type SnapshotStatus struct {
	Branch      string
	Commit      string
	Dirty       int
	Changes     []string
	Upstream    string
	Ahead       int
	Behind      int
	Destination string
	PolicyError string
}

func (s SnapshotStatus) NeedsSave() bool {
	return s.Dirty > 0 || s.Ahead > 0 || s.Upstream == "" || s.PolicyError != ""
}

func (s SnapshotStatus) Warnings() int {
	n := 0
	if s.Dirty > 0 {
		n++
	}
	if s.Upstream == "" || s.Ahead > 0 {
		n++
	}
	if s.Behind > 0 {
		n++
	}
	if s.PolicyError != "" {
		n++
	}
	return n
}

// PendingParts names each snapshot fact that still needs attention. Every
// surface that describes pending snapshot work renders these parts.
func (s SnapshotStatus) PendingParts() []string {
	var parts []string
	if s.Dirty > 0 {
		parts = append(parts, FormatCount(s.Dirty, "changed file", "changed files"))
	}
	if s.Ahead > 0 {
		parts = append(parts, FormatCount(s.Ahead, "unpushed commit", "unpushed commits"))
	}
	if s.Behind > 0 {
		parts = append(parts, FormatCount(s.Behind, "remote commit absent", "remote commits absent"))
	}
	if s.Upstream == "" {
		parts = append(parts, "no upstream")
	}
	if s.PolicyError != "" {
		parts = append(parts, s.PolicyError)
	}
	return parts
}

func (s SnapshotStatus) Summary() string {
	parts := s.PendingParts()
	if len(parts) == 0 {
		return s.Commit + " · " + s.Upstream
	}
	summary := strings.Join(parts, " · ")
	if s.Destination != "" {
		summary += " → " + s.Destination
	}
	return summary
}

type Report struct {
	Resources []Resource
	Snapshot  SnapshotStatus
}

func (r Report) Resource(id string) (Resource, bool) {
	for _, resource := range r.Resources {
		if resource.ID == id {
			return resource, true
		}
	}
	return Resource{}, false
}

// Counts totals the resource-side numbers only; snapshot warnings remain a
// snapshot fact reached through Snapshot.Warnings().
func (r Report) Counts() (failures, decisions, advisories int) {
	for _, resource := range r.Resources {
		failures += resource.Failed()
		if resource.NeedsDecision() {
			decisions++
		} else if resource.State == Uncaptured {
			advisories++
		}
	}
	return failures, decisions, advisories
}

// NeedsAttention is the verdict behind an unsuccessful exit: a check that
// failed, or a bidirectional resource still waiting on a choice. Every status
// surface answers it the same way, whichever way Config was invoked.
func (r Report) NeedsAttention() bool {
	failures, decisions, _ := r.Counts()
	return failures > 0 || decisions > 0
}

// PreflightError is the gate Save runs before it commits. It asks only
// whether the state this snapshot would record is describable: a resource
// that owns no snapshot content cannot make the commit wrong.
func (r Report) PreflightError() error {
	var problems []string
	for _, resource := range r.Resources {
		if resource.Authoritative {
			continue
		}
		for _, check := range resource.Checks {
			if !check.OK {
				problems = append(problems, resource.Name+": "+check.Label)
			}
		}
		if resource.NeedsDecision() {
			problems = append(problems, resource.Name+": unresolved "+string(resource.State))
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("machine preflight failed:\n  %s", strings.Join(problems, "\n  "))
}

// defaultConfigDir is Config's managed application-state checkout.
const defaultConfigDir = "Library/Application Support/Config/repository"

type Paths struct {
	Root     string
	Home     string
	StateDir string
}

// NewPaths derives every path Config owns from one home directory. Config's
// own state is deliberately not XDG-relocatable: the advisory lock lives in
// StateDir, so a cache directory the caller could move independently would
// give two Configs converging one checkout two different locks.
func NewPaths(home string) (Paths, error) {
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return Paths{}, err
		}
	}
	root, err := filepath.Abs(filepath.Join(home, defaultConfigDir))
	if err != nil {
		return Paths{}, err
	}
	return Paths{Root: root, Home: home, StateDir: stateDir(home)}, nil
}

// stateDir is Config's private state: baselines, pending markers, restore
// records, the checkout lock, and the release adapter's root beside it.
func stateDir(home string) string {
	return filepath.Join(home, ".cache", "config", "state")
}

func FormatCount(n int, singular, plural string) string {
	word := plural
	if n == 1 {
		word = singular
	}
	return fmt.Sprintf("%d %s", n, word)
}

// InRoot addresses this machine's configuration — the dotfile sources,
// captured preferences, and declarations at the root of its config repo.
func (p Paths) InRoot(parts ...string) string {
	return filepath.Join(append([]string{p.Root}, parts...)...)
}

func (p Paths) InHome(parts ...string) string {
	return filepath.Join(append([]string{p.Home}, parts...)...)
}

type Selection struct {
	ID     string `json:"id"`
	Action Action `json:"action"`
}
