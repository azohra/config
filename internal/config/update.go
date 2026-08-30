package config

import (
	"errors"
	"fmt"
	"io"
	"os"
)

type Updater struct {
	Mise   string
	Runner Runner
	Live   LiveRunner
	Log    Logger
}

func NewUpdater(paths Paths, out io.Writer) Updater {
	return Updater{
		Mise:   misePath(paths),
		Runner: NewMachineRunner(paths),
		Live:   NewMachineLiveRunner(paths),
		Log:    Logger{Out: out},
	}
}

func (u Updater) Update() error {
	info, err := os.Stat(u.Mise)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		return fmt.Errorf("mise unavailable at %s", u.Mise)
	}

	u.Log.Section("mise")
	if err := u.Live.Command("mise", "self-update", testedMiseVersion, "--yes", "--no-plugins"); err != nil {
		u.Log.Error(err.Error())
		return fmt.Errorf("mise: %w", err)
	}
	if err := requireTestedMise(u.Runner); err != nil {
		u.Log.Error(err.Error())
		return fmt.Errorf("mise: %w", err)
	}
	u.Log.OK("standalone mise set to " + testedMiseVersion)

	steps := []struct {
		name    string
		success string
		args    []string
	}{
		{"Tools", "declared tools updated", []string{"upgrade", "--yes"}},
		{"Packages", "declared packages updated", []string{"bootstrap", "packages", "upgrade", "--yes"}},
		{"Repositories", "clean repositories updated", []string{"bootstrap", "repos", "update", "--yes", "--skip-dirty"}},
	}

	var failures []error
	for _, step := range steps {
		u.Log.Section(step.name)
		if err := u.Live.Command("mise", step.args...); err != nil {
			u.Log.Error(err.Error())
			failures = append(failures, fmt.Errorf("%s: %w", step.name, err))
			continue
		}
		u.Log.OK(step.success)
	}
	return errors.Join(failures...)
}
