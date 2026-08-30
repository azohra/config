package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

func dockSnapshotPath(paths Paths) string {
	return paths.InRoot("snapshots", "dock.apps")
}

func (b Bidirectional) dockSaved() (json.RawMessage, []string, []string, bool, error) {
	data, err := os.ReadFile(dockSnapshotPath(b.Paths))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil, false, nil
	}
	if err != nil {
		return nil, nil, nil, false, err
	}
	var all, present []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "~/") {
			line = filepath.Join(b.Paths.Home, strings.TrimPrefix(line, "~/"))
		}
		line = filepath.Clean(line)
		all = append(all, line)
		if info, statErr := os.Stat(line); statErr == nil && info.IsDir() {
			present = append(present, line)
		}
	}
	canonical, err := json.Marshal(present)
	return canonical, all, present, true, err
}

func parseDock(output string) ([]string, error) {
	var apps []string
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 2 {
			return nil, fmt.Errorf("unexpected dockutil output: %s", line)
		}
		if len(fields) >= 3 && fields[2] != "persistentApps" {
			continue
		}
		path := strings.TrimSuffix(strings.TrimPrefix(fields[1], "file://"), "/")
		decoded, err := url.PathUnescape(path)
		if err != nil {
			return nil, err
		}
		apps = append(apps, filepath.Clean(decoded))
	}
	return apps, nil
}

type dockOperation struct {
	Action   string
	Path     string
	Position int
}

func planDock(saved, live []string) []dockOperation {
	current := slices.Clone(live)
	var operations []dockOperation
	for index := len(current) - 1; index >= 0; index-- {
		if slices.Contains(saved, current[index]) {
			continue
		}
		operations = append(operations, dockOperation{Action: "remove", Path: current[index]})
		current = slices.Delete(current, index, index+1)
	}
	for index, path := range saved {
		if index < len(current) && current[index] == path {
			continue
		}
		position := slices.Index(current, path)
		if position < 0 {
			operations = append(operations, dockOperation{Action: "add", Path: path, Position: index + 1})
			current = slices.Insert(current, index, path)
			continue
		}
		operations = append(operations, dockOperation{Action: "move", Path: path, Position: index + 1})
		current = slices.Delete(current, position, position+1)
		current = slices.Insert(current, index, path)
	}
	return operations
}

func (b Bidirectional) dockLive() (json.RawMessage, []string, error) {
	result := run(b.Runner, "dockutil", "--list")
	if result.Err != nil {
		return nil, nil, result.Failure()
	}
	apps, err := parseDock(result.Stdout)
	if err != nil {
		return nil, nil, err
	}
	canonical, err := json.Marshal(apps)
	return canonical, apps, err
}

func dockDiff(saved, live []string) []string {
	var details []string
	for _, app := range live {
		if !slices.Contains(saved, app) {
			details = append(details, "Only on this Mac: "+filepath.Base(app))
		}
	}
	for _, app := range saved {
		if !slices.Contains(live, app) {
			details = append(details, "Only in the saved layout: "+filepath.Base(app))
		}
	}
	if len(details) == 0 && !slices.Equal(saved, live) {
		details = append(details, "The same apps are in a different order")
	}
	return details
}

const (
	dockID   = "dock"
	dockName = "Dock"
)

func (b Bidirectional) InspectDock() Resource {
	resource := Resource{ID: dockID, Name: dockName, Bidirectional: true}
	if !b.Runner.Exists("dockutil") {
		resource.State = Unavailable
		resource.Summary = "dockutil unavailable"
		resource.Checks = []Check{{Label: "dockutil installed", OK: false, Severity: Failure}}
		return resource
	}
	saved, all, present, hasSaved, savedErr := b.dockSaved()
	live, liveApps, liveErr := b.dockLive()
	if savedErr != nil || liveErr != nil {
		resource.State = Unavailable
		resource.Summary = "Dock state unavailable"
		if savedErr != nil {
			resource.Checks = append(resource.Checks, no("saved Dock layout valid", Failure, savedErr.Error()))
		}
		if liveErr != nil {
			resource.Checks = append(resource.Checks, no("Dock layout readable", Failure, liveErr.Error()))
		}
		return resource
	}
	if !hasSaved {
		resource.State = Uncaptured
		resource.Summary = "the Dock layout is not captured"
		resource.Actions = []Action{Capture}
		resource.ActionLabels = map[Action]string{Capture: "Capture this Mac's layout"}
		return resource
	}
	missing := len(all) - len(present)
	baseline, hasBaseline, _ := b.Baselines.Load(resource.ID)
	resource.State = Classify(saved, live, baseline, hasBaseline)
	if resource.State != Current {
		resource.ActionLabels = map[Action]string{Capture: "Save this Mac's layout", Apply: "Restore the saved layout"}
		if missing > 0 {
			resource.Actions = []Action{Capture}
		} else if resource.State == LiveChanged {
			resource.Actions = []Action{Capture, Apply}
		} else {
			resource.Actions = []Action{Apply, Capture}
		}
	}
	resource.Details = dockDiff(present, liveApps)
	if missing > 0 {
		resource.Checks = append(resource.Checks, Check{
			Label:    FormatCount(missing, "saved Dock app unavailable", "saved Dock apps unavailable"),
			Severity: Failure,
		})
		for _, app := range all {
			if !slices.Contains(present, app) {
				resource.Details = append(resource.Details, "Unavailable: "+filepath.Base(app))
			}
		}
	}
	switch resource.State {
	case Current:
		resource.Summary = "this Mac matches the saved layout"
	case SavedChanged:
		resource.Summary = "the saved layout changed"
	case LiveChanged:
		resource.Summary = "the Dock on this Mac changed"
	case Conflict:
		resource.Summary = "the saved layout and this Mac both changed"
	case Unknown:
		resource.Summary = "this Mac and the saved layout differ"
	}
	if missing > 0 && resource.State != Current {
		resource.Summary += " · " + FormatCount(missing, "saved app unavailable", "saved apps unavailable")
	}
	return resource
}

func (b Bidirectional) CaptureDock() error {
	_, live, err := b.dockLive()
	if err != nil {
		return fmt.Errorf("read Dock layout: %w", err)
	}
	var lines []string
	for _, app := range live {
		if strings.HasPrefix(app, b.Paths.Home+string(filepath.Separator)) {
			app = "~/" + strings.TrimPrefix(app, b.Paths.Home+string(filepath.Separator))
		}
		lines = append(lines, app)
	}
	if err := atomicWrite(dockSnapshotPath(b.Paths), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		return err
	}
	return nil
}

func (b Bidirectional) MarkDockIfCurrent() error {
	saved, _, _, hasSaved, savedErr := b.dockSaved()
	live, _, liveErr := b.dockLive()
	if savedErr != nil || liveErr != nil || !hasSaved || string(saved) != string(live) {
		return fmt.Errorf("Dock is not synchronized")
	}
	return b.Baselines.Save(dockID, saved)
}
