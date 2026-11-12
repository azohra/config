package config

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func itemLabels(group UpdateGroup) []string {
	var labels []string
	for _, item := range group.Items {
		labels = append(labels, item.Label())
	}
	return labels
}

func TestToolUpdatesComeFromNativeStateNotUpgradeOutput(t *testing.T) {
	native, runner := nativeFixture(t)
	runner.replies = map[string]Result{
		"outdated --global --json": {
			Stdout: `{"npm:example-cli":{"current":"1.0.0","latest":"1.1.0","bump":null},"node":{"current":"24.1.0","latest":"24.2.0","bump":null}}`,
			Stderr: "mise WARN  newer example release 2.1 ignored by minimum_release_age (24h)\n",
		},
		"outdated --global --bump --json": {
			Stdout: `{"node":{"current":"24.1.0","latest":"26.0.0","bump":"26"},"npm:example-cli":{"current":"1.0.0","latest":"1.1.0","bump":null}}`,
		},
	}
	group := native.planTools(context.Background())
	if want := []string{"node 24.1.0 → 24.2.0", "example-cli 1.0.0 → 1.1.0"}; group.State != UpdateAvailable || !slices.Equal(itemLabels(group), want) {
		t.Fatalf("tool updates = %+v", group)
	}
	if group.Items[1].Target != "npm:example-cli" {
		t.Fatalf("a tool lost the name mise selects it by: %+v", group.Items[1])
	}
	details := strings.Join(group.Details, "\n")
	if !strings.Contains(details, "not installed: node 26.0.0") || !strings.Contains(details, "minimum_release_age") {
		t.Fatalf("pinned versions and warnings are not noted: %q", details)
	}

	runner.replies["outdated --global --json"] = Result{Stdout: "{}"}
	if group := native.planTools(context.Background()); group.State != UpdateCurrent || len(group.Items) != 0 {
		t.Fatalf("current tools offered work: %+v", group)
	}
	runner.replies["outdated --global --json"] = Result{Err: errors.New("exit status 1"), Stderr: "mise ERROR no network\n"}
	if group := native.planTools(context.Background()); group.State != UpdateUnavailable {
		t.Fatalf("an unreadable tool state looked current: %+v", group)
	}
}

func TestPackageUpdatesNameEachAppItsVersionsAndWhatIsLeftAlone(t *testing.T) {
	native, runner := nativeFixture(t)
	runner.replies = map[string]Result{
		"bootstrap packages status --json": {Stdout: `{
		  "brew":{"packages":[{"package":"example-formula","installed_version":"1.0"}]},
		  "brew-cask":{"packages":[{"package":"example-app","installed_version":"1.0"},{"package":"example/tap/tapped","installed_version":"3.0"}]},
		  "mas":{"packages":[{"package":"123","name":"Example Store App","installed_version":"5.0"}]}
		}`},
		"bootstrap packages upgrade --yes --dry-run": {Stdout: strings.Join([]string{
			"pour example-formula/1.1 (requested)",
			"pour example-library/2.0 (dependency)",
			"install cask example-app/2.0",
			"install pkg Example.pkg",
			"link app /Applications/Example.app",
			"install cask tapped/3.1",
			"mas upgrade 123",
			"mise [gh-extension] gh extension upgrade 'stack'",
			"a step this release of Config does not know",
		}, "\n"), Stderr: "mise brew-cask:chat: skipped: installed app is running and updates itself\n" +
			"mise brew-cask:video: skipped: installed app is running and updates itself\n" +
			"mise brew-cask:fonts: already up to date\n"},
	}
	group := native.planApps(context.Background())
	want := []string{"example-formula 1.0 → 1.1", "example-app 1.0 → 2.0", "tapped 3.0 → 3.1"}
	if !slices.Equal(itemLabels(group), want) || !group.Elevated || group.State != UpdateAvailable {
		t.Fatalf("package updates = %q elevated=%v", itemLabels(group), group.Elevated)
	}
	if targets := []string{group.Items[0].Target, group.Items[2].Target}; !slices.Equal(targets, []string{"brew:example-formula", "brew-cask:example/tap/tapped"}) {
		t.Fatalf("package targets = %q", targets)
	}
	details := strings.Join(group.Details, "\n")
	if !strings.Contains(details, "Left alone, installed app is running and updates itself: chat, video") || strings.Contains(details, "fonts") {
		t.Fatalf("held packages = %q", details)
	}
	// mise prints these whether or not anything is newer, so they are not
	// counted as updates, and they are not hidden either.
	if also := "Also runs with the whole step, updates unknown until then: App Store upgrade for Example Store App; dependency example-library 2.0; gh extension upgrade 'stack'; a step this release of Config does not know"; !strings.Contains(details, also) {
		t.Fatalf("commands without a known update = %q", details)
	}

	// With no known update those commands still make the step runnable, and
	// a change among them is a changed plan.
	dryRun := runner.replies["bootstrap packages upgrade --yes --dry-run"]
	runner.replies["bootstrap packages upgrade --yes --dry-run"] = Result{Stdout: "mas upgrade 123\n", Stderr: dryRun.Stderr}
	idle := native.planApps(context.Background())
	if idle.State != UpdatePending || len(idle.Items) != 0 || idle.Count != 0 || idle.Changes != "App Store upgrade for Example Store App" {
		t.Fatalf("a step with only unverifiable commands = %+v", idle)
	}
	runner.replies["bootstrap packages upgrade --yes --dry-run"] = Result{Stderr: dryRun.Stderr}
	if current := native.planApps(context.Background()); current.State != UpdateCurrent {
		t.Fatalf("a step with nothing to run = %+v", current)
	}
}

func TestRepositoryUpdatesListCheckoutsBehindTheirRemote(t *testing.T) {
	native, runner := nativeFixture(t)
	runner.replies = map[string]Result{"bootstrap repos status --json": {Stdout: `{"repos":[
	  {"path_raw":"~/projects/behind","ref":"main","current_ref":"main","state":"differs"},
	  {"path_raw":"~/projects/branch","ref":"main","current_ref":"topic","state":"differs"},
	  {"path_raw":"~/projects/new","ref":"main","state":"missing"},
	  {"path_raw":"~/projects/edited","ref":"main","current_ref":"main","state":"dirty"},
	  {"path_raw":"~/projects/current","ref":"main","current_ref":"main","state":"current"},
	  {"path_raw":"~/sources/unpinned","ref":null,"current_ref":"main","state":"current"}
	]}`}}
	group := native.planRepositories(context.Background())
	if want := []string{"~/projects/behind (main)", "~/projects/branch topic → main", "~/projects/new (clone)"}; !slices.Equal(itemLabels(group), want) {
		t.Fatalf("repository updates = %q", itemLabels(group))
	}
	details := strings.Join(group.Details, "\n")
	if !strings.Contains(details, "local edits: ~/projects/edited") || !strings.Contains(details, "no ref declared; pulled when every repository updates: ~/sources/unpinned") {
		t.Fatalf("repository notes = %q", details)
	}
}

func TestFileUpdatesListEntriesThatAreNotApplied(t *testing.T) {
	native, runner := nativeFixture(t)
	runner.replies = map[string]Result{"dotfiles status --json": {Stdout: `{
	  "files":[{"target":"~/.applied","state":"applied"},{"target":"~/.missing","state":"missing"}],
	  "edits":[{"path":"~/.client.json","edit":"merge:shared","state":"differs"}]
	}`}}
	group := native.planFiles(context.Background())
	if want := []string{"~/.missing (missing)", "~/.client.json merge:shared (differs)"}; !slices.Equal(itemLabels(group), want) {
		t.Fatalf("file updates = %q", itemLabels(group))
	}
}

func TestMiseUpdatesItselfOnlyToANewerRelease(t *testing.T) {
	native, runner := nativeFixture(t)
	runner.replies = map[string]Result{"version --json": {Stdout: `{"version":"2026.11.4 macos-arm64 (2026-11-07)","latest":"2026.11.6"}`}}
	if group := native.planSelf(context.Background()); group.State != UpdateAvailable || group.Summary != "mise: 2026.11.4 → 2026.11.6" {
		t.Fatalf("mise update = %+v", group)
	}
	runner.replies["version --json"] = Result{Stdout: `{"version":"2026.11.6 macos-arm64","latest":"2026.11.4"}`}
	if group := native.planSelf(context.Background()); group.State != UpdateCurrent {
		t.Fatalf("an older release was offered: %+v", group)
	}
}

// A step is judged by planning it again, not by what mise printed: an item
// that is still pending afterwards was not updated.
func TestAStepReportsEachItemsOutcome(t *testing.T) {
	native, runner := nativeFixture(t)
	log := filepath.Join(t.TempDir(), "commands")
	t.Setenv("NATIVE_MISE_TEST_LOG", log)
	mise := native.Paths.InHome(".local", "bin", "mise")
	writeUpdateExecutable(t, mise, "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$NATIVE_MISE_TEST_LOG\"\necho 'mise ERROR example-cli: install failed' >&2\necho '  caused by: no release asset' >&2\nexit 1\n")
	var output bytes.Buffer
	native.Live.Stdout, native.Live.Stderr = &output, &output
	planned := UpdateGroup{ID: toolsStepID, Name: "Tools", Items: []UpdateItem{
		{Name: "node", From: "24.1.0", To: "24.2.0", Target: "node"},
		{Name: "example-cli", From: "1.0.0", To: "1.1.0", Target: "npm:example-cli"},
	}}.itemized()
	runner.replies = map[string]Result{
		"outdated --global --json": {Stdout: `{"npm:example-cli":{"current":"1.0.0","latest":"1.1.0"}}`},
	}
	step := native.run(planned, UpdateSoftware)
	if step.Status != StepFailed || step.Error != "example-cli: install failed: no release asset" || step.Command != "mise upgrade --global --no-prune --yes" {
		t.Fatalf("step = %+v", step)
	}
	if len(step.Items) != 2 || step.Items[0].Status != ItemUpdated || step.Items[1].Status != ItemFailed {
		t.Fatalf("item outcomes = %+v", step.Items)
	}
	if text := output.String(); !strings.Contains(text, "node 24.1.0 → 24.2.0") || !strings.Contains(text, "example-cli 1.0.0 → 1.1.0: not updated") {
		t.Fatalf("progress did not name each item:\n%s", text)
	}

	// A command that succeeds without changing an item leaves it unchanged,
	// and a selection passes only its targets.
	writeUpdateExecutable(t, mise, "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$NATIVE_MISE_TEST_LOG\"\n")
	planned.Items, planned.Partial = planned.Items[1:], true
	step = native.run(planned, UpdateSoftware)
	if step.Status != StepDone || step.Items[0].Status != ItemUnchanged {
		t.Fatalf("an item mise left alone = %+v", step)
	}
	commands, _ := os.ReadFile(log)
	if string(commands) != "upgrade --global --no-prune --yes\nupgrade --global --no-prune --yes npm:example-cli\n" {
		t.Fatalf("commands = %s", commands)
	}
}

func TestSelectionKeepsNamedItemsAndBindsThem(t *testing.T) {
	plan := UpdatePlan{Scope: UpdateSoftware, Groups: []UpdateGroup{
		UpdateGroup{ID: toolsStepID, Name: "Tools", Items: []UpdateItem{
			{Name: "node", To: "24.2.0", Target: "node"},
			{Name: "example-cli", To: "1.1.0", Target: "npm:example-cli"},
		}}.itemized(),
		UpdateGroup{ID: appsStepID, Name: "Apps and packages", Elevated: true, Items: []UpdateItem{
			{Name: "example-app", To: "2.0", Target: "brew-cask:example-app"},
			{Name: "node", To: "24.2.0", Target: "brew:node"},
		}}.itemized(),
		UpdateGroup{ID: repositoriesStepID, Name: "Repositories", Items: []UpdateItem{
			{Name: "~/projects/tools", Target: "~/projects/tools"},
		}}.itemized(),
	}}
	selected, err := plan.Select([]string{"Example-CLI"})
	if err != nil {
		t.Fatal(err)
	}
	tools, apps := selected.Groups[0], selected.Groups[1]
	if len(tools.Items) != 1 || tools.Items[0].Target != "npm:example-cli" || !tools.Partial || apps.State != UpdateSkipped {
		t.Fatalf("selection = %+v", selected.Groups)
	}
	if selected.Elevated() || selected.Fingerprint() == plan.Fingerprint() {
		t.Fatal("a selection kept the whole plan's identity or its administrator step")
	}
	// A step's id selects that step alone, even where an item shares the word.
	whole, err := plan.Select([]string{"tools", "apps:brew-cask:example-app"})
	if err != nil || whole.Groups[0].Partial || len(whole.Groups[0].Items) != 2 || len(whole.Groups[1].Items) != 1 || whole.Groups[2].State != UpdateSkipped {
		t.Fatalf("step and key selection = %+v, %v", whole.Groups, err)
	}
	if _, err := plan.Select([]string{"missing"}); err == nil || !strings.Contains(err.Error(), `no pending update is named "missing"`) {
		t.Fatalf("an unknown name = %v", err)
	}
	if _, err := plan.Select([]string{"node"}); err == nil || !strings.Contains(err.Error(), "choose tools:node or apps:brew:node") {
		t.Fatalf("a name two steps share = %v", err)
	}

	// Before a Config release hands off, nothing on the machine is planned.
	handoff := UpdatePlan{Scope: UpdateAll, Groups: []UpdateGroup{
		{ID: configStepID, Name: "Config", State: UpdateAvailable, Items: []UpdateItem{{Name: "Config", To: "v2.0.0", Target: configStepID}}},
		{ID: machineStepID, Name: "Machine updates", State: UpdatePending},
	}}
	if _, err := handoff.Select([]string{"node"}); err == nil || !strings.Contains(err.Error(), "after the new Config installs") {
		t.Fatalf("an item behind a release handoff = %v", err)
	}
	if _, err := handoff.Select([]string{"machine"}); err == nil || !strings.Contains(err.Error(), "select config as well") {
		t.Fatalf("machine updates without their release = %v", err)
	}
	if alone, err := handoff.Select([]string{"config"}); err != nil || alone.awaitsHandoff() {
		t.Fatalf("Config alone = %+v, %v", alone, err)
	}
}

func TestPlansBindItemsNotNotes(t *testing.T) {
	native, runner := nativeFixture(t)
	machine := "kind = \"azohra.config.machine\"\nschema = 8\n[repository]\nbranch = \"main\"\nurl = \"https://example.com/machine.git\"\n"
	if err := os.WriteFile(native.Paths.InRoot("config.toml"), []byte(machine), 0o600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, native.Paths.Root, "init", "--quiet")
	runner.replies = map[string]Result{
		"version --json":           {Stdout: `{"version":"` + releaseMiseVersion + `","latest":"` + releaseMiseVersion + `"}`},
		"outdated --global --json": {Stdout: "{}", Stderr: "mise WARN  newer example release 2.1 (eligible 18:01) ignored by minimum_release_age (24h)\n"},
	}
	updater := NewUpdater(native.Paths, io.Discard, "dev")
	updater.Native = &native
	plan, err := updater.Plan(UpdateSoftware)
	if err != nil || plan.HasWork() || len(plan.Groups) != 3 {
		t.Fatalf("a preview of notes offered work: %+v, %v", plan, err)
	}
	if got := plan.Groups[1].Details; len(got) != 1 || !strings.HasPrefix(got[0], "newer example release 2.1") {
		t.Fatalf("an idle preview dropped its warning: %q", got)
	}
	for _, command := range runner.calls {
		if strings.Contains(command, "upgrade") && !strings.Contains(command, "--dry-run") || strings.Contains(command, "self-update") {
			t.Fatalf("planning ran a mutation: %s", command)
		}
	}

	runner.replies["outdated --global --json"] = Result{Stdout: `{"example":{"current":"1.0","latest":"2.0"}}`, Stderr: runner.replies["outdated --global --json"].Stderr}
	plan, err = updater.Plan(UpdateSoftware)
	if err != nil || !plan.HasWork() {
		t.Fatalf("software preview = %+v, %v", plan, err)
	}
	runner.replies["outdated --global --json"] = Result{Stdout: `{"example":{"current":"1.0","latest":"2.0"}}`, Stderr: "mise WARN  newer example release 2.1 (eligible 18:02) ignored by minimum_release_age (24h)\n"}
	if again, err := updater.Plan(UpdateSoftware); err != nil || again.Fingerprint() != plan.Fingerprint() {
		t.Fatalf("changed notes invalidated a confirmed plan: %v", err)
	}
	runner.replies["outdated --global --json"] = Result{Stdout: `{"example":{"current":"1.0","latest":"2.1"}}`}
	if changed, _ := updater.Plan(UpdateSoftware); changed.Fingerprint() == plan.Fingerprint() {
		t.Fatal("a changed item kept the confirmed plan")
	}
}
