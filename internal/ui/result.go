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

const operationResultSchema = 2

type storedOutputSpan struct {
	Kind config.OperationEventKind `json:"kind,omitempty"`
	Text string                    `json:"text"`
}

type storedOperationResult struct {
	Schema     int                `json:"schema"`
	Label      string             `json:"label"`
	Output     string             `json:"output,omitempty"`
	Spans      []storedOutputSpan `json:"spans,omitempty"`
	Error      string             `json:"error,omitempty"`
	Cancelled  bool               `json:"cancelled,omitempty"`
	FinishedAt time.Time          `json:"finished_at"`
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
	if json.Unmarshal(data, &stored) != nil || (stored.Schema != 1 && stored.Schema != operationResultSchema) || stored.Label == "" {
		return operationResult{}
	}
	output := outputFromString(stored.Output)
	if stored.Schema == operationResultSchema {
		output = terminalOutput{}
		for _, span := range stored.Spans {
			if span.Kind != config.OperationOutput && span.Kind != config.OperationOK && span.Kind != config.OperationInfo &&
				span.Kind != config.OperationWarn && span.Kind != config.OperationError {
				return operationResult{}
			}
			output.appendText(span.Kind, span.Text)
		}
		output.bound()
	}
	result := operationResult{
		label: stored.Label, output: output, cancelled: stored.Cancelled, finishedAt: stored.FinishedAt,
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
		Cancelled: result.cancelled, FinishedAt: result.finishedAt,
	}
	for _, span := range result.output.spans {
		stored.Spans = append(stored.Spans, storedOutputSpan{Kind: span.kind, Text: string(span.text)})
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
