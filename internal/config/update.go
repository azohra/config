package config

import (
	"errors"
	"fmt"
	"io"
	"os"
)

type Updater struct {
	Mise string
	Live LiveRunner
	Log  Logger
}

func NewUpdater(paths Paths, out io.Writer) Updater {
	return Updater{
		Mise: misePath(paths),
		Live: NewMachineLiveRunner(paths),
		Log:  Logger{Out: out},
	}
}

func (u Updater) Update() error {
	info, err := os.Stat(u.Mise)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		return fmt.Errorf("mise unavailable at %s", u.Mise)
	}

	steps := []struct {
		name    string
		success string
		args    []string
	}{
		{"mise", "standalone mise updated", []string{"self-update", "--yes"}},
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
