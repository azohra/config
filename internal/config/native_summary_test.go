package config

import (
	"slices"
	"strings"
	"testing"
)

// The shape of a mise 2026.10.21 bootstrap dry run with work in most parts.
const nativeRepairPreview = `install cask example-app/7.2.2
install pkg ExampleInstaller.pkg
install cask example-camera/2.2.5,build25
link app /Applications/Example Camera.app
sh -o errexit -c '"$HOME/.local/bin/mise" install "github:example/agent" --yes'
sh -o errexit -c 'PATH="/opt/homebrew/bin:$PATH" "$HOME/bin/example-agent" setup --from-ref "op://Vault/item/field"'
git -C /Users/example/Development/site -c 'safe.directory=/Users/example/Development/site' fetch --prune --tags origin
git -C /Users/example/Development/site -c 'safe.directory=/Users/example/Development/site' checkout main
git -C /Users/example/Development/site -c 'safe.directory=/Users/example/Development/site' pull --ff-only origin main
git clone --branch main https://example.com/tools.git /Users/example/Development/tools
git -C /Users/example/.local/share/mise/skill-sources/example/skills -c 'safe.directory=x' pull --ff-only origin main
edit ~/.gitconfig (block:machine-template) (if changed)
sh -o errexit -c 'hidutil property --set '\''{"UserKeyMapping":[]}'\'''
macOS preference com.apple.dock persistent-apps = ["/Applications/Example.app","/Applications/Other.app"]
sh -o errexit -c 'killall Dock || true'
bootstrap: follow-up if applied
  - relaunch apps that read changed macOS defaults
mise bootstrap: system packages
mise brew-cask: 24 package(s) already installed
mise repos: 36 repo(s) already current
mise all tools are installed
something mise added later`

func TestNativeSummaryDescribesARepairPlan(t *testing.T) {
	_, changes := nativePreview(Result{Stdout: nativeRepairPreview})
	summary := summarizeNative(changes)
	want := []string{
		"Needs your administrator password",
		"Install 2 apps: example-app, example-camera",
		"Clone 1 repository: tools",
		"Update 1 repository: site; fast-forward only",
		"Refresh 1 skill source: example/skills",
		"Set 1 preference: Dock persistent-apps (2 items)",
		"Edit 1 file: .gitconfig if it changed",
		"Run 4 hooks: mise install, example-agent setup, hidutil property, killall Dock; hooks run every time",
		"Other steps:",
		"  something mise added later",
	}
	if !summary.Elevated || !slices.Equal(summary.Lines, want) {
		t.Fatalf("repair summary =\n%s", strings.Join(summary.Lines, "\n"))
	}
}

func TestNativeSummaryDescribesSoftwareAndCleanup(t *testing.T) {
	_, changes := nativePreview(Result{
		Stdout: "Would install github:example/oh-my-tool@18.13.3\nWould install npm:@scope/agent@1.4.0\nmas upgrade 111 222\n",
		Stderr: "mise [gh-extension] gh extension upgrade 'stack'\n" +
			"mise cmake@4.4.4 is prunable: no tracked config or tool stub requires cmake\n" +
			"mise cmake@4.4.4 [dryrun]                uninstall\n" +
			"mise cmake@4.4.4 [dryrun]                remove ~/.local/share/mise/installs/cmake/4.4.4\n" +
			"mise cmake@4.4.4 [dryrun]              ✓ done\n" +
			"remove brew-cask:example@1.0\n",
	})
	summary := summarizeNative(changes)
	want := []string{
		"Needs your administrator password",
		"Upgrade 2 tools: oh-my-tool 18.13.3, agent 1.4.0",
		"Update 2 App Store apps",
		"Remove 1 unused tool version: cmake 4.4.4",
		"Remove 1 package: brew-cask:example 1.0",
		"gh-extension: gh extension upgrade 'stack'",
	}
	if !slices.Equal(summary.Lines, want) {
		t.Fatalf("summary =\n%s", strings.Join(summary.Lines, "\n"))
	}
}

func TestNativeFailureReportsMisesError(t *testing.T) {
	native, _ := nativeFixture(t)
	writeUpdateExecutable(t, native.Paths.InHome(".local", "bin", "mise"), `#!/bin/sh
echo "mise brew-cask:example-app            ✗ failed" >&2
echo "mise ERROR sudo requires a password but no TTY is available. Run manually:" >&2
echo "  sudo installer -pkg /tmp/Example.pkg -target /" >&2
echo "mise ERROR Version: 2026.10.21 macos-arm64 (2026-10-27)" >&2
exit 1
`)
	var output strings.Builder
	native.Live.Stdout, native.Live.Stderr = &output, &output
	step := native.run(UpdateGroup{ID: appsStepID, Name: "Apps and packages"}, UpdateSoftware)
	if step.Status != StepFailed || step.Error != "an installer needs your administrator password, and mise had no terminal to ask for it; run `config update software --yes` in a terminal" {
		t.Fatalf("native failure = %+v", step)
	}
	if !strings.Contains(output.String(), "sudo installer -pkg") || !strings.Contains(step.Output, "sudo installer -pkg") {
		t.Fatalf("provider output was not kept:\n%s\n%s", output.String(), step.Output)
	}

	writeUpdateExecutable(t, native.Paths.InHome(".local", "bin", "mise"), "#!/bin/sh\nexit 3\n")
	if step := native.run(UpdateGroup{ID: repositoriesStepID, Name: "Repositories"}, UpdateRepositories); !strings.Contains(step.Error, "exit status 3") {
		t.Fatalf("a failure without a mise error lost its status: %+v", step)
	}
}

func TestWarningsAreNotesNotWork(t *testing.T) {
	preview := "mise WARN  Plugin gh-extension already installed\n" +
		"mise WARN  Use --force to install anyway\n" +
		"mise WARN  newer example release 2.1 ignored by minimum_release_age (24h)\n"
	shown, changes := nativePreview(Result{Stderr: preview})
	if changes != "" {
		t.Fatalf("a note was planned as work: %q", changes)
	}
	if got := nativeWarnings(shown); !slices.Equal(got, []string{"newer example release 2.1 ignored by minimum_release_age (24h)"}) {
		t.Fatalf("notes = %q", got)
	}
}

func TestASummaryNeverHidesAllOfItsChanges(t *testing.T) {
	if got := summarizeNative("repo: ~/projects/example").Lines; !slices.Equal(got, []string{"Other steps:", "  repo: ~/projects/example"}) {
		t.Fatalf("summary = %q", got)
	}
}
