package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type miseConfigBindingState int

const (
	miseConfigBindingInvalid miseConfigBindingState = iota
	miseConfigBindingMissing
	miseConfigBindingEmpty
	miseConfigBindingCurrent
	miseConfigBindingConflict
)

func miseConfigSource(paths Paths) string {
	return paths.InRoot("mise")
}

func miseConfigDir(paths Paths) string {
	return paths.InHome(".config", "mise")
}

func inspectMiseConfigBinding(paths Paths) (miseConfigBindingState, string) {
	source := miseConfigSource(paths)
	info, err := os.Lstat(source)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return miseConfigBindingInvalid, "machine Mise declarations are missing under mise/"
		}
		return miseConfigBindingInvalid, fmt.Sprintf("machine Mise declarations are unreadable: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return miseConfigBindingInvalid, "machine Mise declarations under mise/ are not a directory"
	}

	target := miseConfigDir(paths)
	info, err = os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		return miseConfigBindingMissing, "connect ~/.config/mise to the machine declarations"
	}
	if err != nil {
		return miseConfigBindingConflict, fmt.Sprintf("%s is unreadable: %v", target, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		link, readErr := os.Readlink(target)
		if readErr != nil {
			return miseConfigBindingConflict, fmt.Sprintf("%s is unreadable: %v", target, readErr)
		}
		resolved := link
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(filepath.Dir(target), resolved)
		}
		if samePath(resolved, source) {
			return miseConfigBindingCurrent, ""
		}
		return miseConfigBindingConflict, fmt.Sprintf("%s points to %s; left untouched", target, link)
	}
	if !info.IsDir() {
		return miseConfigBindingConflict, fmt.Sprintf("%s already exists and is not Config-owned; left untouched", target)
	}
	entries, readErr := os.ReadDir(target)
	if readErr != nil {
		return miseConfigBindingConflict, fmt.Sprintf("%s is unreadable: %v", target, readErr)
	}
	if len(entries) == 0 {
		return miseConfigBindingEmpty, "replace the empty ~/.config/mise directory with the machine declarations"
	}
	return miseConfigBindingConflict, fmt.Sprintf("%s contains existing configuration; left untouched", target)
}

func miseConfigBindingCheck(paths Paths) Check {
	state, detail := inspectMiseConfigBinding(paths)
	if state == miseConfigBindingCurrent {
		return yes("global Mise configuration")
	}
	return no("global Mise configuration is not connected", detail)
}

func requireMiseConfigBinding(paths Paths) error {
	state, detail := inspectMiseConfigBinding(paths)
	if state == miseConfigBindingCurrent {
		return nil
	}
	return errors.New(detail)
}

func ensureMiseConfigBinding(paths Paths) error {
	state, detail := inspectMiseConfigBinding(paths)
	switch state {
	case miseConfigBindingCurrent:
		return nil
	case miseConfigBindingMissing, miseConfigBindingEmpty:
	default:
		return errors.New(detail)
	}

	// A symlink is one indivisible directory entry. If a process ends after an
	// empty placeholder is removed, the next run sees a missing path and safely
	// recreates it.
	target := miseConfigDir(paths)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("prepare global Mise configuration: %w", err)
	}
	if state == miseConfigBindingEmpty {
		if err := os.Remove(target); err != nil {
			return fmt.Errorf("replace empty global Mise configuration: %w", err)
		}
	}
	if err := os.Symlink(miseConfigSource(paths), target); err != nil {
		return fmt.Errorf("connect global Mise configuration: %w", err)
	}
	if err := syncDirectory(filepath.Dir(target)); err != nil {
		return fmt.Errorf("sync global Mise configuration: %w", err)
	}
	return nil
}
