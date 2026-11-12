package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const (
	updateRunSchema = 1
	// UpdateRunEnv carries a run's record across Config's release handoff, so
	// the installed release continues the record its predecessor began.
	UpdateRunEnv   = "AZOHRA_CONFIG_UPDATE_RUN"
	keptUpdateRuns = 20
	// shownOutputLines is how much of a failed step's output a report prints.
	shownOutputLines = 30
)

type StepStatus string

const (
	StepDone    StepStatus = "done"
	StepFailed  StepStatus = "failed"
	StepSkipped StepStatus = "skipped"
)

type ItemStatus string

const (
	ItemUpdated   ItemStatus = "updated"
	ItemFailed    ItemStatus = "failed"
	ItemUnchanged ItemStatus = "unchanged"
	ItemUnknown   ItemStatus = "unverified"
)

type ItemResult struct {
	UpdateItem
	Status ItemStatus `json:"status"`
}

// StepResult is what one step of an update did: its command, the outcome of
// each item, mise's error and the end of its output.
type StepResult struct {
	ID       string        `json:"id"`
	Name     string        `json:"name"`
	Status   StepStatus    `json:"status"`
	Command  string        `json:"command,omitempty"`
	Items    []ItemResult  `json:"items,omitempty"`
	Error    string        `json:"error,omitempty"`
	Output   string        `json:"output,omitempty"`
	Duration time.Duration `json:"duration_ns,omitempty"`
}

// UpdateRun is the record of one update.
type UpdateRun struct {
	Schema     int          `json:"schema"`
	Scope      string       `json:"scope"`
	StartedAt  time.Time    `json:"started_at"`
	FinishedAt time.Time    `json:"finished_at"`
	Steps      []StepResult `json:"steps"`
	// Error is why the update stopped when no step failed.
	Error string `json:"error,omitempty"`
}

// Failed reports whether the update stopped or any step failed.
func (r UpdateRun) Failed() bool {
	return r.Error != "" || slices.ContainsFunc(r.Steps, func(step StepResult) bool { return step.Status == StepFailed })
}

// Label names the update the record is of.
func (r UpdateRun) Label() string {
	scope, _ := ParseUpdateScope(r.Scope)
	return scope.Label()
}

func updateRunDir(paths Paths) string { return filepath.Join(paths.StateDir, "updates") }

// updateRunName is a record's file name: the time its update started.
const updateRunName = "20060102T150405.000Z.json"

func (r UpdateRun) name() string { return r.StartedAt.UTC().Format(updateRunName) }

// beginUpdateRun continues the record a release handoff names, or starts one.
// The name is read as the time it encodes, never used as a path.
func beginUpdateRun(paths Paths, scope UpdateScope) UpdateRun {
	if name, continued := os.LookupEnv(UpdateRunEnv); continued {
		_ = os.Unsetenv(UpdateRunEnv)
		if started, err := time.Parse(updateRunName, name); err == nil {
			if run, err := readUpdateRun(filepath.Join(updateRunDir(paths), started.Format(updateRunName))); err == nil {
				return run
			}
		}
	}
	return UpdateRun{Schema: updateRunSchema, Scope: scope.String(), StartedAt: time.Now()}
}

func readUpdateRun(path string) (UpdateRun, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return UpdateRun{}, err
	}
	var run UpdateRun
	if err := json.Unmarshal(data, &run); err != nil || run.Schema != updateRunSchema || run.StartedAt.IsZero() {
		return UpdateRun{}, errors.New("update record is unreadable")
	}
	return run, nil
}

// save writes the record and keeps the newest few.
func (r UpdateRun) save(paths Paths) error {
	if paths.StateDir == "" || !filepath.IsAbs(paths.StateDir) {
		return nil
	}
	dir := updateRunDir(paths)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create update records: %w", err)
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := AtomicWrite(filepath.Join(dir, r.name()), append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("save update record: %w", err)
	}
	names := updateRunNames(dir)
	for _, name := range names[:max(0, len(names)-keptUpdateRuns)] {
		_ = os.Remove(filepath.Join(dir, name))
	}
	return nil
}

// updateRunNames lists the records oldest first; their names sort by time.
func updateRunNames(dir string) []string {
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, entry := range entries {
		if entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), ".json") {
			names = append(names, entry.Name())
		}
	}
	slices.Sort(names)
	return names
}

// LastUpdateRun reads the most recent update record.
func LastUpdateRun(paths Paths) (UpdateRun, error) {
	names := updateRunNames(updateRunDir(paths))
	if len(names) == 0 {
		return UpdateRun{}, errors.New("no update has been recorded on this Mac")
	}
	return readUpdateRun(filepath.Join(updateRunDir(paths), names[len(names)-1]))
}

// WriteUpdateRun renders a record: each step, each item's outcome, and for a
// failed step mise's error, its command and the end of its output.
func WriteUpdateRun(out io.Writer, run UpdateRun) {
	heading := run.Label() + " · " + run.StartedAt.Local().Format("Jan 2, 2006 at 3:04 PM")
	if !run.FinishedAt.IsZero() {
		heading += " · " + run.FinishedAt.Sub(run.StartedAt).Round(time.Second).String()
	}
	fmt.Fprintln(out, heading)
	WriteUpdateSteps(out, run)
}

// WriteUpdateSteps renders a record without its heading.
func WriteUpdateSteps(out io.Writer, run UpdateRun) {
	if run.Error != "" {
		fmt.Fprintf(out, "  %s %s\n", GlyphError, run.Error)
	}
	for _, step := range run.Steps {
		glyph := GlyphOK
		switch step.Status {
		case StepFailed:
			glyph = GlyphError
		case StepSkipped:
			glyph = GlyphWarn
		}
		line := fmt.Sprintf("  %s %s", glyph, step.Name)
		if updated := step.count(ItemUpdated); len(step.Items) > 0 {
			line += fmt.Sprintf(": %d of %d updated", updated, len(step.Items))
		} else if step.Status != StepDone {
			line += ": " + string(step.Status)
		}
		if step.Duration >= time.Second {
			line += " · " + step.Duration.Round(time.Second).String()
		}
		fmt.Fprintln(out, line)
		for _, item := range step.Items {
			glyph := GlyphOK
			switch item.Status {
			case ItemFailed:
				glyph = GlyphError
			case ItemUnchanged, ItemUnknown:
				glyph = GlyphWarn
			}
			line := fmt.Sprintf("      %s %s", glyph, item.Label())
			if item.Status != ItemUpdated {
				line += ": " + string(item.Status)
			}
			fmt.Fprintln(out, line)
		}
		if step.Error == "" {
			continue
		}
		fmt.Fprintf(out, "      error: %s\n", step.Error)
		if step.Command != "" {
			fmt.Fprintf(out, "      command: %s\n", step.Command)
		}
		if step.Output != "" {
			lines := strings.Split(step.Output, "\n")
			fmt.Fprintln(out, "      output:")
			if len(lines) > shownOutputLines {
				fmt.Fprintf(out, "        … %d earlier lines; --json has them\n", len(lines)-shownOutputLines)
				lines = lines[len(lines)-shownOutputLines:]
			}
			for _, line := range lines {
				fmt.Fprintf(out, "        %s\n", line)
			}
		}
	}
}

func (s StepResult) count(status ItemStatus) int {
	count := 0
	for _, item := range s.Items {
		if item.Status == status {
			count++
		}
	}
	return count
}
