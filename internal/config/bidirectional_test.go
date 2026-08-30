package config

import (
	"slices"
	"testing"
)

// The Dock and Chrome PWAs answer the same question about the same three
// sources, so a state has to permit the same actions in the same order
// whichever one is asked. They used to decide that separately, where a change
// to one could leave the other behind with nothing to notice.
func TestBidirectionalCapabilitiesAgreeOnEveryState(t *testing.T) {
	for _, state := range []State{Current, SavedChanged, LiveChanged, Conflict, Unknown} {
		var dock, pwas Resource
		dockWords.offer(&dock, state)
		chromePWAWords.offer(&pwas, state)

		if dock.State != state || pwas.State != state {
			t.Fatalf("%s: states = %s and %s", state, dock.State, pwas.State)
		}
		if !slices.Equal(dock.Actions, pwas.Actions) {
			t.Fatalf("%s: Dock offers %v, PWAs offer %v", state, dock.Actions, pwas.Actions)
		}
		if dock.Summary == "" || pwas.Summary == "" {
			t.Fatalf("%s: an unreadable state: %q and %q", state, dock.Summary, pwas.Summary)
		}
		if (len(dock.ActionLabels) == 0) != (len(pwas.ActionLabels) == 0) {
			t.Fatalf("%s: one capability labelled its actions and the other did not", state)
		}
	}
}

// Only this Mac moved, so keeping its version is the likelier answer and is
// offered first. Every other divergence leads with the restore.
func TestALiveEditIsOfferedTheCaptureFirst(t *testing.T) {
	var live, conflicted, current Resource
	dockWords.offer(&live, LiveChanged)
	dockWords.offer(&conflicted, Conflict)
	dockWords.offer(&current, Current)

	if !slices.Equal(live.Actions, []Action{Capture, Apply}) {
		t.Fatalf("a live edit offers %v", live.Actions)
	}
	if !slices.Equal(conflicted.Actions, []Action{Apply, Capture}) {
		t.Fatalf("a conflict offers %v", conflicted.Actions)
	}
	if len(current.Actions) != 0 || len(current.ActionLabels) != 0 {
		t.Fatalf("a converged resource offers %v", current.Actions)
	}
}

// The summary is what the reader sees, so each state has to name which side
// moved rather than only that something did.
func TestEachStateReadsAsWhichSideMoved(t *testing.T) {
	for state, want := range map[State]string{
		Current:      "this Mac matches the saved layout",
		SavedChanged: "the saved layout changed",
		LiveChanged:  "the Dock on this Mac changed",
		Conflict:     "the saved layout and this Mac both changed",
		Unknown:      "this Mac and the saved layout differ",
	} {
		var resource Resource
		dockWords.offer(&resource, state)
		if resource.Summary != want {
			t.Errorf("%s reads %q, want %q", state, resource.Summary, want)
		}
	}
}
