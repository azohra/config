package ui

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/azohra/config/internal/config"
)

const (
	operationOutputFlushInterval = 75 * time.Millisecond
	maxOperationEventBatch       = 128
)

type operation struct {
	label     string
	name      string
	args      []string
	log       operationLog
	events    <-chan operationEvent
	cancel    context.CancelFunc
	cancelled bool
	version   string
	startedAt time.Time
}

type operationResult struct {
	label      string
	log        operationLog
	err        error
	cancelled  bool
	finishedAt time.Time
	duration   time.Duration
}

type operationEvent struct {
	event config.OperationEvent
	err   error
	done  bool
}

type operationEventsMsg []operationEvent

type eventWriter struct {
	ctx    context.Context
	events chan<- operationEvent
}

func (w eventWriter) Write(data []byte) (int, error) {
	event := config.OperationEvent{Kind: config.OperationOutput, Text: string(append([]byte(nil), data...))}
	select {
	case w.events <- operationEvent{event: event}:
		return len(data), nil
	case <-w.ctx.Done():
		return 0, w.ctx.Err()
	}
}

func (m Model) startOperation(label, name string, args ...string) (tea.Model, tea.Cmd) {
	m.cancelUpdatePlanning()
	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan operationEvent, maxOperationEventBatch)
	m.operation = operation{
		label: label, name: name, args: append([]string(nil), args...), log: newOperationLog(),
		events: events, cancel: cancel, startedAt: time.Now(),
	}
	m.showDiagnostics = false
	m.scroll = 0
	m.screen = screenRunning
	return m, tea.Batch(
		m.spinner.Tick,
		runOperation(ctx, m.paths.Root, name, args, events, true),
		waitOperation(events),
	)
}

func runOperation(ctx context.Context, dir, name string, args []string, events chan<- operationEvent, structured bool) tea.Cmd {
	return func() tea.Msg {
		defer close(events)
		command := exec.CommandContext(ctx, name, args...)
		command.Dir = dir
		settle := config.InterruptGroup(command, config.CommandWaitDelay)
		writer := eventWriter{ctx: ctx, events: events}
		command.Stderr = writer
		var err error
		if structured {
			command.Env = operationEnvironment()
			stdout, pipeErr := command.StdoutPipe()
			if pipeErr != nil {
				err = pipeErr
			} else if startErr := command.Start(); startErr != nil {
				err = startErr
			} else {
				decodeErr := decodeOperationEvents(ctx, stdout, events)
				waitErr := command.Wait()
				err = errors.Join(waitErr, decodeErr)
			}
		} else {
			command.Stdout = writer
			err = command.Run()
		}
		settle()
		// A successful operation whose descendant still holds the output pipes
		// ends in ErrWaitDelay. Reporting that as a failed Apply, Save, or
		// Update reports a machine that did not converge when it did.
		events <- operationEvent{err: config.CommandFailure(command, err), done: true}
		return nil
	}
}

func operationEnvironment() []string {
	prefix := config.OperationEventsEnv + "="
	environment := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, prefix) {
			environment = append(environment, entry)
		}
	}
	return append(environment, prefix+"1")
}

func decodeOperationEvents(ctx context.Context, stream io.Reader, events chan<- operationEvent) error {
	reader := bufio.NewReader(stream)
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) == 0 && errors.Is(readErr, io.EOF) {
			return nil
		}
		var event config.OperationEvent
		if err := json.Unmarshal(line, &event); err != nil || !knownOperationEvent(event.Kind) {
			event = config.OperationEvent{Kind: config.OperationOutput, Text: string(line)}
		}
		select {
		case events <- operationEvent{event: event}:
		case <-ctx.Done():
			return ctx.Err()
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			return fmt.Errorf("read Config operation event: %w", readErr)
		}
	}
}

func knownOperationEvent(kind config.OperationEventKind) bool {
	switch kind {
	case config.OperationOutput, config.OperationSection, config.OperationOK, config.OperationInfo,
		config.OperationWarn, config.OperationError, config.OperationVersion:
		return true
	default:
		return false
	}
}

func waitOperation(events <-chan operationEvent) tea.Cmd {
	return func() tea.Msg {
		first, ok := <-events
		if !ok {
			return nil
		}
		batch := []operationEvent{first}
		if first.done || first.event.Kind != config.OperationOutput {
			return operationEventsMsg(batch)
		}
		timer := time.NewTimer(operationOutputFlushInterval)
		defer timer.Stop()
		for {
			select {
			case event, open := <-events:
				if !open {
					return operationEventsMsg(batch)
				}
				batch = append(batch, event)
				if len(batch) >= maxOperationEventBatch || event.done || event.event.Kind != config.OperationOutput {
					return operationEventsMsg(batch)
				}
			case <-timer.C:
				return operationEventsMsg(batch)
			}
		}
	}
}

func (m Model) updateOperation(msg operationEventsMsg) (tea.Model, tea.Cmd) {
	var final *operationEvent
	var output strings.Builder
	flushOutput := func() {
		if output.Len() == 0 {
			return
		}
		m.operation.log.Append(config.OperationEvent{Kind: config.OperationOutput, Text: output.String()})
		output.Reset()
	}
	for index := range msg {
		item := msg[index]
		if item.event.Kind != "" {
			if item.event.Kind == config.OperationOutput {
				output.WriteString(item.event.Text)
			} else {
				flushOutput()
				if item.event.Kind == config.OperationVersion {
					m.operation.version = item.event.Text
				} else {
					m.operation.log.Append(item.event)
				}
			}
		}
		if item.done {
			final = &item
			break
		}
	}
	flushOutput()
	if final == nil {
		return m, waitOperation(m.operation.events)
	}
	if m.operation.cancel != nil {
		m.operation.cancel()
	}
	duration := time.Duration(0)
	if !m.operation.startedAt.IsZero() {
		duration = time.Since(m.operation.startedAt)
	}
	result := operationResult{
		label:      m.operation.label,
		log:        m.operation.log,
		err:        final.err,
		cancelled:  m.operation.cancelled,
		finishedAt: time.Now(),
		duration:   duration,
	}
	installedVersion := m.operation.version
	if err := saveOperationResult(m.paths, result); err != nil {
		result.log.Append(config.OperationEvent{Kind: config.OperationWarn, Text: "Last result was not saved: " + err.Error()})
	}
	m.last = result
	m.operation = operation{}
	m.screen = screenResult
	m.scroll = 0
	m.showDiagnostics = m.showDiagnostics || (result.err != nil && result.log.hasDiagnostics())
	if m.showDiagnostics {
		m.scroll = m.scrollBound()
	}
	m.cancelUpdatePlanning()
	m.checkingOverview = false
	m.overviewReady = false
	m.overviewError = nil
	if final.err == nil && installedVersion != "" && installedVersion != m.version {
		m.restart = true
		return m, tea.Quit
	}
	return m, m.inspectCmd(true)
}

func (m *Model) cancelOperation() {
	if m.operation.cancel != nil && !m.operation.cancelled {
		m.operation.cancelled = true
		m.operation.cancel()
	}
}
