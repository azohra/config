package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const (
	repositoryHooksID            = "repository-hooks"
	repositoryHooksName          = "Repository hooks"
	repositoryHookManifestName   = "config-hooks.json"
	repositoryHookManifestSchema = 1
)

// RepositoryHook declares one hook body owned by the machine repository.
// Config owns its installation into every repository the machine declares.
type RepositoryHook struct {
	Name   string `toml:"name"`
	Source string `toml:"source"`
}

type repositoryHookPayload struct {
	Name   string
	Body   []byte
	Mode   os.FileMode
	Digest string
}

type repositoryHookManifest struct {
	Schema int               `json:"schema"`
	Hooks  map[string]string `json:"hooks"`
}

type repositoryHookTarget struct {
	Name     string
	Dir      string
	Conflict string
}

type repositoryHookPlacement struct {
	Target  repositoryHookTarget
	Hook    repositoryHookPayload
	Current bool
	Managed bool
	Detail  string
}

func repositoryHookTemplateDir(paths Paths) string {
	return paths.InHome(".config", "git", "template")
}

func repositoryHookTemplateEnvironment(paths Paths) []string {
	return []string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=init.templateDir",
		"GIT_CONFIG_VALUE_0=" + repositoryHookTemplateDir(paths),
	}
}

func repositoryHookDigest(body []byte) string {
	digest := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func repositoryHookPayloads(paths Paths, declarations []RepositoryHook) ([]repositoryHookPayload, error) {
	payloads := make([]repositoryHookPayload, 0, len(declarations))
	for _, declaration := range declarations {
		source := paths.InRoot(filepath.FromSlash(declaration.Source))
		info, err := regularFileInside(paths.Root, source)
		if err != nil {
			return nil, fmt.Errorf("repository hook %q source: %w", declaration.Name, err)
		}
		if info.Mode().Perm()&0o111 == 0 {
			return nil, fmt.Errorf("repository hook %q source is not executable", declaration.Name)
		}
		body, err := os.ReadFile(source)
		if err != nil {
			return nil, fmt.Errorf("read repository hook %q source: %w", declaration.Name, err)
		}
		payloads = append(payloads, repositoryHookPayload{
			Name: declaration.Name, Body: body, Mode: info.Mode().Perm(), Digest: repositoryHookDigest(body),
		})
	}
	return payloads, nil
}

// regularFileInside rejects symlinks in every path component. A hook may run
// during a clone, outside Config's process, so its declared bytes must remain
// inside the authenticated machine repository.
func regularFileInside(root, path string) (os.FileInfo, error) {
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == "." || !filepath.IsLocal(relative) {
		return nil, errors.New("path leaves the managed repository")
	}
	current := root
	parts := strings.Split(filepath.Clean(relative), string(filepath.Separator))
	for index, part := range parts {
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if statErr != nil {
			return nil, statErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("path contains a symlink")
		}
		if index < len(parts)-1 && !info.IsDir() {
			return nil, errors.New("parent is not a directory")
		}
		if index == len(parts)-1 {
			if !info.Mode().IsRegular() {
				return nil, errors.New("source is not a regular file")
			}
			return info, nil
		}
	}
	return nil, errors.New("source is not a regular file")
}

func repositoryHookTargets(paths Paths, runner Runner, includeRepositories bool) ([]repositoryHookTarget, error) {
	targets := []repositoryHookTarget{{Name: "Git template", Dir: filepath.Join(repositoryHookTemplateDir(paths), "hooks")}}
	if !includeRepositories {
		return targets, nil
	}
	repositories, err := miseRepositories(paths, runner)
	if err != nil {
		return nil, err
	}
	repositoryPaths := []string{paths.Root}
	seen := map[string]bool{filepath.Clean(paths.Root): true}
	for _, repository := range repositories {
		if _, statErr := os.Lstat(filepath.Join(repository.Path, ".git")); errors.Is(statErr, os.ErrNotExist) {
			continue
		} else if statErr != nil {
			return nil, fmt.Errorf("inspect %s: %w", repository.Path, statErr)
		}
		path := filepath.Clean(repository.Path)
		if !seen[path] {
			seen[path] = true
			repositoryPaths = append(repositoryPaths, path)
		}
	}
	slices.Sort(repositoryPaths)
	seenTargets := map[string]int{}
	for _, repository := range repositoryPaths {
		common := run(runner, "git", "-C", repository, "rev-parse", "--path-format=absolute", "--git-common-dir")
		if common.Err != nil {
			return nil, fmt.Errorf("resolve hooks for %s: %w", repository, common.Failure())
		}
		dir := filepath.Join(common.Output(), "hooks")
		effective := run(runner, "git", "-C", repository, "rev-parse", "--path-format=absolute", "--git-path", "hooks")
		if effective.Err != nil {
			return nil, fmt.Errorf("resolve effective hooks for %s: %w", repository, effective.Failure())
		}
		var conflict string
		if filepath.Clean(effective.Output()) != filepath.Clean(dir) {
			conflict = "core.hooksPath redirects this repository to " + effective.Output()
		}
		key := filepath.Clean(dir)
		if index, seen := seenTargets[key]; seen {
			if targets[index].Conflict == "" {
				targets[index].Conflict = conflict
			}
			continue
		}
		seenTargets[key] = len(targets)
		targets = append(targets, repositoryHookTarget{
			Name: filepath.Base(repository), Dir: dir, Conflict: conflict,
		})
	}
	return targets, nil
}

func readRepositoryHookManifest(dir string) (repositoryHookManifest, error) {
	manifest := repositoryHookManifest{Schema: repositoryHookManifestSchema, Hooks: map[string]string{}}
	path := filepath.Join(dir, repositoryHookManifestName)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return manifest, nil
	}
	if err != nil {
		return repositoryHookManifest{}, err
	}
	if !info.Mode().IsRegular() {
		return repositoryHookManifest{}, errors.New("manifest is not a regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return repositoryHookManifest{}, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return repositoryHookManifest{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return repositoryHookManifest{}, errors.New("manifest contains trailing data")
	}
	if manifest.Schema != repositoryHookManifestSchema || manifest.Hooks == nil {
		return repositoryHookManifest{}, errors.New("unsupported manifest")
	}
	for name, digest := range manifest.Hooks {
		if !contractIDPattern.MatchString(name) || !validRepositoryHookDigest(digest) {
			return repositoryHookManifest{}, errors.New("invalid manifest entry")
		}
	}
	return manifest, nil
}

func validRepositoryHookDigest(value string) bool {
	digest, found := strings.CutPrefix(value, "sha256:")
	if !found || len(digest) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(digest)
	return err == nil
}

func repositoryHookPlacements(paths Paths, payloads []repositoryHookPayload, targets []repositoryHookTarget) ([]repositoryHookPlacement, error) {
	var placements []repositoryHookPlacement
	for _, target := range targets {
		conflict, err := repositoryHookTargetConflict(target)
		if err != nil {
			return nil, err
		}
		if conflict != "" {
			for _, hook := range payloads {
				placements = append(placements, repositoryHookPlacement{Target: target, Hook: hook, Detail: conflict})
			}
			continue
		}
		manifest, err := readRepositoryHookManifest(target.Dir)
		if err != nil {
			return nil, fmt.Errorf("read hook ownership for %s: %w", target.Name, err)
		}
		for _, hook := range payloads {
			placement, err := repositoryHookPlacementAt(paths, target, hook, manifest)
			if err != nil {
				return nil, err
			}
			placements = append(placements, placement)
		}
	}
	return placements, nil
}

func repositoryHookTargetConflict(target repositoryHookTarget) (string, error) {
	if target.Conflict != "" {
		return target.Conflict, nil
	}
	dirInfo, dirErr := os.Lstat(target.Dir)
	if dirErr == nil && dirInfo.Mode()&os.ModeSymlink != 0 {
		return "hooks directory is a repository-owned symlink", nil
	}
	if dirErr == nil && !dirInfo.IsDir() {
		return "hooks path is not a directory", nil
	}
	if dirErr != nil && !errors.Is(dirErr, os.ErrNotExist) {
		return "", fmt.Errorf("inspect %s hooks directory: %w", target.Name, dirErr)
	}
	return "", nil
}

func repositoryHookPlacementAt(paths Paths, target repositoryHookTarget, hook repositoryHookPayload, manifest repositoryHookManifest) (repositoryHookPlacement, error) {
	placement := repositoryHookPlacement{Target: target, Hook: hook}
	destination := filepath.Join(target.Dir, hook.Name)
	info, err := os.Lstat(destination)
	if errors.Is(err, os.ErrNotExist) {
		placement.Managed = true
		placement.Detail = "missing"
		return placement, nil
	}
	if err != nil {
		return placement, fmt.Errorf("inspect %s hook %s: %w", target.Name, hook.Name, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		link, readErr := os.Readlink(destination)
		if readErr != nil {
			return placement, fmt.Errorf("read %s hook %s link: %w", target.Name, hook.Name, readErr)
		}
		if !filepath.IsAbs(link) {
			link = filepath.Join(target.Dir, link)
		}
		if pathInside(paths.Root, link) {
			placement.Managed = true
			placement.Detail = "legacy managed link"
			return placement, nil
		}
		placement.Detail = "conflicts with a repository-owned symlink"
		return placement, nil
	}
	if !info.Mode().IsRegular() {
		placement.Detail = "is not a regular file"
		return placement, nil
	}
	body, err := os.ReadFile(destination)
	if err != nil {
		return placement, fmt.Errorf("read %s hook %s: %w", target.Name, hook.Name, err)
	}
	digest := repositoryHookDigest(body)
	recorded, recordedByConfig := manifest.Hooks[hook.Name]
	placement.Managed = digest == hook.Digest || (recordedByConfig && recorded == digest)
	placement.Current = digest == hook.Digest && info.Mode().Perm() == hook.Mode && recordedByConfig && recorded == hook.Digest
	switch {
	case placement.Current:
		placement.Detail = "current"
	case placement.Managed && digest == hook.Digest:
		placement.Detail = "ownership or mode needs refresh"
	case placement.Managed:
		placement.Detail = "managed copy is stale"
	default:
		placement.Detail = "conflicts with a repository-owned hook"
	}
	return placement, nil
}

func pathInside(root, path string) bool {
	relative, err := filepath.Rel(root, filepath.Clean(path))
	return err == nil && relative != "." && filepath.IsLocal(relative)
}

func InspectRepositoryHooks(paths Paths, machine Machine, runner Runner) Resource {
	resource := Resource{ID: repositoryHooksID, Name: repositoryHooksName, Authoritative: true}
	payloads, err := repositoryHookPayloads(paths, machine.RepositoryHooks)
	if err != nil {
		resource.State = Unavailable
		resource.Summary = "Declared hook sources are unavailable"
		resource.Checks = []Check{no("Repository hook sources readable", err.Error())}
		return resource
	}
	targets, err := repositoryHookTargets(paths, runner, true)
	if err != nil {
		resource.State = Unavailable
		resource.Summary = "Repository hook targets are unavailable"
		resource.Checks = []Check{no("Repository hook targets readable", err.Error())}
		return resource
	}
	placements, err := repositoryHookPlacements(paths, payloads, targets)
	if err != nil {
		resource.State = Unavailable
		resource.Summary = "Repository hook state is unreadable"
		resource.Checks = []Check{no("Repository hooks readable", err.Error())}
		return resource
	}
	var drifted, conflicts []string
	for _, placement := range placements {
		if placement.Current {
			continue
		}
		label := placement.Target.Name + ": " + placement.Hook.Name
		resource.Details = append(resource.Details, label+" — "+placement.Detail)
		if placement.Managed {
			drifted = append(drifted, label)
		} else {
			conflicts = append(conflicts, label)
		}
	}
	if len(conflicts) > 0 {
		resource.State = Drift
		resource.Summary = FormatCount(len(conflicts), "hook conflicts with its repository", "hooks conflict with their repositories")
		resource.Checks = []Check{no("Repository hooks safe to manage", strings.Join(conflicts, ", "))}
		return resource
	}
	if len(drifted) > 0 {
		resource.State = Drift
		resource.Summary = FormatCount(len(drifted), "hook copy needs refresh", "hook copies need refresh")
		resource.Checks = []Check{no("Repository hooks current", strings.Join(drifted, ", "))}
		resource.Actions = []Action{Apply}
		resource.ActionLabels = map[Action]string{Apply: "Refresh repository hooks"}
		return resource
	}
	resource.State = Current
	resource.Summary = FormatCount(len(placements), "hook copy current", "hook copies current")
	resource.Checks = []Check{yes("Repository hooks current")}
	return resource
}

func (e Applier) reconcileRepositoryHooks(action Action) error {
	if action != Apply || len(e.Machine.RepositoryHooks) == 0 {
		return nil
	}
	changed, err := e.applyRepositoryHookTargets(true)
	if err != nil {
		return err
	}
	if changed == 0 {
		e.Log.OK("repository hooks already current")
	} else {
		e.Log.OK(FormatCount(changed, "hook copy refreshed", "hook copies refreshed"))
	}
	return nil
}

func (e Applier) applyRepositoryHookTargets(includeRepositories bool) (int, error) {
	payloads, err := repositoryHookPayloads(e.Paths, e.Machine.RepositoryHooks)
	if err != nil {
		return 0, err
	}
	targets, err := repositoryHookTargets(e.Paths, e.Runner, includeRepositories)
	if err != nil {
		return 0, err
	}
	placements, err := repositoryHookPlacements(e.Paths, payloads, targets)
	if err != nil {
		return 0, err
	}
	for _, placement := range placements {
		if !placement.Current && !placement.Managed {
			return 0, fmt.Errorf("%s %s %s", placement.Target.Name, placement.Hook.Name, placement.Detail)
		}
	}
	changed := 0
	for _, target := range targets {
		manifest, err := readRepositoryHookManifest(target.Dir)
		if err != nil {
			return changed, err
		}
		manifestChanged := false
		var pending []repositoryHookPayload
		for _, hook := range payloads {
			var placement repositoryHookPlacement
			for _, candidate := range placements {
				if candidate.Target.Dir == target.Dir && candidate.Hook.Name == hook.Name {
					placement = candidate
					break
				}
			}
			if !placement.Current {
				pending = append(pending, hook)
			}
			if manifest.Hooks[hook.Name] != hook.Digest {
				manifest.Hooks[hook.Name] = hook.Digest
				manifestChanged = true
			}
		}
		// Claim ownership before installing, and hold the pair together. An
		// interrupted apply that has written the manifest leaves a claim on a
		// hook that is not there,
		// which the next apply completes and prune ignores. The other order
		// leaves an executable hook no record accounts for, which prune, and
		// every later refresh, cannot see.
		if manifestChanged || len(pending) > 0 {
			release := holdInterrupt()
			defer release()
		}
		if manifestChanged {
			data, err := json.MarshalIndent(manifest, "", "  ")
			if err != nil {
				return changed, err
			}
			if err := atomicWrite(filepath.Join(target.Dir, repositoryHookManifestName), append(data, '\n'), 0o644); err != nil {
				return changed, fmt.Errorf("write hook ownership for %s: %w", target.Name, err)
			}
		}
		for _, hook := range pending {
			if err := atomicWrite(filepath.Join(target.Dir, hook.Name), hook.Body, hook.Mode); err != nil {
				return changed, fmt.Errorf("write %s hook %s: %w", target.Name, hook.Name, err)
			}
			changed++
		}
	}
	return changed, nil
}
