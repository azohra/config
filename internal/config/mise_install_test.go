package config

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestMiseInstallerVerifiesAndAtomicallyInstallsTheRelease(t *testing.T) {
	content := []byte("#!/bin/sh\nprintf '%s\\n' '" + testedMiseVersion + "'\n")
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write(content)
	}))
	defer server.Close()

	destination := filepath.Join(t.TempDir(), "bin", "mise")
	old := filepath.Join(t.TempDir(), "old")
	if err := os.WriteFile(old, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(old, destination); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	installer := miseInstaller{
		Destination: destination,
		URL:         server.URL,
		SHA256:      fmt.Sprintf("%x", sum),
		Size:        int64(len(content)),
		Client:      server.Client(),
	}
	if err := installer.Install(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o755 {
		t.Fatalf("installed mode = %v", info.Mode())
	}
	if actual, err := os.ReadFile(destination); err != nil || string(actual) != string(content) {
		t.Fatalf("installed bytes = %q, %v", actual, err)
	}
}

func TestMiseInstallerRefusesUnverifiedBytes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte("not the pinned release"))
	}))
	defer server.Close()
	destination := filepath.Join(t.TempDir(), "mise")
	if err := os.WriteFile(destination, []byte("preserve"), 0o755); err != nil {
		t.Fatal(err)
	}
	installer := miseInstaller{Destination: destination, URL: server.URL,
		SHA256: fmt.Sprintf("%064x", 0), Size: int64(len("not the pinned release")), Client: server.Client()}
	if err := installer.Install(); err == nil {
		t.Fatal("unverified Mise release was installed")
	}
	if actual, err := os.ReadFile(destination); err != nil || string(actual) != "preserve" {
		t.Fatalf("failed verification changed the destination: %q, %v", actual, err)
	}
}

func TestMiseInstallerRefusesAnUnexpectedReleaseSize(t *testing.T) {
	content := []byte("not the pinned size")
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write(content)
	}))
	defer server.Close()
	destination := filepath.Join(t.TempDir(), "mise")
	if err := os.WriteFile(destination, []byte("preserve"), 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	installer := miseInstaller{Destination: destination, URL: server.URL,
		SHA256: fmt.Sprintf("%x", sum), Size: int64(len(content) - 1), Client: server.Client()}
	if err := installer.Install(); err == nil {
		t.Fatal("unexpected Mise release size was installed")
	}
	if actual, err := os.ReadFile(destination); err != nil || string(actual) != "preserve" {
		t.Fatalf("failed size verification changed the destination: %q, %v", actual, err)
	}
}

type installingMiseRunner struct {
	installed bool
}

func (r *installingMiseRunner) Run(_ context.Context, name string, args ...string) Result {
	if name == "mise" && len(args) == 1 && args[0] == "--version" && r.installed {
		return Result{Stdout: testedMiseVersion}
	}
	return Result{}
}

func (r *installingMiseRunner) Exists(string) bool { return r.installed }

func TestEnsureTestedMiseInstallsOnlyWhenNeeded(t *testing.T) {
	runner := &installingMiseRunner{}
	installed := 0
	install := func() error {
		installed++
		runner.installed = true
		return nil
	}
	if err := ensureTestedMise(runner, install); err != nil {
		t.Fatal(err)
	}
	if err := ensureTestedMise(runner, install); err != nil {
		t.Fatal(err)
	}
	if installed != 1 {
		t.Fatalf("installer ran %d times, want once", installed)
	}
}
