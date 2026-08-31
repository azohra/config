package config

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// MaterializeRepository clones the requested configuration into Config's
// managed checkout. The locator carries public repository identity;
// authentication comes from the caller's environment. The boolean reports
// whether this exact checkout still has bootstrap restore work pending.
func MaterializeRepository(paths Paths, source string, stdout, stderr io.Writer) (Machine, bool, error) {
	if _, err := repositoryIdentity(source); err != nil {
		return Machine{}, false, err
	}
	if info, err := os.Lstat(paths.Root); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return Machine{}, false, fmt.Errorf("managed checkout %s is not a directory", paths.Root)
		}
		machine, err := validateMaterializedRepository(paths, source)
		if err != nil {
			return Machine{}, false, err
		}
		_, pending, err := pendingRestore(paths, machine, stdout)
		return machine, pending, err
	} else if !os.IsNotExist(err) {
		return Machine{}, false, err
	}

	parent := filepath.Dir(paths.Root)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return Machine{}, false, err
	}
	// A terminal interrupt during bootstrap skips the cleanup below and leaves
	// a whole clone of the machine repository beside the managed checkout.
	sweepStaging(parent, ".config-clone-")
	staging, err := os.MkdirTemp(parent, ".config-clone-")
	if err != nil {
		return Machine{}, false, err
	}
	defer os.RemoveAll(staging)

	checkout := filepath.Join(staging, "repository")
	runner := newLiveRunner(parent)
	runner.Stdout = stdout
	runner.Stderr = stderr
	if err := runner.Command("git", "clone", "--origin", managedRemote, "--", source, checkout); err != nil {
		return Machine{}, false, fmt.Errorf("clone configuration: %w", err)
	}
	stagedPaths := paths
	stagedPaths.Root = checkout
	machine, err := validateMaterializedRepository(stagedPaths, source)
	if err != nil {
		return Machine{}, false, err
	}
	progress, err := beginRestore(paths, stagedPaths, machine)
	if err != nil {
		return Machine{}, false, err
	}
	if err := os.Rename(checkout, paths.Root); err != nil {
		cleanupErr := os.Remove(restoreStatePath(paths, progress.record.Checkout))
		if cleanupErr != nil && !os.IsNotExist(cleanupErr) {
			return Machine{}, false, errors.Join(fmt.Errorf("install managed checkout: %w", err), fmt.Errorf("remove pending restore state: %w", cleanupErr))
		}
		return Machine{}, false, fmt.Errorf("install managed checkout: %w", err)
	}
	return machine, true, nil
}

func validateMaterializedRepository(paths Paths, source string) (Machine, error) {
	runner := newGitRunner(paths.Root)
	top := run(runner, "git", "rev-parse", "--show-toplevel")
	if top.Err != nil || !samePath(top.Output(), paths.Root) {
		return Machine{}, fmt.Errorf("%s is not a Git repository rooted at Config's managed checkout", paths.Root)
	}
	remote := run(runner, "git", "remote", "get-url", managedRemote)
	if remote.Err != nil {
		return Machine{}, fmt.Errorf("managed checkout has no %s remote", managedRemote)
	}
	if !sameRepositoryLocator(remote.Output(), source) {
		return Machine{}, fmt.Errorf("managed checkout origin is %s, not %s", remote.Output(), source)
	}
	machine, err := LoadMachine(paths)
	if err != nil {
		return Machine{}, err
	}
	if !sameRepositoryLocator(source, machine.Repository.URL) {
		return Machine{}, fmt.Errorf("machine contract declares %s, not %s", machine.Repository.URL, source)
	}
	status := snapshotStatus(paths, machine, runner)
	if status.PolicyError != "" {
		return Machine{}, fmt.Errorf("managed checkout: %s", status.PolicyError)
	}
	return machine, nil
}

func sameRepositoryLocator(left, right string) bool {
	leftIdentity, leftErr := repositoryIdentity(left)
	rightIdentity, rightErr := repositoryIdentity(right)
	return leftErr == nil && rightErr == nil && leftIdentity == rightIdentity
}

func repositoryIdentity(locator string) (string, error) {
	locator = strings.TrimSpace(locator)
	if locator == "" {
		return "", fmt.Errorf("repository locator is empty")
	}
	for _, character := range locator {
		if unicode.IsControl(character) {
			return "", fmt.Errorf("repository locator contains control characters")
		}
	}
	if at := strings.IndexByte(locator, '@'); at > 0 {
		if colon := strings.IndexByte(locator[at+1:], ':'); colon >= 0 && !strings.Contains(locator[:at], "/") {
			colon += at + 1
			if strings.Contains(locator[:at], ":") || strings.Contains(locator[at+1:], "@") {
				return "", fmt.Errorf("repository locator must not contain credentials")
			}
			host := strings.ToLower(locator[at+1 : colon])
			path := cleanRepositoryPath(locator[colon+1:])
			if host == "" || path == "" {
				return "", fmt.Errorf("repository locator is invalid")
			}
			return host + "/" + path, nil
		}
	}
	parsed, err := url.Parse(locator)
	if err != nil {
		return "", fmt.Errorf("repository locator is invalid")
	}
	if parsed.Scheme == "" {
		if !filepath.IsAbs(locator) {
			return "", fmt.Errorf("local repository locator must be an absolute path")
		}
		return "file:" + filepath.Clean(locator), nil
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("repository locator must not contain credentials, query parameters, or a fragment")
	}
	if parsed.User != nil {
		_, hasPassword := parsed.User.Password()
		if parsed.Scheme != "ssh" || hasPassword {
			return "", fmt.Errorf("repository locator must not contain credentials")
		}
	}
	if parsed.Scheme == "file" {
		if parsed.Host != "" && parsed.Host != "localhost" {
			return "", fmt.Errorf("file repository locator has an unsupported host")
		}
		path := filepath.FromSlash(parsed.Path)
		if parsed.Opaque != "" || !filepath.IsAbs(path) {
			return "", fmt.Errorf("file repository locator must contain an absolute path")
		}
		return "file:" + filepath.Clean(path), nil
	}
	if parsed.Scheme != "https" && parsed.Scheme != "ssh" {
		return "", fmt.Errorf("repository locator scheme %q is unsupported", parsed.Scheme)
	}
	host := strings.ToLower(parsed.Host)
	path := cleanRepositoryPath(parsed.Path)
	if host == "" || path == "" {
		return "", fmt.Errorf("repository locator is invalid")
	}
	return host + "/" + path, nil
}

func cleanRepositoryPath(path string) string {
	path = strings.Trim(strings.TrimSpace(filepath.ToSlash(path)), "/")
	path = strings.TrimSuffix(path, ".git")
	for _, segment := range strings.Split(path, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return ""
		}
	}
	return path
}
