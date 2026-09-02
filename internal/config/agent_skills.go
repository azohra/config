package config

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const (
	agentSkillsID             = "agent-skills"
	agentSkillsName           = "Agent skills"
	testedAgentSkillsVersion  = "1.5.23"
	agentSkillsLockSchema     = 3
	agentSkillsManifestSchema = 1
)

// AgentSkills declares the global skills and agent links owned by this
// machine. Config owns the tested npx adapter and the reconciliation; the
// machine repository owns only these source and selection values.
type AgentSkills struct {
	// Agents are requested CLI targets. Universal agents share the canonical
	// global directory; the adapter creates links for agent-specific layouts.
	Agents  []string           `toml:"agents"`
	Sources []AgentSkillSource `toml:"sources"`
}

type AgentSkillSource struct {
	Source string   `toml:"source"`
	Skills []string `toml:"skills"`
}

func (s AgentSkills) Validate() error {
	if len(s.Agents) == 0 {
		return errors.New("agents must not be empty")
	}
	if len(s.Sources) == 0 {
		return errors.New("sources must not be empty")
	}
	seenAgents := map[string]bool{}
	for _, agent := range s.Agents {
		if !contractIDPattern.MatchString(agent) {
			return fmt.Errorf("agent %q is invalid", agent)
		}
		if seenAgents[agent] {
			return fmt.Errorf("agents repeats %q", agent)
		}
		seenAgents[agent] = true
	}
	seenSources := map[string]bool{}
	seenSkills := map[string]bool{}
	for _, source := range s.Sources {
		identity, err := repositoryIdentity(source.Source)
		if err != nil {
			return fmt.Errorf("source %q: %w", source.Source, err)
		}
		if seenSources[identity] {
			return fmt.Errorf("sources repeats %q", source.Source)
		}
		seenSources[identity] = true
		if len(source.Skills) == 0 {
			return fmt.Errorf("source %q skills must not be empty", source.Source)
		}
		for _, name := range source.Skills {
			if !contractIDPattern.MatchString(name) {
				return fmt.Errorf("skill %q is invalid", name)
			}
			if seenSkills[name] {
				return fmt.Errorf("skill %q is declared more than once", name)
			}
			seenSkills[name] = true
		}
	}
	return nil
}

func (s AgentSkills) desired() map[string]agentSkillDeclaration {
	desired := make(map[string]agentSkillDeclaration)
	agents := append([]string(nil), s.Agents...)
	slices.Sort(agents)
	for _, source := range s.Sources {
		for _, name := range source.Skills {
			desired[name] = agentSkillDeclaration{Name: name, Source: source.Source, Agents: agents}
		}
	}
	return desired
}

type agentSkillDeclaration struct {
	Name   string
	Source string
	Agents []string
}

func sortedAgentSkillNames(skills map[string]agentSkillDeclaration) []string {
	names := make([]string, 0, len(skills))
	for name := range skills {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

type agentSkillListing struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Scope     string `json:"scope"`
	Source    string `json:"source"`
	SourceURL string `json:"sourceUrl"`
}

func (s agentSkillListing) locator() string {
	if s.SourceURL != "" {
		return s.SourceURL
	}
	return s.Source
}

type agentSkillInventory struct {
	Global map[string]agentSkillListing
	Agents map[string]map[string]agentSkillListing
}

type agentSkillOwnership struct {
	Source string   `json:"source"`
	Agents []string `json:"agents"`
	Path   string   `json:"path"`
	Digest string   `json:"digest"`
}

type agentSkillManifest struct {
	Schema int                            `json:"schema"`
	Skills map[string]agentSkillOwnership `json:"skills"`
}

func emptyAgentSkillManifest() agentSkillManifest {
	return agentSkillManifest{Schema: agentSkillsManifestSchema, Skills: map[string]agentSkillOwnership{}}
}

func agentSkillManifestPath(paths Paths) string {
	return paths.InHome("Library", "Application Support", "Config", "agent-skills.json")
}

func agentSkillsLockPath(paths Paths) string {
	if state := os.Getenv("XDG_STATE_HOME"); state != "" {
		if !filepath.IsAbs(state) {
			state = filepath.Join(paths.Root, state)
		}
		return filepath.Join(state, "skills", ".skill-lock.json")
	}
	return paths.InHome(".agents", ".skill-lock.json")
}

func validateAgentSkillsLock(paths Paths) error {
	data, err := os.ReadFile(agentSkillsLockPath(paths))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var lock struct {
		Version int                        `json:"version"`
		Skills  map[string]json.RawMessage `json:"skills"`
	}
	if err := json.Unmarshal(data, &lock); err != nil || lock.Skills == nil {
		return errors.New("global skills lock is unreadable")
	}
	if lock.Version != agentSkillsLockSchema {
		return fmt.Errorf("global skills lock schema is %d, want %d", lock.Version, agentSkillsLockSchema)
	}
	return nil
}

func agentSkillsCachePath(paths Paths) string {
	return filepath.Join(filepath.Dir(paths.StateDir), "agent-skills", "npm")
}

func agentSkillsEnvironment(paths Paths) []string {
	// Scope only package resolution. HOME and XDG_STATE_HOME stay inherited so
	// Config and ordinary npx skills invocations operate on one skill store.
	return []string{
		"DO_NOT_TRACK=1",
		"NPM_CONFIG_CACHE=" + agentSkillsCachePath(paths),
		"NPM_CONFIG_LOGS_MAX=0",
		"NPM_CONFIG_UPDATE_NOTIFIER=false",
	}
}

var agentSkillsLocalEnvironment = []string{
	"npm_config_cache", "npm_config_logs_max", "npm_config_update_notifier",
}

// newAgentSkillsRunner keeps the exact npx adapter inside Config's cache.
// The managed machine document selects skills, not a global npm package.
func newAgentSkillsRunner(paths Paths) OSRunner {
	runner := NewMachineRunner(paths)
	runner.Environment = agentSkillsEnvironment(paths)
	runner.Unset = append(runner.Unset, agentSkillsLocalEnvironment...)
	return runner
}

func newAgentSkillsLiveRunner(paths Paths) LiveRunner {
	runner := newMachineLiveRunner(paths)
	runner.Environment = agentSkillsEnvironment(paths)
	runner.Unset = append(runner.Unset, agentSkillsLocalEnvironment...)
	return runner
}

func readAgentSkillManifest(paths Paths) (agentSkillManifest, []byte, error) {
	path := agentSkillManifestPath(paths)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return emptyAgentSkillManifest(), nil, nil
	}
	if err != nil {
		return agentSkillManifest{}, nil, err
	}
	var manifest agentSkillManifest
	if err := decodeExactJSON(data, &manifest); err != nil {
		return agentSkillManifest{}, data, err
	}
	if manifest.Schema != agentSkillsManifestSchema || manifest.Skills == nil {
		return agentSkillManifest{}, data, errors.New("unsupported manifest")
	}
	for name, skill := range manifest.Skills {
		if !contractIDPattern.MatchString(name) {
			return agentSkillManifest{}, data, fmt.Errorf("invalid skill %q", name)
		}
		if _, err := repositoryIdentity(skill.Source); err != nil {
			return agentSkillManifest{}, data, fmt.Errorf("invalid source for %q", name)
		}
		if !filepath.IsAbs(skill.Path) || !pathInside(paths.Home, skill.Path) || !validContentDigest(skill.Digest) {
			return agentSkillManifest{}, data, fmt.Errorf("invalid ownership for %q", name)
		}
		seen := map[string]bool{}
		for _, agent := range skill.Agents {
			if !contractIDPattern.MatchString(agent) || seen[agent] {
				return agentSkillManifest{}, data, fmt.Errorf("invalid agents for %q", name)
			}
			seen[agent] = true
		}
	}
	return manifest, data, nil
}

func writeAgentSkillManifest(paths Paths, manifest agentSkillManifest) error {
	for name, skill := range manifest.Skills {
		slices.Sort(skill.Agents)
		skill.Agents = slices.Compact(skill.Agents)
		manifest.Skills[name] = skill
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(agentSkillManifestPath(paths), append(data, '\n'), 0o600)
}

func agentSkillsArguments(offline bool, arguments ...string) []string {
	args := make([]string, 0, len(arguments)+5)
	if offline {
		args = append(args, "--offline")
	}
	args = append(args, "--yes", "--package=skills@"+testedAgentSkillsVersion, "skills")
	return append(args, arguments...)
}

func runAgentSkills(runner Runner, offline bool, arguments ...string) Result {
	timeout := 20 * time.Second
	if !offline {
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return runner.Run(ctx, "npx", agentSkillsArguments(offline, arguments...)...)
}

func requireAgentSkillsAdapter(runner Runner, offline bool) error {
	if !runner.Exists("npx") {
		return errors.New("npx is unavailable")
	}
	result := runAgentSkills(runner, offline, "--version")
	if result.Err != nil {
		return fmt.Errorf("run npx skills %s: %w", testedAgentSkillsVersion, result.Failure())
	}
	if result.Output() != testedAgentSkillsVersion {
		return fmt.Errorf("npx skills is %q, want %s", result.Output(), testedAgentSkillsVersion)
	}
	return nil
}

func parseAgentSkillListing(output string) (map[string]agentSkillListing, error) {
	var listing []agentSkillListing
	if err := json.Unmarshal([]byte(output), &listing); err != nil {
		return nil, err
	}
	byName := make(map[string]agentSkillListing, len(listing))
	for _, skill := range listing {
		if skill.Name == "" || skill.Scope != "global" || !filepath.IsAbs(skill.Path) {
			return nil, errors.New("skills listing contains an invalid global entry")
		}
		if _, exists := byName[skill.Name]; exists {
			return nil, fmt.Errorf("skills listing repeats %q", skill.Name)
		}
		byName[skill.Name] = skill
	}
	return byName, nil
}

func loadAgentSkillInventory(paths Paths, runner Runner, agents []string, offline bool) (agentSkillInventory, error) {
	if err := validateAgentSkillsLock(paths); err != nil {
		return agentSkillInventory{}, err
	}
	if err := requireAgentSkillsAdapter(runner, offline); err != nil {
		return agentSkillInventory{}, err
	}
	globalResult := runAgentSkills(runner, offline, "list", "-g", "--json")
	if globalResult.Err != nil {
		return agentSkillInventory{}, fmt.Errorf("list global skills: %w", globalResult.Failure())
	}
	global, err := parseAgentSkillListing(globalResult.Stdout)
	if err != nil {
		return agentSkillInventory{}, fmt.Errorf("read global skills: %w", err)
	}
	inventory := agentSkillInventory{Global: global, Agents: make(map[string]map[string]agentSkillListing, len(agents))}
	for _, agent := range agents {
		result := runAgentSkills(runner, offline, "list", "-g", "-a", agent, "--json")
		if result.Err != nil {
			return agentSkillInventory{}, fmt.Errorf("list %s skills: %w", agent, result.Failure())
		}
		listing, err := parseAgentSkillListing(result.Stdout)
		if err != nil {
			return agentSkillInventory{}, fmt.Errorf("read %s skills: %w", agent, err)
		}
		inventory.Agents[agent] = listing
	}
	return inventory, nil
}

func sameAgentSkillSource(left, right string) bool {
	return sameRepositoryLocator(left, right)
}

func agentSkillDirectoryDigest(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("skill path is not a directory")
	}
	hash := sha256.New()
	err = filepath.WalkDir(path, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(path, current)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		kind := byte('f')
		switch {
		case info.IsDir():
			kind = 'd'
		case info.Mode()&os.ModeSymlink != 0:
			kind = 'l'
		case !info.Mode().IsRegular():
			return fmt.Errorf("%s is not a regular skill file", relative)
		}
		fmt.Fprintf(hash, "%c\x00%s\x00%04o\x00", kind, filepath.ToSlash(relative), info.Mode().Perm())
		switch kind {
		case 'f':
			file, err := os.Open(current)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(hash, file)
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		case 'l':
			target, err := os.Readlink(current)
			if err != nil {
				return err
			}
			_, _ = io.WriteString(hash, target)
		}
		_, _ = hash.Write([]byte{0})
		return nil
	})
	if err != nil {
		return "", err
	}
	return digestPrefix + hex.EncodeToString(hash.Sum(nil)), nil
}

func inspectAgentSkills(paths Paths, skills AgentSkills, runner Runner) Resource {
	resource := Resource{ID: agentSkillsID, Name: agentSkillsName, Authoritative: true}
	manifest, _, err := readAgentSkillManifest(paths)
	if err != nil {
		resource.State = Unavailable
		resource.Summary = "Agent skill ownership is unreadable"
		resource.Checks = []Check{no("Agent skill ownership readable", err.Error())}
		return resource
	}
	if err := validateAgentSkillsLock(paths); err != nil {
		resource.State = Unavailable
		resource.Summary = "Shared agent skill state is incompatible"
		resource.Checks = []Check{no("Global skills lock compatible", err.Error())}
		return resource
	}
	inventory, err := loadAgentSkillInventory(paths, runner, skills.Agents, true)
	if err != nil {
		resource.State = Unavailable
		resource.Summary = "Agent skills are unavailable"
		resource.Checks = []Check{no("Pinned npx skills adapter available", err.Error())}
		resource.Actions = []Action{Apply}
		resource.ActionLabels = map[Action]string{Apply: "Reconcile agent skills"}
		return resource
	}
	var drifted, conflicts []string
	for name, declaration := range skills.desired() {
		live, found := inventory.Global[name]
		if !found {
			drifted = append(drifted, name+" is missing")
			continue
		}
		if !sameAgentSkillSource(live.locator(), declaration.Source) {
			conflicts = append(conflicts, name+" comes from another source")
			continue
		}
		if !pathInside(paths.Home, live.Path) {
			conflicts = append(conflicts, name+" is outside the home directory")
			continue
		}
		missingPlacement := false
		for _, agent := range declaration.Agents {
			installed, found := inventory.Agents[agent][name]
			if !found || !sameAgentSkillSource(installed.locator(), declaration.Source) {
				drifted = append(drifted, name+" is missing for "+agent)
				missingPlacement = true
			}
		}
		ownership, owned := manifest.Skills[name]
		if !owned {
			drifted = append(drifted, name+" ownership needs adoption")
			continue
		}
		digest, digestErr := agentSkillDirectoryDigest(live.Path)
		if digestErr != nil {
			conflicts = append(conflicts, name+" is unreadable")
			continue
		}
		if ownership.Path != live.Path || digest != ownership.Digest {
			if missingPlacement {
				conflicts = append(conflicts, name+" content and agent placements both changed")
			} else {
				drifted = append(drifted, name+" changed outside Config and needs adoption")
			}
			continue
		}
		if !sameAgentSkillSource(ownership.Source, declaration.Source) {
			drifted = append(drifted, name+" source needs reconciliation")
		}
	}
	slices.Sort(drifted)
	slices.Sort(conflicts)
	resource.Details = append(resource.Details, drifted...)
	resource.Details = append(resource.Details, conflicts...)
	resource.Checks = []Check{yes("Pinned npx skills " + testedAgentSkillsVersion)}
	switch {
	case len(conflicts) > 0:
		resource.State = Drift
		resource.Summary = FormatCount(len(conflicts), "skill conflicts with Config ownership", "skills conflict with Config ownership")
		resource.Checks = append(resource.Checks, no("Agent skills safe to manage", strings.Join(conflicts, ", ")))
	case len(drifted) > 0:
		resource.State = Drift
		resource.Summary = FormatCount(len(drifted), "agent-skill change needs reconciliation", "agent-skill changes need reconciliation")
		resource.Checks = append(resource.Checks, no("Declared agent skills current", strings.Join(drifted, ", ")))
		resource.Actions = []Action{Apply}
		resource.ActionLabels = map[Action]string{Apply: "Reconcile agent skills"}
	default:
		resource.State = Current
		resource.Summary = FormatCount(len(skills.desired()), "agent skill current", "agent skills current")
		resource.Checks = append(resource.Checks, yes("Declared agent skills current"))
	}
	return resource
}

type agentSkillManager struct {
	Paths  Paths
	Skills AgentSkills
	Probe  Runner
	Live   commandRunner
	Log    Logger
}

func (m agentSkillManager) command(arguments ...string) error {
	if err := validateAgentSkillsLock(m.Paths); err != nil {
		return err
	}
	return m.Live.Command("npx", agentSkillsArguments(false, arguments...)...)
}

func (m agentSkillManager) Reconcile() error {
	return m.reconcile(true)
}

func (m agentSkillManager) reconcile(adoptChanges bool) error {
	manifest, _, err := readAgentSkillManifest(m.Paths)
	if err != nil {
		return fmt.Errorf("read agent skill ownership: %w", err)
	}
	if err := validateAgentSkillsLock(m.Paths); err != nil {
		return err
	}
	if err := requireAgentSkillsAdapter(m.Probe, false); err != nil {
		return err
	}
	inventory, err := loadAgentSkillInventory(m.Paths, m.Probe, m.Skills.Agents, true)
	if err != nil {
		return err
	}
	desired := m.Skills.desired()
	installBySource := map[string][]string{}
	var failures []error
	for _, name := range sortedAgentSkillNames(desired) {
		declaration := desired[name]
		live, found := inventory.Global[name]
		ownership, owned := manifest.Skills[name]
		if found && !sameAgentSkillSource(live.locator(), declaration.Source) {
			failures = append(failures, fmt.Errorf("%s comes from another source; left untouched", name))
			delete(desired, name)
			continue
		}
		if found && !pathInside(m.Paths.Home, live.Path) {
			failures = append(failures, fmt.Errorf("%s is outside the home directory; left untouched", name))
			delete(desired, name)
			continue
		}
		ownedStateChanged := false
		if found && owned {
			digest, digestErr := agentSkillDirectoryDigest(live.Path)
			if digestErr != nil {
				failures = append(failures, fmt.Errorf("%s is unreadable; left untouched", name))
				delete(desired, name)
				continue
			}
			ownedStateChanged = ownership.Path != live.Path || digest != ownership.Digest
			if ownedStateChanged && !adoptChanges {
				failures = append(failures, fmt.Errorf("%s changed since Config recorded it; left untouched", name))
				delete(desired, name)
				continue
			}
		}
		needsInstall := !found
		if found {
			for _, agent := range declaration.Agents {
				installed, present := inventory.Agents[agent][name]
				if !present || !sameAgentSkillSource(installed.locator(), declaration.Source) {
					needsInstall = true
				}
			}
		}
		if ownedStateChanged && needsInstall {
			failures = append(failures, fmt.Errorf("%s content and agent placements both changed; left untouched", name))
			delete(desired, name)
			continue
		}
		if needsInstall {
			installBySource[declaration.Source] = append(installBySource[declaration.Source], name)
		}
	}

	sources := make([]string, 0, len(installBySource))
	for source := range installBySource {
		sources = append(sources, source)
	}
	slices.Sort(sources)
	for _, source := range sources {
		names := installBySource[source]
		slices.Sort(names)
		arguments := []string{"add", source, "-g", "--skill"}
		arguments = append(arguments, names...)
		arguments = append(arguments, "--agent")
		arguments = append(arguments, m.Skills.Agents...)
		arguments = append(arguments, "-y")
		if err := m.command(arguments...); err != nil {
			failures = append(failures, fmt.Errorf("install %s: %w", strings.Join(names, ", "), err))
			for _, name := range names {
				delete(desired, name)
			}
		}
	}

	refreshed, refreshErr := loadAgentSkillInventory(m.Paths, m.Probe, m.Skills.Agents, true)
	if refreshErr != nil {
		failures = append(failures, refreshErr)
	} else {
		for _, name := range sortedAgentSkillNames(desired) {
			declaration := desired[name]
			live, found := refreshed.Global[name]
			if !found || !sameAgentSkillSource(live.locator(), declaration.Source) {
				failures = append(failures, fmt.Errorf("%s did not reconcile", name))
				continue
			}
			current := true
			for _, agent := range declaration.Agents {
				installed, present := refreshed.Agents[agent][name]
				current = current && present && sameAgentSkillSource(installed.locator(), declaration.Source)
			}
			if !current {
				failures = append(failures, fmt.Errorf("%s agent links did not reconcile", name))
				continue
			}
			digest, err := agentSkillDirectoryDigest(live.Path)
			if err != nil {
				failures = append(failures, fmt.Errorf("record %s: %w", name, err))
				continue
			}
			agents := append([]string(nil), declaration.Agents...)
			if previous, exists := manifest.Skills[name]; exists && sameAgentSkillSource(previous.Source, declaration.Source) {
				agents = append(agents, previous.Agents...)
			}
			slices.Sort(agents)
			manifest.Skills[name] = agentSkillOwnership{
				Source: declaration.Source, Agents: slices.Compact(agents), Path: live.Path, Digest: digest,
			}
		}
	}
	if err := writeAgentSkillManifest(m.Paths, manifest); err != nil {
		failures = append(failures, fmt.Errorf("record agent skill ownership: %w", err))
	}
	if len(failures) == 0 {
		m.Log.OK(FormatCount(len(desired), "agent skill current", "agent skills current"))
	}
	return errors.Join(failures...)
}

func (m agentSkillManager) Update() error {
	if err := m.reconcile(false); err != nil {
		return err
	}
	manifest, _, err := readAgentSkillManifest(m.Paths)
	if err != nil {
		return err
	}
	desired := m.Skills.desired()
	names := sortedAgentSkillNames(desired)
	for _, name := range names {
		ownership, owned := manifest.Skills[name]
		if !owned || !sameAgentSkillSource(ownership.Source, desired[name].Source) {
			return fmt.Errorf("%s has no current Config ownership", name)
		}
		digest, digestErr := agentSkillDirectoryDigest(ownership.Path)
		if digestErr != nil || digest != ownership.Digest {
			return fmt.Errorf("%s changed since Config recorded it; left untouched", name)
		}
	}
	arguments := append([]string{"update"}, names...)
	arguments = append(arguments, "-g", "-y")
	if err := m.command(arguments...); err != nil {
		return err
	}
	inventory, err := loadAgentSkillInventory(m.Paths, m.Probe, m.Skills.Agents, true)
	if err != nil {
		return err
	}
	for _, name := range names {
		live, found := inventory.Global[name]
		if !found || !sameAgentSkillSource(live.locator(), desired[name].Source) {
			return fmt.Errorf("%s did not remain current after update", name)
		}
		digest, err := agentSkillDirectoryDigest(live.Path)
		if err != nil {
			return err
		}
		ownership := manifest.Skills[name]
		ownership.Path = live.Path
		ownership.Digest = digest
		manifest.Skills[name] = ownership
	}
	if err := writeAgentSkillManifest(m.Paths, manifest); err != nil {
		return err
	}
	m.Log.OK("declared agent skills updated")
	return nil
}
