package ui

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"
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
	label     string
	output    string
	err       error
	cancelled bool
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
		command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		command.WaitDelay = 2 * time.Second
		command.Cancel = func() error {
			if command.Process == nil {
				return os.ErrProcessDone
			}
			err := syscall.Kill(-command.Process.Pid, syscall.SIGINT)
			if errors.Is(err, syscall.ESRCH) {
				return os.ErrProcessDone
			}
			return err
		}
		writer := eventWriter{ctx: ctx, events: events}
		command.Stdout = writer
		command.Stderr = writer
		err := command.Run()
		events <- operationEvent{err: err, done: true}
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
	m.last = operationResult{
		label:     m.operation.label,
		output:    m.operation.output,
		err:       msg.err,
		cancelled: m.operation.cancelled,
	}
	m.operation = operation{}
	m.screen = screenDashboard
	m.afterInspect = screenDashboard
	m.loading = true
	return m, tea.Batch(m.inspectCmd(), m.spinner.Tick)
}

func (m *Model) cancelOperation() {
	if m.operation.cancel != nil && !m.operation.cancelled {
		m.operation.cancelled = true
		m.operation.cancel()
	}
}

func appendOutput(current, chunk string) string {
	current += chunk
	current = strings.ReplaceAll(current, "\r\n", "\n")
	current = strings.ReplaceAll(current, "\r", "\n")
	if len(current) <= maxOperationOutput {
		return current
	}
	current = current[len(current)-maxOperationOutput:]
	if newline := strings.IndexByte(current, '\n'); newline >= 0 {
		current = current[newline+1:]
	}
	return current
}
