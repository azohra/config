package ui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

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
