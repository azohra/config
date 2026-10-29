package config

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
)

type pruneFile struct {
	Label  string
	Path   string
	Digest string
}

// PrunePlan is the exact work Config has previewed. Its details stay private
// so callers can confirm or apply a plan without widening the ownership model.
type PrunePlan struct {
	files         []pruneFile
	warnings      []string
	native        []nativePrune
	configuration string
}

type nativePrune struct {
	Operation nativeOperation
	Preview   string
}

func (p PrunePlan) Empty() bool { return len(p.files) == 0 && len(p.native) == 0 }

func (p PrunePlan) sameWork(other PrunePlan) bool {
	return reflect.DeepEqual(p.files, other.files) && reflect.DeepEqual(p.native, other.native) && p.configuration == other.configuration
}

func (p PrunePlan) Fingerprint() string {
	identity := struct {
		Files         []pruneFile
		Native        []nativePrune
		Configuration string
	}{p.files, p.native, p.configuration}
	data, _ := json.Marshal(identity)
	return fmt.Sprintf("sha256:%x", sha256.Sum256(data))
}

// Pruner previews native cleanup and removes verified Config artifacts.
type Pruner struct {
	Paths   Paths
	Machine Machine
	Runner  Runner
	Log     Logger
	Mise    *NativeMise
}

func NewPruner(paths Paths, machine Machine, out io.Writer) Pruner {
	p := Pruner{Paths: paths, Machine: machine, Runner: NewMachineRunner(paths), Log: Logger{Out: out}}
	if machineUsesMise(paths) {
		native := NewNativeMise(paths, out)
		p.Mise = &native
	}
	return p
}

func (p Pruner) Plan() (PrunePlan, error) {
	files, warnings, err := p.planConfigFiles(nil)
	if err != nil {
		return PrunePlan{}, err
	}
	plan := PrunePlan{files: files}
	if p.Mise != nil {
		if err := p.Mise.check(context.Background()); err != nil {
			warnings = append(warnings, "Native cleanup unavailable; its state is preserved: "+err.Error())
		} else {
			digest, err := p.Mise.configurationDigest(context.Background())
			if err != nil {
				warnings = append(warnings, "Native cleanup unavailable: "+err.Error())
			} else {
				plan.configuration = digest
				plan.native, warnings = p.planNativePrune(warnings)
			}
		}
	}
	slices.Sort(warnings)
	plan.warnings = slices.Compact(warnings)
	return plan, nil
}

func (p Pruner) planNativePrune(warnings []string) ([]nativePrune, []string) {
	operations := []nativeOperation{{"Unused tools and configuration records", []string{"prune", "--yes"}}}
	result := run(p.Mise.Probe, "mise", "bootstrap", "packages", "status", "--json")
	var managers map[string]struct {
		Available bool              `json:"available"`
		Packages  []json.RawMessage `json:"packages"`
	}
	if result.Err != nil || json.Unmarshal([]byte(result.Stdout), &managers) != nil {
		warnings = append(warnings, "Package cleanup unavailable; package inventory is unreadable")
	} else {
		for _, manager := range slices.Sorted(maps.Keys(managers)) {
			if !managers[manager].Available || len(managers[manager].Packages) == 0 {
				continue
			}
			operations = append(operations, nativeOperation{manager + " packages", []string{"bootstrap", "packages", "prune", "--manager", manager, "--yes"}})
		}
	}
	var plans []nativePrune
	for _, operation := range operations {
		result := run(p.Mise.Probe, "mise", append(slices.Clone(operation.Args), "--dry-run")...)
		if result.Err != nil {
			warnings = append(warnings, nativeText(operation.Name+": "+result.Failure().Error()+"; left untouched"))
			continue
		}
		preview := nativeText(result.Stdout + "\n" + result.Stderr)
		if preview == "" || strings.Contains(strings.ToLower(preview), "nothing to prune") {
			continue
		}
		plans = append(plans, nativePrune{Operation: operation, Preview: preview})
	}
	return plans, warnings
}

func (p Pruner) planConfigFiles(warnings []string) ([]pruneFile, []string, error) {
	var planned []pruneFile
	for _, capability := range []struct {
		enabled  bool
		resource string
		label    string
	}{
		{false, "dock", "Dock baseline"},
		{p.Machine.ChromePWAs, chromePWAsID, chromePWAsName + " baseline"},
		{p.Machine.FinderFavorites, finderFavoritesID, finderFavoritesName + " baseline"},
	} {
		if capability.enabled {
			continue
		}
		path := filepath.Join(p.Paths.StateDir, capability.resource+".json")
		candidate, found, warning, err := baselinePruneFile(path, capability.resource, capability.label)
		if err != nil {
			return nil, warnings, err
		}
		if warning != "" {
			warnings = append(warnings, warning)
		}
		if found {
			planned = append(planned, candidate)
		}
	}
	restoreFiles, restoreWarnings, err := p.planRestoreFiles()
	if err != nil {
		return nil, warnings, err
	}
	planned = append(planned, restoreFiles...)
	warnings = append(warnings, restoreWarnings...)
	markers, markerWarnings, err := p.planPendingMarkers()
	if err != nil {
		return nil, warnings, err
	}
	planned = append(planned, markers...)
	warnings = append(warnings, markerWarnings...)
	slices.SortFunc(planned, func(left, right pruneFile) int { return strings.Compare(left.Path, right.Path) })
	return planned, warnings, nil
}

func baselinePruneFile(path, resource, label string) (pruneFile, bool, string, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return pruneFile{}, false, "", nil
	}
	if err != nil {
		return pruneFile{}, false, "", err
	}
	if !info.Mode().IsRegular() {
		return pruneFile{}, false, label + " is not a regular Config state file; left untouched", nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return pruneFile{}, false, "", err
	}
	var baseline Baseline
	if decodeExactJSON(data, &baseline) != nil || baseline.Schema != baselineSchema ||
		baseline.Resource != resource || !json.Valid(baseline.Content) {
		return pruneFile{}, false, label + " is unrecognised; left untouched", nil
	}
	return pruneFile{Label: label, Path: path, Digest: contentDigest(data)}, true, "", nil
}

// planPendingMarkers reclaims the markers left by a run that was killed
// between the two halves of an operation. A marker the machine still declares
// is a step the next apply owes and stays; one for a capability the document
// no longer declares is a step nothing will ever take.
func (p Pruner) planPendingMarkers() ([]pruneFile, []string, error) {
	dir := filepath.Join(p.Paths.StateDir, "pending")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	declared := map[string]bool{}
	for _, preference := range p.Machine.Preferences {
		declared[relaunchMarker(preference.Bundle)] = true
	}
	var planned []pruneFile
	var warnings []string
	for _, entry := range entries {
		name := entry.Name()
		label := "pending " + name
		if declared[name] {
			continue
		}
		path := filepath.Join(dir, name)
		info, statErr := os.Lstat(path)
		if statErr != nil {
			return nil, warnings, statErr
		}
		if !info.Mode().IsRegular() {
			warnings = append(warnings, label+" is not a regular Config state file; left untouched")
			continue
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil, warnings, readErr
		}
		if strings.TrimSpace(string(data)) != name {
			warnings = append(warnings, label+" is unrecognised; left untouched")
			continue
		}
		planned = append(planned, pruneFile{Label: label, Path: path, Digest: contentDigest(data)})
	}
	return planned, warnings, nil
}

func (p Pruner) planRestoreFiles() ([]pruneFile, []string, error) {
	current, found, err := checkoutRestoreIDWithRunner(p.Runner)
	if err != nil {
		return nil, nil, err
	}
	if !found {
		return nil, nil, nil
	}
	dir := filepath.Join(filepath.Dir(p.Paths.Root), "restore")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var planned []pruneFile
	var warnings []string
	for _, entry := range entries {
		name := entry.Name()
		identifier, matched := strings.CutPrefix(name, restoreStateFileStem)
		identifier, matchedSuffix := strings.CutSuffix(identifier, ".json")
		if !matched || !matchedSuffix || !validRestoreID(identifier) {
			continue
		}
		path := filepath.Join(dir, name)
		info, err := os.Lstat(path)
		if err != nil {
			return nil, warnings, err
		}
		if !info.Mode().IsRegular() {
			warnings = append(warnings, name+" is not a regular Config restore record; left untouched")
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, warnings, err
		}
		var record restoreRecord
		if decodeExactJSON(data, &record) != nil || record.validate() != nil || record.Checkout != identifier {
			warnings = append(warnings, name+" is an unrecognised restore record; left untouched")
			continue
		}
		if record.Status == restoreCompleteState && record.Checkout != current {
			planned = append(planned, pruneFile{
				Label: "completed restore record " + record.Checkout,
				Path:  path, Digest: contentDigest(data),
			})
		}
	}
	return planned, warnings, nil
}

// WritePrunePlan renders the complete preview, including anything Config has
// deliberately preserved because its ownership could not be proved.
func WritePrunePlan(out io.Writer, plan PrunePlan) {
	fmt.Fprintln(out, "Prune plan")
	for _, native := range plan.native {
		fmt.Fprintln(out, "\n"+nativeText(native.Operation.Name))
		for line := range strings.SplitSeq(native.Preview, "\n") {
			fmt.Fprintln(out, "  "+line)
		}
	}

	if len(plan.files) > 0 {
		fmt.Fprintln(out, "\nConfig state")
		for _, file := range plan.files {
			fmt.Fprintf(out, "  %s\n", file.Label)
		}
	}
	if plan.Empty() {
		fmt.Fprintln(out, "\n  Nothing to prune.")
	}
	for _, warning := range plan.warnings {
		fmt.Fprintf(out, "  %s %s\n", GlyphWarn, nativeText(warning))
	}
}

func (p Pruner) Apply(expected PrunePlan) error {
	current, err := p.Plan()
	if err != nil {
		return fmt.Errorf("refresh prune plan: %w", err)
	}
	if !expected.sameWork(current) {
		return errors.New("prune plan changed; run config prune again")
	}
	var failures []error
	for _, native := range current.native {
		p.Log.Section(native.Operation.Name)
		if err := p.Mise.Live.Command("mise", native.Operation.Args...); err != nil {
			failures = append(failures, err)
		}
	}

	if len(current.files) > 0 {
		p.Log.Section("Config state")
		for _, file := range current.files {
			if err := applyPruneFile(file); err != nil {
				p.Log.Error(file.Label + ": " + err.Error())
				failures = append(failures, fmt.Errorf("%s: %w", file.Label, err))
			} else {
				p.Log.OK(file.Label + " pruned")
			}
		}
	}
	return errors.Join(failures...)
}

func applyPruneFile(file pruneFile) error {
	info, err := os.Lstat(file.Path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("file is no longer regular")
	}
	data, err := os.ReadFile(file.Path)
	if err != nil {
		return err
	}
	if contentDigest(data) != file.Digest {
		return errors.New("file changed after preview")
	}
	return os.Remove(file.Path)
}
