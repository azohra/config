package config

import (
	"bytes"
	"encoding/json"
	"io"
	"sync"
	"unicode/utf8"
)

const OperationEventsEnv = "AZOHRA_CONFIG_OPERATION_EVENTS"

// OperationEventKind identifies presentation owned by Config. Output is
// provider-owned terminal output; the other kinds are stable Config events.
type OperationEventKind string

const (
	OperationOutput  OperationEventKind = "output"
	OperationSection OperationEventKind = "section"
	OperationOK      OperationEventKind = "ok"
	OperationInfo    OperationEventKind = "info"
	OperationWarn    OperationEventKind = "warn"
	OperationError   OperationEventKind = "error"
	OperationVersion OperationEventKind = "version"
)

type OperationEvent struct {
	Kind OperationEventKind `json:"kind"`
	Text string             `json:"text"`
}

type operationEventSink interface {
	OperationEvent(OperationEvent) error
}

// OperationEventWriter frames arbitrary provider output and typed Config
// events as JSON lines for the terminal app's child-process boundary.
type OperationEventWriter struct {
	mu      sync.Mutex
	out     io.Writer
	pending []byte
}

func NewOperationEventWriter(out io.Writer) *OperationEventWriter {
	return &OperationEventWriter{out: out}
}

func (w *OperationEventWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	length := len(data)
	w.pending = append(w.pending, data...)
	complete := 0
	for complete < len(w.pending) && utf8.FullRune(w.pending[complete:]) {
		_, size := utf8.DecodeRune(w.pending[complete:])
		complete += size
	}
	if complete == 0 {
		return length, nil
	}
	text := string(bytes.ToValidUTF8(w.pending[:complete], []byte("\uFFFD")))
	if err := w.writeLocked(OperationEvent{Kind: OperationOutput, Text: text}); err != nil {
		return 0, err
	}
	w.pending = append(w.pending[:0], w.pending[complete:]...)
	return length, nil
}

func (w *OperationEventWriter) OperationEvent(event OperationEvent) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.flushLocked(); err != nil {
		return err
	}
	return w.writeLocked(event)
}

func (w *OperationEventWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.flushLocked()
}

func (w *OperationEventWriter) flushLocked() error {
	if len(w.pending) == 0 {
		return nil
	}
	text := string(bytes.ToValidUTF8(w.pending, []byte("\uFFFD")))
	w.pending = nil
	return w.writeLocked(OperationEvent{Kind: OperationOutput, Text: text})
}

func (w *OperationEventWriter) writeLocked(event OperationEvent) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = w.out.Write(data)
	return err
}
