package ui

import (
	"context"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/azohra/config/internal/config"
)

const maxOperationOutput = 128 << 10

type operation struct {
	label     string
	name      string
	args      []string
	output    string
	events    <-chan operationEvent
	cancel    context.CancelFunc
	cancelled bool
}

type operationResult struct {
	label      string
	output     string
	err        error
	cancelled  bool
	finishedAt time.Time
}

type operationEvent struct {
	output string
	err    error
	done   bool
}

type operationEventMsg operationEvent

type eventWriter struct {
	ctx    context.Context
	events chan<- operationEvent
}

func (w eventWriter) Write(data []byte) (int, error) {
	chunk := string(append([]byte(nil), data...))
	select {
	case w.events <- operationEvent{output: chunk}:
		return len(data), nil
	case <-w.ctx.Done():
		return 0, w.ctx.Err()
	}
}

func (m Model) startOperation(label, name string, args ...string) (tea.Model, tea.Cmd) {
	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan operationEvent)
	m.operation = operation{label: label, name: name, args: append([]string(nil), args...), events: events, cancel: cancel}
	m.screen = screenRunning
	return m, tea.Batch(
		m.spinner.Tick,
		runOperation(ctx, m.paths.Root, name, args, events),
		waitOperation(events),
	)
}

func runOperation(ctx context.Context, dir, name string, args []string, events chan<- operationEvent) tea.Cmd {
	return func() tea.Msg {
		defer close(events)
		command := exec.CommandContext(ctx, name, args...)
		command.Dir = dir
		settle := config.InterruptGroup(command, config.CommandWaitDelay)
		writer := eventWriter{ctx: ctx, events: events}
		command.Stdout = writer
		command.Stderr = writer
		err := command.Run()
		settle(err)
		// A successful operation whose descendant still holds the output pipes
		// ends in ErrWaitDelay. Reporting that as a failed Apply, Save, or
		// Update reports a machine that did not converge when it did.
		events <- operationEvent{err: config.CommandFailure(command, err), done: true}
		return nil
	}
}

func waitOperation(events <-chan operationEvent) tea.Cmd {
	return func() tea.Msg {
		event, ok := <-events
		if !ok {
			return nil
		}
		return operationEventMsg(event)
	}
}

func (m Model) updateOperation(msg operationEventMsg) (tea.Model, tea.Cmd) {
	if msg.output != "" {
		m.operation.output = appendOutput(m.operation.output, msg.output)
		return m, waitOperation(m.operation.events)
	}
	if !msg.done {
		return m, waitOperation(m.operation.events)
	}
	if m.operation.cancel != nil {
		m.operation.cancel()
	}
	result := operationResult{
		label:      m.operation.label,
		output:     m.operation.output,
		err:        msg.err,
		cancelled:  m.operation.cancelled,
		finishedAt: time.Now(),
	}
	if err := saveOperationResult(m.paths, result); err != nil {
		result.output = appendOutput(result.output, "\n  ! Last result was not saved: "+err.Error()+"\n")
	}
	m.last = result
	m.operation = operation{}
	m.screen = screenResult
	m.afterInspect = screenResult
	m.loading = true
	m.checkingOverview = true
	m.overviewReady = false
	return m, tea.Batch(m.inspectCmd(), m.updatePlanCmd(config.UpdateAll, false), m.spinner.Tick)
}

func (m *Model) cancelOperation() {
	if m.operation.cancel != nil && !m.operation.cancelled {
		m.operation.cancelled = true
		m.operation.cancel()
	}
}

func appendOutput(current, chunk string) string {
	if strings.ContainsRune(current, '\x1b') {
		current = ansi.Strip(current)
	}
	if strings.ContainsRune(current, '\r') {
		current = applyTerminalOutput("", current)
	}
	current = applyTerminalOutput(current, ansi.Strip(chunk))
	if len(current) <= maxOperationOutput {
		return current
	}
	current = current[len(current)-maxOperationOutput:]
	if newline := strings.IndexByte(current, '\n'); newline >= 0 {
		current = current[newline+1:]
	}
	return current
}

func applyTerminalOutput(current, stream string) string {
	for index := 0; index < len(stream); index++ {
		switch stream[index] {
		case '\r':
			if index+1 < len(stream) && stream[index+1] == '\n' {
				continue
			}
			if newline := strings.LastIndexByte(current, '\n'); newline >= 0 {
				current = current[:newline+1]
			} else {
				current = ""
			}
		case '\b':
			lineStart := strings.LastIndexByte(current, '\n') + 1
			if len(current) > lineStart {
				_, size := utf8.DecodeLastRuneInString(current)
				current = current[:len(current)-size]
			}
		default:
			if stream[index] == '\n' || stream[index] == '\t' || (stream[index] >= ' ' && stream[index] != 0x7f) {
				current += string(stream[index])
			}
		}
	}
	return current
}
