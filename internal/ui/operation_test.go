package ui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	config "github.com/azohra/config/internal/config"
)

func TestRunOperationStreamsOutput(t *testing.T) {
	events := make(chan operationEvent)
	done := make(chan tea.Msg, 1)
	dir := t.TempDir()
	go func() {
		done <- runOperation(context.Background(), dir, "/bin/sh", []string{"-c", "printf 'one\\ntwo\\n'"}, events)()
	}()
	var output string
	var final operationEvent
	for event := range events {
		output += event.output
		if event.done {
			final = event
		}
	}
	<-done
	if final.err != nil || output != "one\ntwo\n" {
		t.Fatalf("output=%q err=%v", output, final.err)
	}
}

func TestRunOperationCancelsProcessGroup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan operationEvent)
	done := make(chan tea.Msg, 1)
	dir := t.TempDir()
	go func() {
		done <- runOperation(ctx, dir, "/bin/sh", []string{"-c", "printf ready; sleep 10"}, events)()
	}()
	first := <-events
	if first.output != "ready" {
		t.Fatalf("first event = %#v", first)
	}
	cancel()
	var final operationEvent
	for event := range events {
		if event.done {
			final = event
		}
	}
	<-done
	if final.err == nil {
		t.Fatal("cancelled operation succeeded")
	}
}

func TestRefreshPreservesOperationFailure(t *testing.T) {
	want := errors.New("failed")
	m := Model{loading: true, afterInspect: screenDashboard, last: operationResult{label: "Save", err: want}}
	next, _ := m.Update(reportMsg{report: config.Report{}})
	got := next.(Model)
	if !errors.Is(got.last.err, want) || got.screen != screenDashboard || got.loading {
		t.Fatalf("refresh lost result: screen=%v loading=%v err=%v", got.screen, got.loading, got.last.err)
	}
}

func TestAppendOutputNormalizesAndBounds(t *testing.T) {
	got := appendOutput("one\r", "two\r\n")
	if got != "one\ntwo\n" {
		t.Fatalf("appendOutput() = %q", got)
	}
	got = appendOutput("", strings.Repeat("x", maxOperationOutput+10)+"\nlast\n")
	if len(got) > maxOperationOutput || !strings.HasSuffix(got, "last\n") {
		t.Fatalf("bounded output length=%d suffix=%q", len(got), got[len(got)-5:])
	}
}

// An operation streams output while it runs and hands the interface back to
// the dashboard when it ends, asking for a fresh inspection because the
// operation just changed the machine it was describing.
func TestUpdateOperationStreamsThenRefreshes(t *testing.T) {
	events := make(chan operationEvent, 1)
	m := Model{screen: screenRunning, operation: operation{label: "Apply", events: events}}

	next, cmd := m.updateOperation(operationEventMsg{output: "  ✓ layout already current\n"})
	running := next.(Model)
	if running.screen != screenRunning {
		t.Fatalf("output moved off the running screen: %v", running.screen)
	}
	if !strings.Contains(running.operation.output, "layout already current") {
		t.Fatalf("output was not kept: %q", running.operation.output)
	}
	if cmd == nil {
		t.Fatal("the interface stopped waiting for the rest of the operation")
	}

	finished, cmd := running.updateOperation(operationEventMsg{done: true})
	dashboard := finished.(Model)
	if dashboard.screen != screenDashboard {
		t.Fatalf("a finished operation left screen %v", dashboard.screen)
	}
	if !dashboard.loading {
		t.Fatal("a finished operation did not refresh the report it invalidated")
	}
	if dashboard.last.label != "Apply" || !strings.Contains(dashboard.last.output, "layout already current") {
		t.Fatalf("the result was not carried to the dashboard: %+v", dashboard.last)
	}
	if dashboard.operation.events != nil {
		t.Fatal("the finished operation was not cleared")
	}
	if cmd == nil {
		t.Fatal("no refresh was scheduled")
	}
}

// Cancelling is a request, not an ending: the operation keeps running until
// its own event says otherwise, and the interface says so meanwhile.
func TestCancelOperationMarksWithoutEnding(t *testing.T) {
	cancelled := false
	m := Model{
		screen:    screenRunning,
		width:     80,
		height:    24,
		operation: operation{label: "Save", cancel: func() { cancelled = true }},
	}
	m.cancelOperation()
	if !m.operation.cancelled || !cancelled {
		t.Fatalf("cancel did not reach the operation: %+v", m.operation)
	}
	if !strings.Contains(ansi.Strip(m.renderRunning()), "Cancelling") {
		t.Fatalf("the interface did not say it was cancelling:\n%s", m.renderRunning())
	}

	// Cancelling twice must not cancel twice.
	calls := 0
	m.operation = operation{label: "Save", cancel: func() { calls++ }}
	m.cancelOperation()
	m.cancelOperation()
	if calls != 1 {
		t.Fatalf("cancel ran %d times", calls)
	}
}

// A cancelled operation is not a failure, and the dashboard says which it was.
func TestResultBannerDistinguishesCancelledFromFailed(t *testing.T) {
	cancelled := Model{width: 80, height: 24, last: operationResult{label: "Apply", cancelled: true}}
	if banner := ansi.Strip(cancelled.resultBanner()); !strings.Contains(banner, "Apply cancelled") {
		t.Fatalf("cancelled banner = %q", banner)
	}
	failed := Model{width: 80, height: 24, last: operationResult{label: "Apply", err: errors.New("defaults: exit status 1")}}
	if banner := ansi.Strip(failed.resultBanner()); !strings.Contains(banner, "Apply failed") || !strings.Contains(banner, "defaults") {
		t.Fatalf("failed banner = %q", banner)
	}
	done := Model{width: 80, height: 24, last: operationResult{label: "Apply"}}
	if banner := ansi.Strip(done.resultBanner()); !strings.Contains(banner, "Apply complete") {
		t.Fatalf("complete banner = %q", banner)
	}
}
