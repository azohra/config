package ui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	config "github.com/azohra/config/internal/config"
)

func TestRunOperationStreamsOutput(t *testing.T) {
	events := make(chan operationEvent)
	done := make(chan tea.Msg, 1)
	dir := t.TempDir()
	go func() {
		done <- runOperation(context.Background(), dir, "/bin/sh", []string{"-c", "printf 'one\\ntwo\\n'"}, events, false)()
	}()
	var output string
	var final operationEvent
	for event := range events {
		output += event.event.Text
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
	// Cancelling has to take the whole group, not just the process Config
	// started. Asserting only that the run returned an error cannot tell the
	// two apart: any cancellation produces one.
	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan operationEvent)
	done := make(chan tea.Msg, 1)
	dir := t.TempDir()
	pidfile := filepath.Join(dir, "descendant.pid")
	script := "/bin/sh -c 'echo $$ > " + pidfile + "; sleep 30' & printf ready; wait"
	go func() {
		done <- runOperation(ctx, dir, "/bin/sh", []string{"-c", script}, events, false)()
	}()
	first := <-events
	if first.event.Text != "ready" {
		t.Fatalf("first event = %#v", first)
	}
	descendant := 0
	for range 200 {
		if data, err := os.ReadFile(pidfile); err == nil {
			if pid, convErr := strconv.Atoi(strings.TrimSpace(string(data))); convErr == nil {
				descendant = pid
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if descendant == 0 {
		t.Fatal("the descendant never recorded its process id")
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
	for range 200 {
		if errors.Is(syscall.Kill(descendant, 0), syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	syscall.Kill(descendant, syscall.SIGKILL)
	t.Fatalf("descendant %d outlived the cancelled operation", descendant)
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
	output := outputFromString("one\r")
	output.Append(config.OperationEvent{Kind: config.OperationOutput, Text: "two\r\n"})
	got := output.String()
	if got != "two\n" {
		t.Fatalf("appendOutput() = %q", got)
	}
	output = terminalOutput{}
	output.Append(config.OperationEvent{Kind: config.OperationOutput, Text: "\x1b[32mready\x1b[0m\rworking\rfinished\n"})
	got = output.String()
	if got != "finished\n" {
		t.Fatalf("appendOutput() kept terminal animation: %q", got)
	}
	output = terminalOutput{}
	output.Append(config.OperationEvent{Kind: config.OperationOutput, Text: "wait..\b\b  \b\bdone\a\n"})
	got = output.String()
	if got != "waitdone\n" {
		t.Fatalf("appendOutput() kept cursor controls: %q", got)
	}
	output = terminalOutput{}
	output.Append(config.OperationEvent{Kind: config.OperationOutput, Text: strings.Repeat("x", maxOperationOutput+10) + "\nlast\n"})
	got = output.String()
	if len(got) > maxOperationOutput || !strings.HasSuffix(got, "last\n") {
		t.Fatalf("bounded output length=%d suffix=%q", len(got), got[len(got)-5:])
	}
}

func TestTerminalOutputPreservesSplitUTF8AndANSI(t *testing.T) {
	var output terminalOutput
	check := []byte("✓")
	output.Append(config.OperationEvent{Kind: config.OperationOutput, Text: string(check[:1])})
	output.Append(config.OperationEvent{Kind: config.OperationOutput, Text: string(check[1:]) + " \x1b[3"})
	output.Append(config.OperationEvent{Kind: config.OperationOutput, Text: "2mready\x1b[0m\n"})
	if got := output.String(); got != "✓ ready\n" || !utf8.ValidString(got) {
		t.Fatalf("split stream = %q", got)
	}
}

func BenchmarkTerminalOutput(b *testing.B) {
	chunk := config.OperationEvent{Kind: config.OperationOutput, Text: "\x1b[32mchecking\x1b[0m ✓ package\rready\n"}
	for b.Loop() {
		var output terminalOutput
		for range 256 {
			output.Append(chunk)
		}
		_ = output.String()
	}
}

// An operation streams output while it runs and keeps its result visible when
// it ends, asking for a fresh inspection because it changed the machine.
func TestUpdateOperationStreamsThenRefreshes(t *testing.T) {
	events := make(chan operationEvent, 1)
	root := t.TempDir()
	paths := config.Paths{Root: root, Home: root, StateDir: filepath.Join(root, "state")}
	m := New(paths, config.Machine{}, "/bin/true", "dev")
	m.screen = screenRunning
	m.loading = false
	m.operation = operation{label: "Apply", events: events}

	next, cmd := m.updateOperation(operationEventMsg{event: config.OperationEvent{Kind: config.OperationOK, Text: "layout already current"}})
	running := next.(Model)
	if running.screen != screenRunning {
		t.Fatalf("output moved off the running screen: %v", running.screen)
	}
	if !strings.Contains(running.operation.output.String(), "layout already current") {
		t.Fatalf("output was not kept: %q", running.operation.output.String())
	}
	if cmd == nil {
		t.Fatal("the interface stopped waiting for the rest of the operation")
	}

	finished, cmd := running.updateOperation(operationEventMsg{done: true})
	result := finished.(Model)
	if result.screen != screenResult {
		t.Fatalf("a finished operation left screen %v", result.screen)
	}
	if result.loading {
		t.Fatal("a finished operation hid its result behind a refresh")
	}
	if result.last.label != "Apply" || !strings.Contains(result.last.output.String(), "layout already current") {
		t.Fatalf("the result was not carried to the result screen: %+v", result.last)
	}
	if result.operation.events != nil {
		t.Fatal("the finished operation was not cleared")
	}
	if cmd == nil {
		t.Fatal("no refresh was scheduled")
	}
	refresh, ok := cmd().(reportMsg)
	if !ok || !refresh.passive {
		t.Fatalf("operation refresh = %#v, want passive report", refresh)
	}
	refreshed, _ := result.Update(refresh)
	if got := refreshed.(Model); got.screen != screenResult || got.loading {
		t.Fatalf("refresh hid the completed result: screen=%v loading=%v", got.screen, got.loading)
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

func TestConfigVersionEventRestartsTheParentAndReopensItsResult(t *testing.T) {
	paths := config.Paths{StateDir: filepath.Join(t.TempDir(), "state")}
	events := make(chan operationEvent, 1)
	m := Model{
		paths: paths, version: "v0.13.0", screen: screenRunning,
		operation: operation{label: "Software update", events: events},
	}
	next, _ := m.updateOperation(operationEventMsg{event: config.OperationEvent{Kind: config.OperationVersion, Text: "v0.14.0"}})
	finished, cmd := next.(Model).updateOperation(operationEventMsg{done: true})
	result := finished.(Model)
	if !result.RestartRequested() || result.screen != screenResult || cmd == nil {
		t.Fatalf("version handoff = restart %v screen %v command %v", result.RestartRequested(), result.screen, cmd)
	}
	reopened := New(paths, config.Machine{}, "/tmp/config", "v0.14.0", true)
	if reopened.screen != screenResult || reopened.loading || reopened.last.label != "Software update" {
		t.Fatalf("reopened result = screen %v loading %v last %+v", reopened.screen, reopened.loading, reopened.last)
	}
}
