package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
)

type commandRunner interface {
	Command(name string, args ...string) error
}

type pruneLink struct {
	Path   string
	Target string
}

type pruneRegistry struct {
	Tracked []pruneLink
	Trusted []pruneLink
}

type pruneTool struct {
	Name    string
	Version string
}

type prunePackageManager struct {
	Name    string
	Preview []string
}

type pruneFile struct {
	Label  string
	Path   string
	Digest string
}

type pruneCacheFile struct {
	Name string
	Size int64
}

type pruneCache struct {
	Label string
	Dir   string
	Files []pruneCacheFile
}

func (c pruneCache) bytes() int64 {
	var total int64
	for _, file := range c.Files {
		total += file.Size
	}
	return total
}

type pruneHook struct {
	Name       string
	Digest     string
	RemoveFile bool
}

type pruneHookTarget struct {
	Name           string
	Dir            string
	ManifestDigest string
	Hooks          []pruneHook
}

type pruneAgentSkill struct {
	Name         string
	RemoveAgents []string
	ForgetAgents []string
}

type pruneAgentSkills struct {
	ManifestDigest string
	Skills         []pruneAgentSkill
}

// PrunePlan is the exact work Config has previewed. Its details stay private
// so callers can confirm or apply a plan without widening the ownership model.
type PrunePlan struct {
	registry        pruneRegistry
	tools           []pruneTool
	packages        []prunePackageManager
	caches          []pruneCache
	files           []pruneFile
	hooks           []pruneHookTarget
	agentSkills     pruneAgentSkills
	warnings        []string
	skippedManagers []string
}

func (p PrunePlan) Empty() bool {
	return len(p.registry.Tracked) == 0 && len(p.registry.Trusted) == 0 &&
		len(p.tools) == 0 && len(p.packages) == 0 && len(p.caches) == 0 && len(p.files) == 0 &&
		len(p.hooks) == 0 && len(p.agentSkills.Skills) == 0
}

func (p PrunePlan) sameWork(other PrunePlan) bool {
	return reflect.DeepEqual(p.registry, other.registry) &&
		reflect.DeepEqual(p.tools, other.tools) &&
		reflect.DeepEqual(p.packages, other.packages) &&
		reflect.DeepEqual(p.caches, other.caches) &&
		reflect.DeepEqual(p.files, other.files) &&
		reflect.DeepEqual(p.hooks, other.hooks) &&
		reflect.DeepEqual(p.agentSkills, other.agentSkills)
}

// Pruner delegates shared inventory decisions to mise and removes only local
// state whose Config ownership can be proved from its own records.
type Pruner struct {
	Paths        Paths
	Machine      Machine
	Runner       Runner
	Mise         Runner
	MiseLive     commandRunner
	Skills       Runner
	SkillsLive   commandRunner
	MiseStateDir string
	MiseCacheDir string
	Log          Logger
}

func NewPruner(paths Paths, machine Machine, out io.Writer) Pruner {
	miseLive := newMiseLiveRunner(paths)
	miseLive.Stdout, miseLive.Stderr = out, out
	skillsLive := newAgentSkillsLiveRunner(paths)
	skillsLive.Stdout, skillsLive.Stderr = out, out
	return Pruner{
		Paths:      paths,
		Machine:    machine,
		Runner:     NewMachineRunner(paths),
		Mise:       NewMiseRunner(paths),
		MiseLive:   miseLive,
		Skills:     newAgentSkillsRunner(paths),
		SkillsLive: skillsLive,
		Log:        Logger{Out: out},
	}
}

// miseDirs asks mise where its own state and cache live. Config once derived
// the state path from the environment and then forced it onto the subprocesses
// that delete tools and packages, which made the safety of every deletion
// depend on Config reproducing a path mise owns.
func miseDirs(runner Runner) (state, cache string, err error) {
	result := run(runner, "mise", "doctor", "--json")
	var report struct {
		Dirs struct {
			State string `json:"state"`
			Cache string `json:"cache"`
		} `json:"dirs"`
	}
	// doctor exits non-zero when it has findings to report, and the document
	// it prints is still the answer.
	if err := json.Unmarshal([]byte(result.Stdout), &report); err != nil {
		if result.Err != nil {
			return "", "", fmt.Errorf("locate mise directories: %w", result.Failure())
		}
		return "", "", fmt.Errorf("locate mise directories: %w", err)
	}
	if !filepath.IsAbs(report.Dirs.State) {
		return "", "", fmt.Errorf("locate mise state: mise reported %q", report.Dirs.State)
	}
	if !filepath.IsAbs(report.Dirs.Cache) {
		return "", "", fmt.Errorf("locate mise cache: mise reported %q", report.Dirs.Cache)
	}
	return filepath.Clean(report.Dirs.State), filepath.Clean(report.Dirs.Cache), nil
}

func (p Pruner) Plan() (PrunePlan, error) {
	var plan PrunePlan
	var repositories []miseRepository
	var miseErr error
	if p.Machine.Mise {
		miseErr = requireMiseConfigBinding(p.Paths)
		if miseErr == nil {
			miseErr = requireTestedMise(p.Mise)
		}
		if miseErr != nil {
			plan.warnings = append(plan.warnings, "Mise cleanup is unavailable; its state was left untouched: "+miseErr.Error())
		}
	}
	if p.Machine.Mise && miseErr == nil {
		stateDir, cacheDir := p.MiseStateDir, p.MiseCacheDir
		if stateDir == "" || cacheDir == "" {
			reportedState, reportedCache, err := miseDirs(p.Mise)
			if err != nil {
				plan.warnings = append(plan.warnings, "Mise directory cleanup is unavailable: "+err.Error())
			}
			if stateDir == "" {
				stateDir = reportedState
			}
			if cacheDir == "" {
				cacheDir = reportedCache
			}
		}
		if stateDir != "" {
			var err error
			plan.registry, err = p.planRegistry(stateDir)
			if err != nil {
				plan.warnings = append(plan.warnings, "Mise configuration cleanup is unavailable: "+err.Error())
			}
		}
		if cacheDir != "" {
			var err error
			plan.caches, plan.warnings, err = p.planCaches(cacheDir, plan.warnings)
			if err != nil {
				plan.warnings = append(plan.warnings, "Mise download cache cleanup is unavailable: "+err.Error())
			}
		}
		var err error
		plan.tools, plan.warnings, err = p.planTools(plan.warnings)
		if err != nil {
			plan.warnings = append(plan.warnings, "Mise tool cleanup is unavailable: "+err.Error())
		}
		plan.packages, plan.skippedManagers, plan.warnings, err = p.planPackages(plan.warnings)
		if err != nil {
			plan.warnings = append(plan.warnings, "package cleanup is unavailable: "+err.Error())
		}
		repositories, err = miseRepositories(p.Paths, p.Mise)
		if err != nil {
			miseErr = err
		}
	}
	if p.Machine.Mise && miseErr != nil {
		plan.warnings = append(plan.warnings, "Mise repository inventory is unavailable: "+miseErr.Error())
	}
	var err error
	plan.hooks, plan.warnings, err = p.planHooks(plan.warnings, repositories, miseErr)
	if err != nil {
		return PrunePlan{}, err
	}
	plan.agentSkills, plan.warnings = p.planAgentSkills(plan.warnings)
	plan.files, plan.warnings, err = p.planConfigFiles(plan.warnings)
	if err != nil {
		return PrunePlan{}, err
	}
	slices.Sort(plan.warnings)
	plan.warnings = slices.Compact(plan.warnings)
	slices.Sort(plan.skippedManagers)
	plan.skippedManagers = slices.Compact(plan.skippedManagers)
	return plan, nil
}

func (p Pruner) planAgentSkills(warnings []string) (pruneAgentSkills, []string) {
	manifest, data, err := readAgentSkillManifest(p.Paths)
	if err != nil {
		return pruneAgentSkills{}, append(warnings, "Agent skill ownership is unrecognised; left untouched")
	}
	if len(data) == 0 || len(manifest.Skills) == 0 {
		return pruneAgentSkills{}, warnings
	}
	agentSet := map[string]bool{}
	for _, ownership := range manifest.Skills {
		for _, agent := range ownership.Agents {
			agentSet[agent] = true
		}
	}
	var desired map[string]agentSkillDeclaration
	if p.Machine.AgentSkills != nil {
		desired = p.Machine.AgentSkills.desired()
		for _, agent := range p.Machine.AgentSkills.Agents {
			agentSet[agent] = true
		}
	}
	agents := make([]string, 0, len(agentSet))
	for agent := range agentSet {
		agents = append(agents, agent)
	}
	slices.Sort(agents)
	inventory, err := loadAgentSkillInventory(p.Paths, p.Skills, agents, true)
	if err != nil {
		return pruneAgentSkills{}, append(warnings, "Agent skill cleanup is unavailable; its state was left untouched: "+err.Error())
	}
	plan := pruneAgentSkills{ManifestDigest: contentDigest(data)}
	names := make([]string, 0, len(manifest.Skills))
	for name := range manifest.Skills {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		ownership := manifest.Skills[name]
		var candidates []string
		if declaration, declared := desired[name]; declared {
			if !sameAgentSkillSource(ownership.Source, declaration.Source) {
				continue
			}
			for _, agent := range ownership.Agents {
				if !slices.Contains(declaration.Agents, agent) {
					candidates = append(candidates, agent)
				}
			}
		} else {
			candidates = append(candidates, ownership.Agents...)
		}
		if len(candidates) == 0 {
			continue
		}
		live, found := inventory.Global[name]
		if found {
			digest, digestErr := agentSkillDirectoryDigest(live.Path)
			if !sameAgentSkillSource(live.locator(), ownership.Source) || live.Path != ownership.Path ||
				digestErr != nil || digest != ownership.Digest {
				warnings = append(warnings, name+" changed since Config installed it; left untouched")
				continue
			}
		}
		item := pruneAgentSkill{Name: name}
		for _, agent := range candidates {
			installed, present := inventory.Agents[agent][name]
			if present && !sameAgentSkillSource(installed.locator(), ownership.Source) {
				warnings = append(warnings, name+" for "+agent+" is no longer Config-owned; left untouched")
				continue
			}
			item.ForgetAgents = append(item.ForgetAgents, agent)
			if present {
				item.RemoveAgents = append(item.RemoveAgents, agent)
			}
		}
		if len(item.ForgetAgents) > 0 {
			plan.Skills = append(plan.Skills, item)
		}
	}
	return plan, warnings
}

func (p Pruner) planRegistry(stateDir string) (pruneRegistry, error) {
	tracked, err := deadConfigLinks(filepath.Join(stateDir, "tracked-configs"))
	if err != nil {
		return pruneRegistry{}, fmt.Errorf("inspect mise tracked configurations: %w", err)
	}
	trusted, err := deadConfigLinks(filepath.Join(stateDir, "trusted-configs"))
	if err != nil {
		return pruneRegistry{}, fmt.Errorf("inspect mise trusted configurations: %w", err)
	}
	return pruneRegistry{Tracked: tracked, Trusted: trusted}, nil
}

// systemBrewCaches is deliberately not every directory under system-brew: the
// cask-extract sibling is where a payload is unpacked on its way into place,
// so an install is using it.
var systemBrewCaches = []struct{ dir, label string }{
	{"casks", "Homebrew cask downloads"},
	{"bottles", "Homebrew bottle downloads"},
}

func (p Pruner) planCaches(cacheDir string, warnings []string) ([]pruneCache, []string, error) {
	root := filepath.Join(cacheDir, "system-brew")
	var planned []pruneCache
	for _, cache := range systemBrewCaches {
		dir := filepath.Join(root, cache.dir)
		files, warning, err := cacheDownloads(dir)
		if err != nil {
			return nil, warnings, fmt.Errorf("inspect %s: %w", cache.label, err)
		}
		if warning != "" {
			warnings = append(warnings, cache.label+" "+warning)
		}
		if len(files) > 0 {
			planned = append(planned, pruneCache{Label: cache.label, Dir: dir, Files: files})
		}
	}
	return planned, warnings, nil
}

func cacheDownloads(dir string) ([]pruneCacheFile, string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	var files []pruneCacheFile
	var foreign bool
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			foreign = true
			continue
		}
		info, err := entry.Info()
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, "", err
		}
		files = append(files, pruneCacheFile{Name: entry.Name(), Size: info.Size()})
	}
	slices.SortFunc(files, func(left, right pruneCacheFile) int { return strings.Compare(left.Name, right.Name) })
	var warning string
	if foreign {
		warning = "holds entries that are not downloaded files; left untouched"
	}
	return files, warning, nil
}

func deadConfigLinks(dir string) ([]pruneLink, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var links []pruneLink
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink == 0 {
			continue
		}
		target, err := os.Readlink(path)
		if err != nil {
			return nil, err
		}
		resolved := target
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(dir, resolved)
		}
		if _, err := os.Stat(resolved); errors.Is(err, os.ErrNotExist) {
			links = append(links, pruneLink{Path: path, Target: target})
		} else if err != nil {
			return nil, err
		}
	}
	slices.SortFunc(links, func(left, right pruneLink) int { return strings.Compare(left.Path, right.Path) })
	return links, nil
}

func (p Pruner) planTools(warnings []string) ([]pruneTool, []string, error) {
	result := run(p.Mise, "mise", "ls", "--prunable", "-J")
	warnings = appendMiseWarnings(warnings, result.Stderr)
	if result.Err != nil {
		return nil, warnings, fmt.Errorf("list prunable mise tools: %w", result.Failure())
	}
	var listing map[string][]struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal([]byte(result.Stdout), &listing); err != nil {
		return nil, warnings, fmt.Errorf("read prunable mise tools: %w", err)
	}
	var tools []pruneTool
	for name, versions := range listing {
		for _, version := range versions {
			if strings.TrimSpace(name) == "" || strings.TrimSpace(version.Version) == "" {
				return nil, warnings, errors.New("read prunable mise tools: tool name or version is empty")
			}
			tools = append(tools, pruneTool{Name: name, Version: version.Version})
		}
	}
	slices.SortFunc(tools, func(left, right pruneTool) int {
		if order := strings.Compare(left.Name, right.Name); order != 0 {
			return order
		}
		return strings.Compare(left.Version, right.Version)
	})
	return tools, warnings, nil
}

func (p Pruner) planPackages(warnings []string) ([]prunePackageManager, []string, []string, error) {
	status := run(p.Mise, "mise", "bootstrap", "packages", "status", "--json")
	warnings = appendMiseWarnings(warnings, status.Stderr)
	if status.Err != nil {
		return nil, nil, warnings, fmt.Errorf("list mise package managers: %w", status.Failure())
	}
	var managers map[string]json.RawMessage
	if err := json.Unmarshal([]byte(status.Stdout), &managers); err != nil {
		return nil, nil, warnings, fmt.Errorf("read mise package managers: %w", err)
	}
	names := make([]string, 0, len(managers))
	for name := range managers {
		if strings.TrimSpace(name) == "" {
			return nil, nil, warnings, errors.New("read mise package managers: manager name is empty")
		}
		names = append(names, name)
	}
	slices.Sort(names)
	var planned []prunePackageManager
	var skipped []string
	for _, name := range names {
		result := run(p.Mise, "mise", "bootstrap", "packages", "prune", "--manager", name, "--dry-run")
		warnings = appendMiseWarnings(warnings, result.Stderr)
		if result.Err != nil {
			if strings.Contains(result.Stderr, "does not support pruning") ||
				strings.Contains(result.Stderr, "unknown bootstrap package manager") {
				skipped = append(skipped, name)
				continue
			}
			return nil, skipped, warnings, fmt.Errorf("preview %s package pruning: %w", name, result.Failure())
		}
		preview := packagePreview(name, result.Stdout+"\n"+result.Stderr)
		if len(preview) > 0 {
			planned = append(planned, prunePackageManager{Name: name, Preview: preview})
		}
	}
	return planned, skipped, warnings, nil
}

func packagePreview(manager, output string) []string {
	var preview []string
	for line := range strings.SplitSeq(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.Contains(line, "mise WARN") || line == "mise "+manager+": nothing to prune" {
			continue
		}
		preview = append(preview, strings.TrimPrefix(line, "mise "+manager+": "))
	}
	slices.Sort(preview)
	return slices.Compact(preview)
}

func appendMiseWarnings(warnings []string, output string) []string {
	for line := range strings.SplitSeq(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "mise WARN") {
			warnings = append(warnings, line)
		}
	}
	return warnings
}

func (p Pruner) planHooks(warnings []string, repositories []miseRepository, inventoryErr error) ([]pruneHookTarget, []string, error) {
	if inventoryErr != nil {
		warnings = append(warnings, "declared repository hook cleanup is unavailable; only the managed checkout and Git template were inspected")
	}
	targets, err := repositoryHookTargets(p.Paths, p.Runner, repositories, true)
	if err != nil {
		return nil, warnings, fmt.Errorf("inspect repository hook targets: %w", err)
	}
	declared := make(map[string]bool, len(p.Machine.RepositoryHooks))
	for _, hook := range p.Machine.RepositoryHooks {
		declared[hook.Name] = true
	}
	var planned []pruneHookTarget
	for _, target := range targets {
		manifestPath := filepath.Join(target.Dir, repositoryHookManifestName)
		if _, err := os.Lstat(manifestPath); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, warnings, fmt.Errorf("inspect hook ownership for %s: %w", target.Name, err)
		}
		conflict, err := repositoryHookTargetConflict(target)
		if err != nil {
			return nil, warnings, err
		}
		if conflict != "" {
			warnings = append(warnings, target.Name+" hook ownership is inaccessible: "+conflict+"; left untouched")
			continue
		}
		manifestBytes, err := os.ReadFile(manifestPath)
		if err != nil {
			return nil, warnings, fmt.Errorf("read hook ownership for %s: %w", target.Name, err)
		}
		manifest, err := readRepositoryHookManifest(target.Dir)
		if err != nil {
			warnings = append(warnings, target.Name+" hook ownership is unrecognised; left untouched")
			continue
		}
		names := make([]string, 0, len(manifest.Hooks))
		for name := range manifest.Hooks {
			names = append(names, name)
		}
		slices.Sort(names)
		candidate := pruneHookTarget{Name: target.Name, Dir: target.Dir, ManifestDigest: contentDigest(manifestBytes)}
		for _, name := range names {
			if declared[name] {
				continue
			}
			digest := manifest.Hooks[name]
			path := filepath.Join(target.Dir, name)
			info, err := os.Lstat(path)
			switch {
			case errors.Is(err, os.ErrNotExist):
				candidate.Hooks = append(candidate.Hooks, pruneHook{Name: name, Digest: digest})
			case err != nil:
				return nil, warnings, fmt.Errorf("inspect %s hook %s: %w", target.Name, name, err)
			case !info.Mode().IsRegular():
				warnings = append(warnings, target.Name+": "+name+" is no longer a regular Config-installed hook; left untouched")
			default:
				body, readErr := os.ReadFile(path)
				if readErr != nil {
					return nil, warnings, fmt.Errorf("read %s hook %s: %w", target.Name, name, readErr)
				}
				if contentDigest(body) != digest {
					warnings = append(warnings, target.Name+": "+name+" changed since Config installed it; left untouched")
					continue
				}
				candidate.Hooks = append(candidate.Hooks, pruneHook{Name: name, Digest: digest, RemoveFile: true})
			}
		}
		if len(candidate.Hooks) > 0 {
			planned = append(planned, candidate)
		}
	}
	slices.SortFunc(planned, func(left, right pruneHookTarget) int { return strings.Compare(left.Dir, right.Dir) })
	return planned, warnings, nil
}

func (p Pruner) planConfigFiles(warnings []string) ([]pruneFile, []string, error) {
	var planned []pruneFile
	for _, capability := range []struct {
		enabled  bool
		resource string
		label    string
	}{
		{p.Machine.Dock, dockID, dockName + " baseline"},
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
	declared := map[string]bool{dockRestartMarker: p.Machine.Dock}
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
	if len(plan.registry.Tracked) > 0 || len(plan.registry.Trusted) > 0 {
		fmt.Fprintln(out, "\nMise configuration registry")
		if len(plan.registry.Tracked) > 0 {
			fmt.Fprintf(out, "  %s\n", FormatCount(len(plan.registry.Tracked), "dead tracked link", "dead tracked links"))
		}
		if len(plan.registry.Trusted) > 0 {
			fmt.Fprintf(out, "  %s\n", FormatCount(len(plan.registry.Trusted), "dead trust link", "dead trust links"))
		}
	}
	if len(plan.tools) > 0 {
		fmt.Fprintln(out, "\nMise tools")
		for _, tool := range plan.tools {
			fmt.Fprintf(out, "  %s@%s\n", tool.Name, tool.Version)
		}
	}
	if len(plan.packages) > 0 {
		fmt.Fprintln(out, "\nPackages")
		for _, manager := range plan.packages {
			for _, line := range manager.Preview {
				fmt.Fprintf(out, "  %s: %s\n", manager.Name, line)
			}
		}
	}
	if len(plan.caches) > 0 {
		fmt.Fprintln(out, "\nMise download cache")
		for _, cache := range plan.caches {
			fmt.Fprintf(out, "  %s: %s, %s\n", cache.Label,
				FormatCount(len(cache.Files), "file", "files"), FormatBytes(cache.bytes()))
		}
	}
	if len(plan.agentSkills.Skills) > 0 {
		fmt.Fprintln(out, "\nAgent skills")
		for _, skill := range plan.agentSkills.Skills {
			fmt.Fprintf(out, "  %s (%s)\n", skill.Name, strings.Join(skill.ForgetAgents, ", "))
		}
	}
	if len(plan.hooks) > 0 || len(plan.files) > 0 {
		fmt.Fprintln(out, "\nConfig state")
		for _, target := range plan.hooks {
			for _, hook := range target.Hooks {
				action := "ownership record"
				if hook.RemoveFile {
					action = "installed hook and ownership record"
				}
				fmt.Fprintf(out, "  %s: %s (%s)\n", target.Name, hook.Name, action)
			}
		}
		for _, file := range plan.files {
			fmt.Fprintf(out, "  %s\n", file.Label)
		}
	}
	if plan.Empty() {
		fmt.Fprintln(out, "\n  Nothing to prune.")
	}
	for _, manager := range plan.skippedManagers {
		fmt.Fprintf(out, "  %s %s does not support pruning; left untouched\n", GlyphInfo, manager)
	}
	for _, warning := range plan.warnings {
		fmt.Fprintf(out, "  %s %s\n", GlyphWarn, warning)
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
	if len(current.registry.Tracked) > 0 || len(current.registry.Trusted) > 0 {
		p.Log.Section("Mise configuration registry")
		if err := p.MiseLive.Command("mise", "prune", "--configs", "--yes"); err != nil {
			p.Log.Error(err.Error())
			failures = append(failures, fmt.Errorf("mise configuration registry: %w", err))
		} else {
			p.Log.OK("dead configuration links pruned")
		}
	}
	if len(current.tools) > 0 {
		p.Log.Section("Mise tools")
		if err := p.MiseLive.Command("mise", "prune", "--tools", "--yes"); err != nil {
			p.Log.Error(err.Error())
			failures = append(failures, fmt.Errorf("mise tools: %w", err))
		} else {
			p.Log.OK(FormatCount(len(current.tools), "tool version pruned", "tool versions pruned"))
		}
	}
	if len(current.packages) > 0 {
		p.Log.Section("Packages")
		for _, manager := range current.packages {
			if err := p.MiseLive.Command("mise", "bootstrap", "packages", "prune", "--manager", manager.Name, "--yes"); err != nil {
				p.Log.Error(manager.Name + ": " + err.Error())
				failures = append(failures, fmt.Errorf("%s packages: %w", manager.Name, err))
			} else {
				p.Log.OK(manager.Name + " packages pruned")
			}
		}
	}
	if len(current.caches) > 0 {
		p.Log.Section("Mise download cache")
		for _, cache := range current.caches {
			reclaimed, err := applyPruneCache(cache)
			if err != nil {
				p.Log.Error(cache.Label + ": " + err.Error())
				failures = append(failures, fmt.Errorf("%s: %w", cache.Label, err))
			}
			if err == nil || reclaimed > 0 {
				p.Log.OK(cache.Label + " reclaimed " + FormatBytes(reclaimed))
			}
		}
	}
	if len(current.agentSkills.Skills) > 0 {
		p.Log.Section(agentSkillsName)
		if err := p.applyPruneAgentSkills(current.agentSkills); err != nil {
			p.Log.Error(err.Error())
			failures = append(failures, fmt.Errorf("%s: %w", agentSkillsName, err))
		} else {
			p.Log.OK(FormatCount(len(current.agentSkills.Skills), "skill placement pruned", "skill placements pruned"))
		}
	}
	if len(current.hooks) > 0 || len(current.files) > 0 {
		p.Log.Section("Config state")
		for _, target := range current.hooks {
			if err := applyPruneHooks(target); err != nil {
				p.Log.Error(target.Name + ": " + err.Error())
				failures = append(failures, fmt.Errorf("%s hooks: %w", target.Name, err))
			} else {
				p.Log.OK(target.Name + " stale hook state pruned")
			}
		}
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

func (p Pruner) applyPruneAgentSkills(plan pruneAgentSkills) error {
	manifest, data, err := readAgentSkillManifest(p.Paths)
	if err != nil {
		return err
	}
	if contentDigest(data) != plan.ManifestDigest {
		return errors.New("agent skill ownership changed after preview")
	}
	agentSet := map[string]bool{}
	for _, skill := range plan.Skills {
		for _, agent := range skill.RemoveAgents {
			agentSet[agent] = true
		}
	}
	agents := make([]string, 0, len(agentSet))
	for agent := range agentSet {
		agents = append(agents, agent)
	}
	slices.Sort(agents)
	var inventory agentSkillInventory
	if len(agents) > 0 {
		inventory, err = loadAgentSkillInventory(p.Paths, p.Skills, agents, true)
		if err != nil {
			return fmt.Errorf("revalidate agent skills: %w", err)
		}
	}
	var failures []error
	for _, skill := range plan.Skills {
		ownership, exists := manifest.Skills[skill.Name]
		if !exists {
			failures = append(failures, fmt.Errorf("%s ownership disappeared after preview", skill.Name))
			continue
		}
		if len(skill.RemoveAgents) > 0 {
			live, found := inventory.Global[skill.Name]
			digest, digestErr := agentSkillDirectoryDigest(ownership.Path)
			if !found || !sameAgentSkillSource(live.locator(), ownership.Source) ||
				live.Path != ownership.Path || digestErr != nil || digest != ownership.Digest {
				failures = append(failures, fmt.Errorf("%s changed after preview; left untouched", skill.Name))
				continue
			}
			changed := false
			for _, agent := range skill.RemoveAgents {
				installed, present := inventory.Agents[agent][skill.Name]
				if !present || !sameAgentSkillSource(installed.locator(), ownership.Source) {
					changed = true
				}
			}
			if changed {
				failures = append(failures, fmt.Errorf("%s agent placements changed after preview; left untouched", skill.Name))
				continue
			}
			arguments := []string{"remove", skill.Name, "-g", "-a"}
			arguments = append(arguments, skill.RemoveAgents...)
			arguments = append(arguments, "-y")
			if err := validateAgentSkillsLock(p.Paths); err != nil {
				failures = append(failures, fmt.Errorf("%s: %w", skill.Name, err))
				continue
			}
			if err := p.SkillsLive.Command("npx", agentSkillsArguments(true, arguments...)...); err != nil {
				failures = append(failures, fmt.Errorf("%s: %w", skill.Name, err))
				continue
			}
		}
		ownership.Agents = slices.DeleteFunc(ownership.Agents, func(agent string) bool {
			return slices.Contains(skill.ForgetAgents, agent)
		})
		if len(ownership.Agents) == 0 {
			delete(manifest.Skills, skill.Name)
		} else {
			manifest.Skills[skill.Name] = ownership
		}
	}
	if err := writeAgentSkillManifest(p.Paths, manifest); err != nil {
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}

func applyPruneHooks(target pruneHookTarget) error {
	manifestPath := filepath.Join(target.Dir, repositoryHookManifestName)
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	if contentDigest(data) != target.ManifestDigest {
		return errors.New("ownership manifest changed after preview")
	}
	manifest, err := readRepositoryHookManifest(target.Dir)
	if err != nil {
		return err
	}
	for _, hook := range target.Hooks {
		if manifest.Hooks[hook.Name] != hook.Digest {
			return fmt.Errorf("%s ownership changed after preview", hook.Name)
		}
		if hook.RemoveFile {
			path := filepath.Join(target.Dir, hook.Name)
			info, err := os.Lstat(path)
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("%s is no longer a regular file", hook.Name)
			}
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if contentDigest(body) != hook.Digest {
				return fmt.Errorf("%s changed after preview", hook.Name)
			}
			if err := os.Remove(path); err != nil {
				return err
			}
		}
		delete(manifest.Hooks, hook.Name)
	}
	if len(manifest.Hooks) == 0 {
		return os.Remove(manifestPath)
	}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWrite(manifestPath, append(encoded, '\n'), 0o644)
}

// applyPruneCache trusts the plan Apply just recomputed rather than checking
// each file again: a cask is cached under a name carrying its content hash,
// so there is nothing a second look could catch.
func applyPruneCache(cache pruneCache) (int64, error) {
	var reclaimed int64
	var failures []error
	for _, file := range cache.Files {
		err := os.Remove(filepath.Join(cache.Dir, file.Name))
		switch {
		case errors.Is(err, os.ErrNotExist):
		case err != nil:
			failures = append(failures, err)
		default:
			reclaimed += file.Size
		}
	}
	return reclaimed, errors.Join(failures...)
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
