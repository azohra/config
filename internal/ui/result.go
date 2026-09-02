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

const operationResultSchema = 1

type storedOperationResult struct {
	Schema     int       `json:"schema"`
	Label      string    `json:"label"`
	Output     string    `json:"output"`
	Error      string    `json:"error,omitempty"`
	Cancelled  bool      `json:"cancelled,omitempty"`
	FinishedAt time.Time `json:"finished_at"`
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
	if json.Unmarshal(data, &stored) != nil || stored.Schema != operationResultSchema || stored.Label == "" {
		return operationResult{}
	}
	result := operationResult{
		label: stored.Label, output: stored.Output, cancelled: stored.Cancelled, finishedAt: stored.FinishedAt,
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
		Schema: operationResultSchema, Label: result.label, Output: result.output,
		Cancelled: result.cancelled, FinishedAt: result.finishedAt,
	}
	if result.err != nil {
		stored.Error = result.err.Error()
	}
	data, err := json.Marshal(stored)
	if err != nil {
		return fmt.Errorf("encode operation result: %w", err)
	}
	path := operationResultPath(paths)
	staged, err := os.CreateTemp(paths.StateDir, ".last-operation-*")
	if err != nil {
		return fmt.Errorf("stage operation result: %w", err)
	}
	name := staged.Name()
	defer os.Remove(name)
	if err := staged.Chmod(0o600); err != nil {
		staged.Close()
		return fmt.Errorf("secure operation result: %w", err)
	}
	if _, err := staged.Write(append(data, '\n')); err != nil {
		staged.Close()
		return fmt.Errorf("write operation result: %w", err)
	}
	if err := staged.Close(); err != nil {
		return fmt.Errorf("close operation result: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("publish operation result: %w", err)
	}
	return nil
}
