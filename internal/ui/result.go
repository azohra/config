package ui

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/azohra/config/internal/config"
)

const operationResultSchema = 3

type storedOutputSpan struct {
	Kind config.OperationEventKind `json:"kind,omitempty"`
	Text string                    `json:"text"`
}

type storedOperationResult struct {
	Schema             int                `json:"schema"`
	Label              string             `json:"label"`
	Output             string             `json:"output,omitempty"`
	Spans              []storedOutputSpan `json:"spans,omitempty"`
	Progress           []storedOutputSpan `json:"progress,omitempty"`
	Diagnostics        string             `json:"diagnostics,omitempty"`
	ProgressOmitted    int                `json:"progress_omitted,omitempty"`
	DiagnosticsOmitted int                `json:"diagnostics_omitted,omitempty"`
	Error              string             `json:"error,omitempty"`
	Cancelled          bool               `json:"cancelled,omitempty"`
	FinishedAt         time.Time          `json:"finished_at"`
	DurationMS         int64              `json:"duration_ms,omitempty"`
}

func operationResultPath(paths config.Paths) string {
	return filepath.Join(paths.StateDir, "last-operation.json")
}

func loadOperationResult(paths config.Paths) operationResult {
	if paths.StateDir == "" || !filepath.IsAbs(paths.StateDir) {
		return operationResult{}
	}
	data, err := os.ReadFile(operationResultPath(paths))
	if err != nil {
		return operationResult{}
	}
	var stored storedOperationResult
	if json.Unmarshal(data, &stored) != nil || (stored.Schema < 1 || stored.Schema > operationResultSchema) || stored.Label == "" {
		return operationResult{}
	}
	if stored.ProgressOmitted < 0 || stored.DiagnosticsOmitted < 0 || stored.DurationMS < 0 {
		return operationResult{}
	}
	log := newOperationLog()
	switch stored.Schema {
	case 1:
		log.diagnostics = outputFromString(stored.Output)
		log.diagnostics.maxBytes = maxOperationDiagnosticBytes
		log.diagnostics.maxLines = maxOperationDiagnosticLines
		log.diagnostics.bound()
	case 2:
		for _, span := range stored.Spans {
			if !storedSpanKind(span.Kind) {
				return operationResult{}
			}
			// Schema 2 stored a rendered transcript: the spaces and message
			// around a typed glyph were ordinary output spans. Keep that
			// transcript intact instead of manufacturing partial progress.
			log.diagnostics.appendText(config.OperationOutput, span.Text)
		}
		log.diagnostics.bound()
	case operationResultSchema:
		for _, span := range stored.Progress {
			if !storedSpanKind(span.Kind) {
				return operationResult{}
			}
			log.progress.appendText(span.Kind, span.Text)
		}
		log.progress.bound()
		log.diagnostics.appendText(config.OperationOutput, stored.Diagnostics)
		log.diagnostics.bound()
		log.progress.omittedLines += stored.ProgressOmitted
		log.diagnostics.omittedLines += stored.DiagnosticsOmitted
	}
	log.activity = lastNonblankLine(log.diagnostics.String())
	result := operationResult{
		label: stored.Label, log: log, cancelled: stored.Cancelled, finishedAt: stored.FinishedAt,
		duration: time.Duration(stored.DurationMS) * time.Millisecond,
	}
	if stored.Error != "" {
		result.err = errors.New(stored.Error)
	}
	return result
}

func saveOperationResult(paths config.Paths, result operationResult) error {
	if paths.StateDir == "" || !filepath.IsAbs(paths.StateDir) {
		return nil
	}
	if err := os.MkdirAll(paths.StateDir, 0o700); err != nil {
		return fmt.Errorf("create Config state: %w", err)
	}
	stored := storedOperationResult{
		Schema: operationResultSchema, Label: result.label,
		Cancelled: result.cancelled, FinishedAt: result.finishedAt, DurationMS: result.duration.Milliseconds(),
		ProgressOmitted: result.log.progress.omittedLines, DiagnosticsOmitted: result.log.diagnostics.omittedLines,
		Diagnostics: result.log.diagnostics.String(),
	}
	for _, span := range result.log.progress.spans {
		stored.Progress = append(stored.Progress, storedOutputSpan{Kind: span.kind, Text: string(span.text)})
	}
	if result.err != nil {
		stored.Error = result.err.Error()
	}
	data, err := json.Marshal(stored)
	if err != nil {
		return fmt.Errorf("encode operation result: %w", err)
	}
	if err := config.AtomicWrite(operationResultPath(paths), append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("save operation result: %w", err)
	}
	return nil
}

func storedSpanKind(kind config.OperationEventKind) bool {
	return kind == config.OperationOutput || kind == config.OperationOK || kind == config.OperationInfo ||
		kind == config.OperationWarn || kind == config.OperationError
}
