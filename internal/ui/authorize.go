package ui

import (
	"errors"
	"os/exec"
	"time"

	tea "charm.land/bubbletea/v2"
)

// Some native installers run as root. mise asks for a password only on a
// terminal, and an operation reaches mise through pipes, so Config asks first.
// sudo keeps the approval for this terminal session, which the operation and
// mise share, and Config renews it while the operation runs.

const authorizationRenewal = time.Minute

type authorizedMsg struct {
	err   error
	label string
	args  []string
}

type renewAuthorizationMsg struct{ started time.Time }

func (m Model) sudoCommand(args ...string) *exec.Cmd {
	if m.sudo != nil {
		return m.sudo(args...)
	}
	return exec.Command("sudo", args...)
}

func (m Model) authorizeThen(label string, args ...string) (tea.Model, tea.Cmd) {
	m.authorizing, m.authorizationError = true, nil
	prompt := m.sudoCommand("-v", "-p", "An installer in this plan runs as administrator.\nPassword for %u: ")
	return m, tea.ExecProcess(prompt, func(err error) tea.Msg {
		return authorizedMsg{err: err, label: label, args: args}
	})
}

func (m Model) authorized(msg authorizedMsg) (tea.Model, tea.Cmd) {
	m.authorizing = false
	if msg.err != nil {
		m.authorizationError = errors.New("Administrator access was not granted, so nothing changed.")
		return m, nil
	}
	next, start := m.startOperation(msg.label, m.executable, msg.args...)
	model := next.(Model)
	return model, tea.Batch(start, renewAuthorization(model.operation.startedAt))
}

func renewAuthorization(started time.Time) tea.Cmd {
	return tea.Tick(authorizationRenewal, func(time.Time) tea.Msg { return renewAuthorizationMsg{started: started} })
}

func (m Model) renewAuthorization(msg renewAuthorizationMsg) (tea.Model, tea.Cmd) {
	if m.screen != screenRunning || !m.operation.startedAt.Equal(msg.started) {
		return m, nil
	}
	renew := m.sudoCommand("-n", "-v")
	return m, tea.Batch(func() tea.Msg {
		_ = renew.Run()
		return nil
	}, renewAuthorization(msg.started))
}
