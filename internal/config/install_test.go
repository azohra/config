package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallExecutableCreatesAnExactPermanentCommand(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "released-config")
	destination := filepath.Join(root, "home", ".local", "bin", "config")
	content := []byte("released config bytes\n")
	if err := os.WriteFile(source, content, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := installExecutable(destination, source); err != nil {
		t.Fatal(err)
	}
	installed, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(installed) != string(content) || info.Mode().Perm() != 0o755 {
		t.Fatalf("installed bytes/mode = %q %o", installed, info.Mode().Perm())
	}
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(destination), ".config-install-*"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("staged files remain: %v, %v", matches, err)
	}
}

func TestInstallExecutableReplacesASymlinkWithoutChangingItsTarget(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "released-config")
	oldTarget := filepath.Join(root, "mise-shim-target")
	destination := filepath.Join(root, "bin", "config")
	if err := os.WriteFile(source, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldTarget, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(oldTarget, destination); err != nil {
		t.Fatal(err)
	}
	if err := installExecutable(destination, source); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(destination); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("destination was not replaced by a regular file: %v, %v", info, err)
	}
	if old, err := os.ReadFile(oldTarget); err != nil || string(old) != "old" {
		t.Fatalf("symlink target changed: %q, %v", old, err)
	}
}

func TestInstallExecutableRefusesADirectoryDestination(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "released-config")
	destination := filepath.Join(root, "config")
	if err := os.WriteFile(source, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(destination, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := installExecutable(destination, source); err == nil || !strings.Contains(err.Error(), "not a regular file or symlink") {
		t.Fatalf("error = %v", err)
	}
}

func TestInstallExecutableIsANoopForTheInstalledFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte("same"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := installExecutable(path, path); err != nil {
		t.Fatal(err)
	}
}
