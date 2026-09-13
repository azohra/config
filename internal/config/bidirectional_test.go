package config

import (
	"slices"
	"testing"
)

// The Finder Favorites and Chrome PWAs answer the same question about the same three
// sources, so a state has to permit the same actions in the same order
// whichever one is asked. They used to decide that separately, where a change
// to one could leave the other behind with nothing to notice.
func TestBidirectionalCapabilitiesAgreeOnEveryState(t *testing.T) {
	for _, state := range []State{Current, SavedChanged, LiveChanged, Conflict, Unknown} {
		var favorites, pwas Resource
		finderFavoritesWords.offer(&favorites, state)
		chromePWAWords.offer(&pwas, state)

		if favorites.State != state || pwas.State != state {
			t.Fatalf("%s: states = %s and %s", state, favorites.State, pwas.State)
		}
		if !slices.Equal(favorites.Actions, pwas.Actions) {
			t.Fatalf("%s: Finder Favorites offers %v, PWAs offer %v", state, favorites.Actions, pwas.Actions)
		}
		if favorites.Summary == "" || pwas.Summary == "" {
			t.Fatalf("%s: an unreadable state: %q and %q", state, favorites.Summary, pwas.Summary)
		}
		if (len(favorites.ActionLabels) == 0) != (len(pwas.ActionLabels) == 0) {
			t.Fatalf("%s: one capability labelled its actions and the other did not", state)
		}
	}
}

// Only this Mac moved, so keeping its version is the likelier answer and is
// offered first. Every other divergence leads with the restore.
func TestALiveEditIsOfferedTheCaptureFirst(t *testing.T) {
	var live, conflicted, current Resource
	finderFavoritesWords.offer(&live, LiveChanged)
	finderFavoritesWords.offer(&conflicted, Conflict)
	finderFavoritesWords.offer(&current, Current)

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
		Current:      "this Mac matches the saved Favorites",
		SavedChanged: "the saved Favorites changed",
		LiveChanged:  "Finder Favorites on this Mac changed",
		Conflict:     "the saved Favorites and this Mac both changed",
		Unknown:      "this Mac and the saved Favorites differ",
	} {
		var resource Resource
		finderFavoritesWords.offer(&resource, state)
		if resource.Summary != want {
			t.Errorf("%s reads %q, want %q", state, resource.Summary, want)
		}
	}
}
