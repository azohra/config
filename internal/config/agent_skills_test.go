package config

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type fakeAgentSkill struct {
	Source string
	Path   string
	Agents []string
}

type fakeAgentSkillsRuntime struct {
	paths     Paths
	available bool
	skills    map[string]fakeAgentSkill
	commands  []string
	probes    []string
	updates   int
}

func newFakeAgentSkillsRuntime(paths Paths) *fakeAgentSkillsRuntime {
	return &fakeAgentSkillsRuntime{paths: paths, available: true, skills: map[string]fakeAgentSkill{}}
}

func testAgentSkillPaths(t *testing.T) Paths {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", "")
	return testPaths(t)
}

func (r *fakeAgentSkillsRuntime) Exists(name string) bool {
	return name == "npx" && r.available
}

func (r *fakeAgentSkillsRuntime) Run(_ context.Context, name string, arguments ...string) Result {
	if !r.Exists(name) {
		return Result{Err: errors.New("not found")}
	}
	command, err := fakeAgentSkillsCommand(arguments)
	if err != nil {
		return Result{Err: err}
	}
	r.probes = append(r.probes, strings.Join(command, " "))
	if len(command) == 1 && command[0] == "--version" {
		return Result{Stdout: testedAgentSkillsVersion + "\n"}
	}
	if len(command) < 3 || command[0] != "list" || command[1] != "-g" {
		return Result{Err: fmt.Errorf("unexpected probe: %v", command)}
	}
	var requested []string
	if len(command) >= 5 && command[2] == "--agent" && command[len(command)-1] == "--json" {
		requested = command[3 : len(command)-1]
	} else if len(command) != 3 || command[2] != "--json" {
		return Result{Err: fmt.Errorf("unexpected list: %v", command)}
	}
	var listing []agentSkillListing
	for name, skill := range r.skills {
		agents := slices.Clone(skill.Agents)
		if len(requested) > 0 {
			agents = slices.DeleteFunc(agents, func(agent string) bool { return !slices.Contains(requested, agent) })
			if len(agents) == 0 {
				continue
			}
		}
		for index, agent := range agents {
			switch agent {
			case "bob":
				agents[index] = "IBM Bob"
			case "claude-code":
				agents[index] = "Claude Code"
			case "codex":
				agents[index] = "Codex"
			}
		}
		listing = append(listing, agentSkillListing{
			Name: name, Path: skill.Path, Scope: "global", SourceURL: skill.Source, Agents: agents,
		})
	}
	slices.SortFunc(listing, func(left, right agentSkillListing) int {
		return strings.Compare(left.Name, right.Name)
	})
	data, err := json.Marshal(listing)
	return Result{Stdout: string(data), Err: err}
}

func (r *fakeAgentSkillsRuntime) Command(name string, arguments ...string) error {
	if !r.Exists(name) {
		return errors.New("not found")
	}
	command, err := fakeAgentSkillsCommand(arguments)
	if err != nil {
		return err
	}
	r.commands = append(r.commands, strings.Join(arguments, " "))
	switch command[0] {
	case "add":
		if len(command) < 8 || command[2] != "-g" || command[3] != "--skill" {
			return fmt.Errorf("unexpected add: %v", command)
		}
		agentIndex := slices.Index(command, "--agent")
		if agentIndex < 5 {
			return fmt.Errorf("unexpected add: %v", command)
		}
		source := command[1]
		names := command[4:agentIndex]
		agents := command[agentIndex+1 : len(command)-1]
		for _, skillName := range names {
			path := filepath.Join(r.paths.Home, ".agents", "skills", skillName)
			if err := os.RemoveAll(path); err != nil {
				return err
			}
			if err := os.MkdirAll(path, 0o755); err != nil {
				return err
			}
			body := fmt.Sprintf("---\nname: %s\ndescription: fixture\n---\n\n%s\n", skillName, source)
			if err := os.WriteFile(filepath.Join(path, "SKILL.md"), []byte(body), 0o644); err != nil {
				return err
			}
			r.skills[skillName] = fakeAgentSkill{Source: source, Path: path, Agents: append([]string(nil), agents...)}
		}
	case "remove":
		if len(command) < 4 {
			return fmt.Errorf("unexpected remove: %v", command)
		}
		skillName := command[1]
		skill, found := r.skills[skillName]
		if !found {
			return nil
		}
		agentIndex := slices.Index(command, "-a")
		if agentIndex < 0 {
			if err := os.RemoveAll(skill.Path); err != nil {
				return err
			}
			delete(r.skills, skillName)
			return nil
		}
		removeAgents := command[agentIndex+1 : len(command)-1]
		skill.Agents = slices.DeleteFunc(skill.Agents, func(agent string) bool {
			return slices.Contains(removeAgents, agent)
		})
		if len(skill.Agents) == 0 {
			if err := os.RemoveAll(skill.Path); err != nil {
				return err
			}
			delete(r.skills, skillName)
		} else {
			r.skills[skillName] = skill
		}
	case "update":
		for _, skillName := range command[1 : len(command)-2] {
			skill, found := r.skills[skillName]
			if !found {
				return fmt.Errorf("update missing %s", skillName)
			}
			r.updates++
			body := fmt.Sprintf("---\nname: %s\ndescription: fixture\n---\n\n%s\nupdate %d\n", skillName, skill.Source, r.updates)
			if err := os.WriteFile(filepath.Join(skill.Path, "SKILL.md"), []byte(body), 0o644); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unexpected mutation: %v", command)
	}
	return nil
}

func fakeAgentSkillsCommand(arguments []string) ([]string, error) {
	index := slices.Index(arguments, "skills")
	if index < 0 || index+1 == len(arguments) {
		return nil, fmt.Errorf("missing skills command: %v", arguments)
	}
	if !slices.Contains(arguments[:index], "--package=skills@"+testedAgentSkillsVersion) {
		return nil, fmt.Errorf("unpinned skills command: %v", arguments)
	}
	return arguments[index+1:], nil
}

func testAgentSkills() AgentSkills {
	return AgentSkills{
		Agents: []string{"claude-code", "codex"},
		Sources: []AgentSkillSource{{
			Source: "https://github.com/example/skills.git",
			Skills: []string{"orca-cli", "orchestration"},
		}},
	}
}

func TestAgentSkillsRunnerUsesConfigOwnedNpxCache(t *testing.T) {
	paths := testAgentSkillPaths(t)
	want := agentSkillsCachePath(paths)
	for _, environment := range [][]string{
		newAgentSkillsRunner(paths).Environment,
		newAgentSkillsLiveRunner(paths).Environment,
	} {
		for _, entry := range environment {
			if strings.HasPrefix(entry, "HOME=") || strings.HasPrefix(entry, "XDG_STATE_HOME=") {
				t.Fatalf("agent skills isolated the shared store with %q", entry)
			}
		}
		if got := environmentValue(environment, "DO_NOT_TRACK"); got != "1" {
			t.Fatalf("DO_NOT_TRACK = %q, want 1", got)
		}
		if got := environmentValue(environment, "NPM_CONFIG_CACHE"); got != want {
			t.Fatalf("NPM_CONFIG_CACHE = %q, want %q", got, want)
		}
		if got := environmentValue(environment, "NPM_CONFIG_LOGS_MAX"); got != "0" {
			t.Fatalf("NPM_CONFIG_LOGS_MAX = %q, want 0", got)
		}
		if got := environmentValue(environment, "NPM_CONFIG_UPDATE_NOTIFIER"); got != "false" {
			t.Fatalf("NPM_CONFIG_UPDATE_NOTIFIER = %q, want false", got)
		}
	}
}

func TestAgentSkillsRefusesAnIncompatibleSharedLock(t *testing.T) {
	paths := testAgentSkillPaths(t)
	path := agentSkillsLockPath(paths)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"version":4,"skills":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime := newFakeAgentSkillsRuntime(paths)
	resource := inspectAgentSkills(paths, testAgentSkills(), runtime)
	if resource.State != Unavailable || resource.Allows(Apply) || !strings.Contains(resource.Checks[0].Detail, "schema is 4") {
		t.Fatalf("incompatible lock resource = %+v", resource)
	}
	manager := agentSkillManager{Paths: paths, Skills: testAgentSkills(), Probe: runtime, Live: runtime, Log: Logger{Out: &bytes.Buffer{}}}
	if err := manager.Reconcile(); err == nil || !strings.Contains(err.Error(), "schema is 4") {
		t.Fatalf("incompatible lock reconcile = %v", err)
	}
	if len(runtime.commands) != 0 {
		t.Fatalf("incompatible lock mutated shared state: %v", runtime.commands)
	}
}

func (r *fakeAgentSkillsRuntime) install(t *testing.T, declaration agentSkillDeclaration) {
	t.Helper()
	arguments := []string{"add", declaration.Source, "-g", "--skill", declaration.Name, "--agent"}
	arguments = append(arguments, declaration.Agents...)
	arguments = append(arguments, "-y")
	if err := r.Command("npx", agentSkillsArguments(false, arguments...)...); err != nil {
		t.Fatal(err)
	}
	r.commands = nil
}

func TestAgentSkillsInspectionAdoptsMatchingLiveStateAndExternalChanges(t *testing.T) {
	paths := testAgentSkillPaths(t)
	contract := testAgentSkills()
	runtime := newFakeAgentSkillsRuntime(paths)
	for _, declaration := range contract.desired() {
		runtime.install(t, declaration)
	}

	before := inspectAgentSkills(paths, contract, runtime)
	if before.State != Drift || !before.Allows(Apply) || !strings.Contains(strings.Join(before.Details, "\n"), "ownership needs adoption") {
		t.Fatalf("pre-adoption resource = %+v", before)
	}
	var output bytes.Buffer
	manager := agentSkillManager{Paths: paths, Skills: contract, Probe: runtime, Live: runtime, Log: Logger{Out: &output}}
	if err := manager.Reconcile(); err != nil {
		t.Fatal(err)
	}
	if len(runtime.commands) != 0 {
		t.Fatalf("adoption reinstalled current skills: %v", runtime.commands)
	}
	if current := inspectAgentSkills(paths, contract, runtime); current.State != Current {
		t.Fatalf("adopted resource = %+v", current)
	}

	path := runtime.skills["orca-cli"].Path
	if err := os.WriteFile(filepath.Join(path, "SKILL.md"), []byte("local edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	changed := inspectAgentSkills(paths, contract, runtime)
	if changed.State != Drift || !changed.Allows(Apply) || !strings.Contains(strings.Join(changed.Details, "\n"), "needs adoption") {
		t.Fatalf("locally edited resource = %+v", changed)
	}
	runtime.commands = nil
	if err := manager.Reconcile(); err != nil {
		t.Fatal(err)
	}
	if len(runtime.commands) != 0 {
		t.Fatalf("adoption rewrote a changed skill: %v", runtime.commands)
	}
	if current := inspectAgentSkills(paths, contract, runtime); current.State != Current {
		t.Fatalf("readopted resource = %+v", current)
	}
}

func TestAgentSkillsRefusesCombinedContentAndPlacementDrift(t *testing.T) {
	paths := testAgentSkillPaths(t)
	contract := testAgentSkills()
	contract.Sources[0].Skills = []string{"orca-cli"}
	runtime := newFakeAgentSkillsRuntime(paths)
	manager := agentSkillManager{Paths: paths, Skills: contract, Probe: runtime, Live: runtime, Log: Logger{Out: &bytes.Buffer{}}}
	if err := manager.Reconcile(); err != nil {
		t.Fatal(err)
	}
	runtime.commands = nil
	skill := runtime.skills["orca-cli"]
	skill.Agents = []string{"codex"}
	runtime.skills["orca-cli"] = skill
	if err := os.WriteFile(filepath.Join(skill.Path, "SKILL.md"), []byte("external change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	resource := inspectAgentSkills(paths, contract, runtime)
	if resource.State != Drift || resource.Allows(Apply) || !strings.Contains(strings.Join(resource.Details, "\n"), "content and agent placements") {
		t.Fatalf("combined drift resource = %+v", resource)
	}
	if err := manager.Reconcile(); err == nil || !strings.Contains(err.Error(), "left untouched") {
		t.Fatalf("combined drift reconcile = %v", err)
	}
	if len(runtime.commands) != 0 {
		t.Fatalf("combined drift mutated live state: %v", runtime.commands)
	}
}

func TestAgentSkillsReconcileUsesThePinnedNpxAdapter(t *testing.T) {
	paths := testAgentSkillPaths(t)
	contract := testAgentSkills()
	runtime := newFakeAgentSkillsRuntime(paths)
	manager := agentSkillManager{Paths: paths, Skills: contract, Probe: runtime, Live: runtime, Log: Logger{Out: &bytes.Buffer{}}}
	if err := manager.Reconcile(); err != nil {
		t.Fatal(err)
	}
	if len(runtime.commands) != 1 || !strings.Contains(runtime.commands[0], "--package=skills@"+testedAgentSkillsVersion+" skills add") {
		t.Fatalf("commands = %v", runtime.commands)
	}
	if resource := inspectAgentSkills(paths, contract, runtime); resource.State != Current {
		t.Fatalf("reconciled resource = %+v", resource)
	}
	manifest, _, err := readAgentSkillManifest(paths)
	if err != nil || len(manifest.Skills) != 2 {
		t.Fatalf("ownership = %+v, %v", manifest, err)
	}
}

func TestAgentSkillsReconcilePreservesASourceChange(t *testing.T) {
	paths := testAgentSkillPaths(t)
	contract := testAgentSkills()
	contract.Sources[0].Skills = []string{"orca-cli"}
	runtime := newFakeAgentSkillsRuntime(paths)
	manager := agentSkillManager{Paths: paths, Skills: contract, Probe: runtime, Live: runtime, Log: Logger{Out: &bytes.Buffer{}}}
	if err := manager.Reconcile(); err != nil {
		t.Fatal(err)
	}
	runtime.commands = nil
	contract.Sources[0].Source = "https://github.com/example/replacement.git"
	manager.Skills = contract
	if err := manager.Reconcile(); err == nil || !strings.Contains(err.Error(), "another source") {
		t.Fatalf("source replacement = %v", err)
	}
	if len(runtime.commands) != 0 {
		t.Fatalf("source replacement mutated live state: %v", runtime.commands)
	}

	foreign := testAgentSkills()
	foreign.Sources[0].Source = "https://github.com/example/foreign.git"
	foreign.Sources[0].Skills = []string{"orca-cli"}
	runtime.skills["orca-cli"] = fakeAgentSkill{
		Source: "https://github.com/example/another.git", Path: runtime.skills["orca-cli"].Path,
		Agents: []string{"bob"},
	}
	manager.Skills = foreign
	runtime.commands = nil
	if err := manager.Reconcile(); err == nil || !strings.Contains(err.Error(), "another source") {
		t.Fatalf("foreign source reconcile = %v", err)
	}
	if len(runtime.commands) != 0 {
		t.Fatalf("foreign source was mutated: %v", runtime.commands)
	}
}

func TestAgentSkillsUpdateRefreshesOnlyDeclaredOwnership(t *testing.T) {
	paths := testAgentSkillPaths(t)
	contract := testAgentSkills()
	contract.Sources[0].Skills = []string{"orca-cli"}
	runtime := newFakeAgentSkillsRuntime(paths)
	manager := agentSkillManager{Paths: paths, Skills: contract, Probe: runtime, Live: runtime, Log: Logger{Out: &bytes.Buffer{}}}
	if err := manager.Update(); err != nil {
		t.Fatal(err)
	}
	if issued := strings.Join(runtime.commands, "\n"); !strings.Contains(issued, "skills update orca-cli -g -y") {
		t.Fatalf("update commands = %v", runtime.commands)
	}
	manifest, _, err := readAgentSkillManifest(paths)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := agentSkillDirectoryDigest(runtime.skills["orca-cli"].Path)
	if err != nil || manifest.Skills["orca-cli"].Digest != digest {
		t.Fatalf("updated ownership digest = %q, want %q (%v)", manifest.Skills["orca-cli"].Digest, digest, err)
	}
}

func TestAgentSkillsUpdateUsesOneCombinedInventoryPerSide(t *testing.T) {
	paths := testAgentSkillPaths(t)
	contract := testAgentSkills()
	runtime := newFakeAgentSkillsRuntime(paths)
	manager := agentSkillManager{Paths: paths, Skills: contract, Probe: runtime, Live: runtime, Log: Logger{Out: &bytes.Buffer{}}}
	if err := manager.Reconcile(); err != nil {
		t.Fatal(err)
	}
	runtime.probes = nil
	runtime.commands = nil
	if err := manager.Update(); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"--version",
		"list -g --json",
		"list -g --json",
	}
	if !slices.Equal(runtime.probes, want) {
		t.Fatalf("probes = %v, want %v", runtime.probes, want)
	}
	if len(runtime.commands) != 1 || !strings.Contains(runtime.commands[0], "skills update") {
		t.Fatalf("mutations = %v, want one update", runtime.commands)
	}
}

func TestAgentSkillInventoryFallsBackForExceptionalDisplayNames(t *testing.T) {
	paths := testAgentSkillPaths(t)
	runtime := newFakeAgentSkillsRuntime(paths)
	declaration := agentSkillDeclaration{Name: "orca-cli", Source: "https://github.com/stablyai/orca.git", Agents: []string{"bob"}}
	runtime.install(t, declaration)

	inventory, err := listAgentSkillInventory(runtime, declaration.Agents, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, found := inventory.Agents["bob"][declaration.Name]; !found {
		t.Fatal("exceptional display name did not resolve through the scoped fallback")
	}
	want := []string{"list -g --json", "list -g --agent bob --json"}
	if !slices.Equal(runtime.probes, want) {
		t.Fatalf("probes = %v, want %v", runtime.probes, want)
	}
}

func TestCurrentAgentSkillsDoNotRewriteTheirOwnershipManifest(t *testing.T) {
	paths := testAgentSkillPaths(t)
	contract := testAgentSkills()
	runtime := newFakeAgentSkillsRuntime(paths)
	manager := agentSkillManager{Paths: paths, Skills: contract, Probe: runtime, Live: runtime, Log: Logger{Out: &bytes.Buffer{}}}
	if err := manager.Reconcile(); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(agentSkillManifestPath(paths))
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Reconcile(); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(agentSkillManifestPath(paths))
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("current ownership manifest was replaced")
	}
}

func TestAgentSkillsUpdateRefusesAnUnadoptedExternalChange(t *testing.T) {
	paths := testAgentSkillPaths(t)
	contract := testAgentSkills()
	contract.Sources[0].Skills = []string{"orca-cli"}
	runtime := newFakeAgentSkillsRuntime(paths)
	manager := agentSkillManager{Paths: paths, Skills: contract, Probe: runtime, Live: runtime, Log: Logger{Out: &bytes.Buffer{}}}
	if err := manager.Reconcile(); err != nil {
		t.Fatal(err)
	}
	runtime.commands = nil
	path := runtime.skills["orca-cli"].Path
	if err := os.WriteFile(filepath.Join(path, "SKILL.md"), []byte("external npx update\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := manager.Update(); err == nil || !strings.Contains(err.Error(), "left untouched") {
		t.Fatalf("update changed skill = %v", err)
	}
	if len(runtime.commands) != 0 {
		t.Fatalf("update rewrote an unadopted skill: %v", runtime.commands)
	}
}

func TestAgentSkillsPruneRemovesOnlyRecordedAgentPlacements(t *testing.T) {
	paths := testAgentSkillPaths(t)
	contract := testAgentSkills()
	contract.Sources[0].Skills = []string{"orca-cli"}
	runtime := newFakeAgentSkillsRuntime(paths)
	manager := agentSkillManager{Paths: paths, Skills: contract, Probe: runtime, Live: runtime, Log: Logger{Out: &bytes.Buffer{}}}
	if err := manager.Reconcile(); err != nil {
		t.Fatal(err)
	}

	reduced := contract
	reduced.Agents = []string{"codex"}
	pruner := Pruner{
		Paths: paths, Machine: Machine{AgentSkills: &reduced}, Runner: converged{},
		Skills: runtime, SkillsLive: runtime, Log: Logger{Out: &bytes.Buffer{}},
	}
	plan, warnings := pruner.planAgentSkills(nil)
	if len(warnings) != 0 || len(plan.Skills) != 1 || !slices.Equal(plan.Skills[0].ForgetAgents, []string{"claude-code"}) {
		t.Fatalf("prune plan = %+v, warnings = %v", plan, warnings)
	}
	if err := pruner.applyPruneAgentSkills(plan); err != nil {
		t.Fatal(err)
	}
	if agents := runtime.skills["orca-cli"].Agents; !slices.Equal(agents, []string{"codex"}) {
		t.Fatalf("remaining agents = %v", agents)
	}
	manifest, _, err := readAgentSkillManifest(paths)
	if err != nil || !slices.Equal(manifest.Skills["orca-cli"].Agents, []string{"codex"}) {
		t.Fatalf("remaining ownership = %+v, %v", manifest, err)
	}

	pruner.Machine.AgentSkills = nil
	stale, warnings := pruner.planAgentSkills(nil)
	if len(warnings) != 0 || len(stale.Skills) != 1 {
		t.Fatalf("pre-change prune = %+v, warnings = %v", stale, warnings)
	}
	runtime.commands = nil
	if err := os.WriteFile(filepath.Join(runtime.skills["orca-cli"].Path, "SKILL.md"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := pruner.applyPruneAgentSkills(stale); err == nil || !strings.Contains(err.Error(), "changed after preview") {
		t.Fatalf("apply stale skill prune = %v", err)
	}
	if len(runtime.commands) != 0 {
		t.Fatalf("stale skill prune mutated live state: %v", runtime.commands)
	}
	unsafe, warnings := pruner.planAgentSkills(nil)
	if len(unsafe.Skills) != 0 || len(warnings) != 1 || !strings.Contains(warnings[0], "left untouched") {
		t.Fatalf("changed skill prune = %+v, warnings = %v", unsafe, warnings)
	}
}

func TestAgentSkillsUpdateRunsWithoutTheMiseResource(t *testing.T) {
	paths := testAgentSkillPaths(t)
	contract := testAgentSkills()
	contract.Sources[0].Skills = []string{"orca-cli"}
	runtime := newFakeAgentSkillsRuntime(paths)
	var output bytes.Buffer
	updater := Updater{
		Paths: paths, Version: "dev", SkillsProbe: runtime, SkillsLive: runtime,
		Log: Logger{Out: &output}, LoadMachine: func() (Machine, error) {
			return Machine{AgentSkills: &contract}, nil
		},
	}
	if err := updater.Apply(testUpdatePlan(UpdateSoftware, "")); err != nil {
		t.Fatal(err)
	}
	if runtime.updates != 1 || !strings.Contains(output.String(), "declared agent skills updated") {
		t.Fatalf("update count = %d, output = %s", runtime.updates, output.String())
	}
}

func TestAgentSkillsRestoreFollowsMiseWhenBothAreDeclared(t *testing.T) {
	machine := testMachine()
	contract := testAgentSkills()
	machine.AgentSkills = &contract
	steps := freshRestoreSteps(Applier{Machine: machine})
	var ids []string
	for _, step := range steps {
		ids = append(ids, step.id)
	}
	if planned := restoreStepIDs(machine); !slices.Equal(ids, planned) {
		t.Fatalf("restore execution = %v, plan = %v", ids, planned)
	}
	miseIndex := slices.Index(ids, restoreMiseStep)
	skillsIndex := slices.Index(ids, restoreAgentSkillsStep)
	if miseIndex < 0 || skillsIndex != miseIndex+1 {
		t.Fatalf("restore steps = %v", ids)
	}
}
