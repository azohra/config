package ui

import (
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	config "github.com/azohra/config/internal/config"
)

func elevatedPreview(scope config.UpdateScope) Model {
	plan := config.UpdatePlan{Scope: scope, Groups: []config.UpdateGroup{{
		Name: "Machine setup", State: config.UpdatePending, Summary: "Machine setup",
		Details:  []string{"Needs your administrator password", "Install 1 app: example-app"},
		Raw:      []string{"install cask example-app/1.0", "install pkg Example.pkg"},
		Changes:  "install cask example-app/1.0\ninstall pkg Example.pkg",
		Elevated: true,
	}}}
	var asked [][]string
	m := Model{screen: screenUpdate, executable: "/tmp/config", updateScope: scope, updatePreview: plan, width: 80, height: 30}
	m.sudo = func(args ...string) *exec.Cmd {
		asked = append(asked, args)
		return exec.Command("true")
	}
	return m
}

func TestElevatedPlansAskForAdministratorAccessBeforeRunning(t *testing.T) {
	m := elevatedPreview(config.UpdateRepair)
	next, command := m.updateUpdate(press(tea.KeyEnter))
	waiting := next.(Model)
	if command == nil || waiting.screen != screenUpdate || !waiting.authorizing || waiting.operation.label != "" {
		t.Fatalf("an elevated plan started before administrator access: %+v", waiting.operation)
	}
	if again, command := waiting.updateUpdate(press(tea.KeyEnter)); command != nil || again.(Model).operation.label != "" {
		t.Fatal("a second enter started the plan while the password was being asked")
	}

	args := []string{"--run-update", "repair", m.updatePreview.Fingerprint()}
	next, _ = waiting.Update(authorizedMsg{err: errors.New("exit status 1"), label: "Repair machine", args: args})
	refused := next.(Model)
	if refused.operation.label != "" || refused.authorizing || !strings.Contains(ansi.Strip(refused.renderUpdate()), "nothing changed") {
		t.Fatalf("refused access = %+v", refused)
	}

	next, command = waiting.Update(authorizedMsg{label: "Repair machine", args: args})
	running := next.(Model)
	if command == nil || running.screen != screenRunning || !slices.Equal(running.operation.args, args) {
		t.Fatalf("granted access did not run the reviewed plan: %+v", running.operation)
	}
	running.operation.cancel()
	if _, command := running.Update(renewAuthorizationMsg{started: running.operation.startedAt}); command == nil {
		t.Fatal("administrator access was not renewed during the operation")
	}
	if _, command := running.Update(renewAuthorizationMsg{started: running.operation.startedAt.Add(-time.Minute)}); command != nil {
		t.Fatal("an earlier operation's renewal kept running")
	}
}

func TestPlansWithoutRootStepsRunWithoutAPassword(t *testing.T) {
	m := elevatedPreview(config.UpdateSoftware)
	m.updatePreview.Groups[0].Elevated = false
	next, _ := m.updateUpdate(press(tea.KeyEnter))
	running := next.(Model)
	if running.screen != screenRunning || running.authorizing {
		t.Fatal("a plan without root steps asked for a password")
	}
	running.operation.cancel()
}

func TestNativeReviewShowsASummaryAndMisesCommandsOnRequest(t *testing.T) {
	m := elevatedPreview(config.UpdateRepair)
	summary := ansi.Strip(m.renderUpdate())
	if !strings.Contains(summary, "Install 1 app: example-app") || strings.Contains(summary, "install pkg") || !strings.Contains(summary, "d mise's commands") {
		t.Fatalf("summary view =\n%s", summary)
	}
	next, _ := m.updateUpdate(press('d'))
	commands := ansi.Strip(next.(Model).renderUpdate())
	if !strings.Contains(commands, "install pkg Example.pkg") || !strings.Contains(commands, "d summary") {
		t.Fatalf("commands view =\n%s", commands)
	}

	prune := Model{screen: screenPrune, width: 80, height: 30, prunePreview: "Remove 1 unused tool version: cmake 4.4.4", pruneCommands: "mise cmake@4.4.4 [dryrun]  uninstall", pruneHasWork: true}
	if view := ansi.Strip(prune.renderPrune()); !strings.Contains(view, "cmake 4.4.4") || strings.Contains(view, "[dryrun]") {
		t.Fatalf("cleanup summary =\n%s", view)
	}
	next, _ = prune.updatePrune(press('d'))
	if view := ansi.Strip(next.(Model).renderPrune()); !strings.Contains(view, "[dryrun]") {
		t.Fatalf("cleanup commands =\n%s", view)
	}
}
