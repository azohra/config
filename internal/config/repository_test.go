package config

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func machineDocumentForRepository(source string) string {
	return strings.Replace(
		validMachineTOML(),
		`url = "https://example.com/owner/machine.git"`,
		`url = "`+source+`"`,
		1,
	)
}

func managedTestPaths(t *testing.T) Paths {
	t.Helper()
	home := t.TempDir()
	return Paths{
		Root:     filepath.Join(home, "managed", "repository"),
		Home:     home,
		StateDir: filepath.Join(home, "state"),
	}
}

func TestMaterializeRepositoryOwnsTheCanonicalCheckout(t *testing.T) {
	home := t.TempDir()
	paths, err := NewPaths("", home)
	if err != nil {
		t.Fatal(err)
	}
	wantRoot := filepath.Join(home, "Library", "Application Support", "Config", "repository")
	if paths.Root != wantRoot {
		t.Fatalf("managed checkout = %s, want %s", paths.Root, wantRoot)
	}
	source := filepath.Join(t.TempDir(), "machine.git")
	source = repositoryFixtureAt(t, source, machineDocumentForRepository(source))

	machine, fresh, err := MaterializeRepository(paths, source, io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !fresh || machine.Repository.URL != source {
		t.Fatalf("materialized = fresh %t, machine %+v", fresh, machine)
	}
	if _, err := os.Stat(paths.InRoot("config.toml")); err != nil {
		t.Fatal(err)
	}
	_, fresh, err = MaterializeRepository(paths, source, io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if fresh {
		t.Fatal("existing managed checkout reported fresh")
	}
}

func repositoryFixtureAt(t *testing.T, remote, machineDocument string) string {
	t.Helper()
	work := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(remote), 0o755); err != nil {
		t.Fatal(err)
	}
	gitTest(t, t.TempDir(), "init", "--quiet", "--bare", remote)
	gitTest(t, work, "init", "--quiet", "--initial-branch=main")
	gitTest(t, work, "config", "user.name", "Config Test")
	gitTest(t, work, "config", "user.email", "config@example.com")
	gitTest(t, work, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(work, "config.toml"), []byte(machineDocument), 0o600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, work, "add", "config.toml")
	gitTest(t, work, "commit", "--quiet", "-m", "Initial machine")
	gitTest(t, work, "remote", "add", "origin", remote)
	gitTest(t, work, "push", "--quiet", "--set-upstream", "origin", "main")
	gitTest(t, work, "--git-dir="+remote, "symbolic-ref", "HEAD", "refs/heads/main")
	return remote
}

func TestMaterializeRepositoryRefusesWrongOrInvalidState(t *testing.T) {
	goodSource := filepath.Join(t.TempDir(), "good.git")
	goodSource = repositoryFixtureAt(t, goodSource, machineDocumentForRepository(goodSource))
	wrongSource := filepath.Join(t.TempDir(), "wrong.git")
	wrongSource = repositoryFixtureAt(t, wrongSource, machineDocumentForRepository(wrongSource))
	paths := managedTestPaths(t)
	if _, _, err := MaterializeRepository(paths, goodSource, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, _, err := MaterializeRepository(paths, wrongSource, io.Discard, io.Discard); err == nil {
		t.Fatal("existing checkout accepted another repository")
	}

	badPaths := managedTestPaths(t)
	badSource := filepath.Join(t.TempDir(), "bad.git")
	badDocument := strings.Replace(machineDocumentForRepository(badSource), MachineKind, "wrong.machine", 1)
	badSource = repositoryFixtureAt(t, badSource, badDocument)
	if _, _, err := MaterializeRepository(badPaths, badSource, io.Discard, io.Discard); err == nil {
		t.Fatal("invalid machine contract was installed")
	}
	if _, err := os.Stat(badPaths.Root); !os.IsNotExist(err) {
		t.Fatalf("invalid clone left managed checkout behind: %v", err)
	}

	symlinkPaths := managedTestPaths(t)
	foreign := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(symlinkPaths.Root), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(foreign, symlinkPaths.Root); err != nil {
		t.Fatal(err)
	}
	if _, _, err := MaterializeRepository(symlinkPaths, goodSource, io.Discard, io.Discard); err == nil {
		t.Fatal("managed checkout followed a symlink")
	}
	entries, err := os.ReadDir(foreign)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatal("refused symlink target was modified")
	}
}

func TestRepositoryLocatorNeverCarriesCredentials(t *testing.T) {
	for _, locator := range []string{
		"https://token@github.com/owner/repository.git",
		"https://user:token@github.com/owner/repository.git",
		"https://github.com/owner/repository.git?token=secret",
	} {
		if _, err := repositoryIdentity(locator); err == nil {
			t.Errorf("credential-bearing locator accepted: %s", locator)
		}
	}
	if _, err := repositoryIdentity("https://github.com/owner/../repository.git"); err == nil {
		t.Fatal("ambiguous repository path was accepted")
	}
	for _, locator := range []string{"relative/repository.git", "~/repository.git", "file:relative/repository.git"} {
		if _, err := repositoryIdentity(locator); err == nil {
			t.Errorf("relative local locator accepted: %s", locator)
		}
	}
	if !sameRepositoryLocator(
		"https://github.com/owner/repository.git",
		"git@github.com:owner/repository.git",
	) {
		t.Fatal("HTTPS and SSH forms of the same repository did not match")
	}
}
