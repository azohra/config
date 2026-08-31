package config

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func configCommandPath(paths Paths) string {
	return paths.InHome(".local", "bin", "config")
}

// InstallCurrent makes the running binary Config's permanent command. A
// development build carries no release version and `config update` skips the
// release transition for one, so installing it says so rather than leaving the
// canonical command quietly pinned to a local build.
func InstallCurrent(paths Paths, version string, out io.Writer) error {
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate running Config: %w", err)
	}
	destination := configCommandPath(paths)
	if !stableConfigVersion(version) {
		Logger{Out: out}.Warn(destination + " is now an unversioned development build; " +
			"config update will not move it forward until a release replaces it")
	}
	return installExecutable(destination, executable)
}

func installExecutable(destination, source string) error {
	sourceInfo, err := os.Stat(source)
	if err != nil {
		return fmt.Errorf("inspect Config executable: %w", err)
	}
	if !sourceInfo.Mode().IsRegular() {
		return fmt.Errorf("Config executable %s is not a regular file", source)
	}

	if destinationInfo, statErr := os.Lstat(destination); statErr == nil {
		if destinationInfo.Mode().IsRegular() {
			resolved, resolveErr := os.Stat(destination)
			if resolveErr == nil && os.SameFile(sourceInfo, resolved) {
				return nil
			}
		} else if destinationInfo.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("Config command %s is not a regular file or symlink", destination)
		}
	} else if !os.IsNotExist(statErr) {
		return fmt.Errorf("inspect Config command: %w", statErr)
	}

	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("create Config command directory: %w", err)
	}
	staged, err := os.CreateTemp(parent, ".config-install-*")
	if err != nil {
		return fmt.Errorf("stage Config command: %w", err)
	}
	stagedName := staged.Name()
	defer os.Remove(stagedName)

	sourceFile, err := os.Open(source)
	if err != nil {
		staged.Close()
		return fmt.Errorf("open Config executable: %w", err)
	}
	_, copyErr := io.Copy(staged, sourceFile)
	closeSourceErr := sourceFile.Close()
	chmodErr := staged.Chmod(0o755)
	syncErr := staged.Sync()
	closeStageErr := staged.Close()
	for _, failure := range []struct {
		name string
		err  error
	}{
		{"copy Config executable", copyErr},
		{"close Config executable", closeSourceErr},
		{"make Config command executable", chmodErr},
		{"sync Config command", syncErr},
		{"close staged Config command", closeStageErr},
	} {
		if failure.err != nil {
			return fmt.Errorf("%s: %w", failure.name, failure.err)
		}
	}
	if equal, compareErr := equalFileContents(source, stagedName); compareErr != nil {
		return fmt.Errorf("verify Config command: %w", compareErr)
	} else if !equal {
		return fmt.Errorf("verify Config command: staged bytes differ from the running executable")
	}
	if err := os.Rename(stagedName, destination); err != nil {
		return fmt.Errorf("install Config command: %w", err)
	}
	directory, err := os.Open(parent)
	if err == nil {
		err = directory.Sync()
		closeErr := directory.Close()
		if err == nil {
			err = closeErr
		}
	}
	if err != nil {
		return fmt.Errorf("sync Config command directory: %w", err)
	}
	return nil
}

func equalFileContents(left, right string) (bool, error) {
	leftData, err := os.ReadFile(left)
	if err != nil {
		return false, err
	}
	rightData, err := os.ReadFile(right)
	if err != nil {
		return false, err
	}
	return bytes.Equal(leftData, rightData), nil
}
