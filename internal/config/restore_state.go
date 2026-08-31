package config

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const (
	restoreSchema        = 1
	restoreStateFileStem = "bootstrap-restore-"
	restoreCheckoutKey   = "azohra-config.restore-id"
	restorePendingState  = "pending"
	restoreCompleteState = "complete"
	restorePlanVersion   = 2
)

type restoreRecord struct {
	Schema     int      `json:"schema"`
	Repository string   `json:"repository"`
	Checkout   string   `json:"checkout"`
	Commit     string   `json:"commit"`
	Plan       string   `json:"plan"`
	Status     string   `json:"status"`
	Completed  []string `json:"completed,omitempty"`
}

type restoreProgress struct {
	paths  Paths
	record restoreRecord
}

func beginRestore(paths, checkout Paths, machine Machine) (restoreProgress, error) {
	source := machine.Repository.URL
	repository, err := repositoryIdentity(source)
	if err != nil {
		return restoreProgress{}, err
	}
	commit, err := cleanCheckoutCommit(checkout)
	if err != nil {
		return restoreProgress{}, err
	}
	plan, err := restorePlanIdentity(checkout, machine)
	if err != nil {
		return restoreProgress{}, err
	}
	identifier, err := newRestoreID()
	if err != nil {
		return restoreProgress{}, fmt.Errorf("create restore identity: %w", err)
	}
	configured := run(newGitRunner(checkout.Root), "git", "config", "--local", restoreCheckoutKey, identifier)
	if configured.Err != nil {
		return restoreProgress{}, fmt.Errorf("record checkout identity: %w", configured.Failure())
	}
	progress := restoreProgress{
		paths: paths,
		record: restoreRecord{
			Schema:     restoreSchema,
			Repository: repository,
			Checkout:   identifier,
			Commit:     commit,
			Plan:       plan,
			Status:     restorePendingState,
		},
	}
	if err := progress.save(); err != nil {
		return restoreProgress{}, err
	}
	return progress, nil
}

func pendingRestore(paths Paths, machine Machine) (restoreProgress, bool, error) {
	identifier, found, err := checkoutRestoreID(paths)
	if err != nil || !found {
		return restoreProgress{}, false, err
	}
	data, err := os.ReadFile(restoreStatePath(paths, identifier))
	if errors.Is(err, os.ErrNotExist) {
		return restoreProgress{}, false, nil
	}
	if err != nil {
		return restoreProgress{}, false, fmt.Errorf("read bootstrap restore state: %w", err)
	}
	var record restoreRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return restoreProgress{}, false, fmt.Errorf("read bootstrap restore state: %w", err)
	}
	if err := record.validate(); err != nil {
		return restoreProgress{}, false, err
	}
	repository, err := repositoryIdentity(machine.Repository.URL)
	if err != nil {
		return restoreProgress{}, false, err
	}
	if record.Checkout != identifier || record.Repository != repository {
		return restoreProgress{}, false, nil
	}
	progress := restoreProgress{paths: paths, record: record}
	if record.Status != restorePendingState {
		return progress, false, nil
	}
	if err := progress.validatePending(machine); err != nil {
		return restoreProgress{}, false, err
	}
	return progress, true, nil
}

func (p restoreProgress) validatePending(machine Machine) error {
	if p.record.Status != restorePendingState {
		return errors.New("bootstrap restore is not pending")
	}
	commit, err := cleanCheckoutCommit(p.paths)
	if err != nil {
		return err
	}
	if commit != p.record.Commit {
		return fmt.Errorf("managed checkout changed from %s to %s while bootstrap restore is pending", shortCommit(p.record.Commit), shortCommit(commit))
	}
	plan, err := restorePlanIdentity(p.paths, machine)
	if err != nil {
		return err
	}
	if plan != p.record.Plan {
		return errors.New("machine declaration or Config restore plan changed while bootstrap restore is pending")
	}
	return nil
}

func cleanCheckoutCommit(paths Paths) (string, error) {
	runner := newGitRunner(paths.Root)
	status := run(runner, "git", "status", "--porcelain=v1", "--untracked-files=all")
	if status.Err != nil {
		return "", fmt.Errorf("inspect bootstrap restore checkout: %w", status.Failure())
	}
	if status.Output() != "" {
		return "", errors.New("managed checkout has local changes while bootstrap restore is pending")
	}
	head := run(runner, "git", "rev-parse", "--verify", "HEAD")
	if head.Err != nil || !validCommit(head.Output()) {
		return "", errors.New("managed checkout commit is unreadable")
	}
	return head.Output(), nil
}

// restorePlanVersion is bumped whenever the meaning or ordering of a restore
// step changes. The declaration bytes bind the plan to this checkout's exact
// policy; the commit binds every tracked snapshot the steps will consume.
func restorePlanIdentity(paths Paths, machine Machine) (string, error) {
	declaration, err := os.ReadFile(paths.InRoot("config.toml"))
	if err != nil {
		return "", fmt.Errorf("read bootstrap restore declaration: %w", err)
	}
	identity := struct {
		Version     int      `json:"version"`
		Declaration []byte   `json:"declaration"`
		Steps       []string `json:"steps"`
	}{restorePlanVersion, declaration, restoreStepIDs(machine)}
	data, err := json.Marshal(identity)
	if err != nil {
		return "", fmt.Errorf("encode bootstrap restore plan: %w", err)
	}
	digest := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func restoreStepIDs(machine Machine) []string {
	steps := []string{restoreSetupStep}
	if machine.FinderFavorites {
		steps = append(steps, "resource/"+finderFavoritesID)
	}
	for _, preference := range machine.Preferences {
		steps = append(steps, "preference/"+preference.ID)
	}
	if machine.ChromePWAs {
		steps = append(steps, "resource/"+chromePWAsID)
	}
	if machine.Dock {
		steps = append(steps, "resource/"+dockID)
	}
	return steps
}

func shortCommit(commit string) string {
	if len(commit) >= 7 {
		return commit[:7]
	}
	return commit
}

func checkoutRestoreID(paths Paths) (string, bool, error) {
	return checkoutRestoreIDWithRunner(newGitRunner(paths.Root))
}

func checkoutRestoreIDWithRunner(runner Runner) (string, bool, error) {
	result := run(runner, "git", "config", "--local", "--get", restoreCheckoutKey)
	if result.Err == nil {
		identifier := result.Output()
		if identifier == "" {
			return "", false, nil
		}
		if !validRestoreID(identifier) {
			return "", false, errors.New("checkout restore identity is invalid")
		}
		return identifier, true, nil
	}
	if result.ExitCode() == 1 && result.Output() == "" {
		return "", false, nil
	}
	return "", false, fmt.Errorf("read checkout restore identity: %w", result.Failure())
}

func newRestoreID() (string, error) {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(data[:]), nil
}

func validRestoreID(identifier string) bool {
	if len(identifier) != 32 {
		return false
	}
	_, err := hex.DecodeString(identifier)
	return err == nil
}

func validRestorePlan(plan string) bool {
	digest, found := strings.CutPrefix(plan, "sha256:")
	if !found || len(digest) != 64 {
		return false
	}
	_, err := hex.DecodeString(digest)
	return err == nil
}

func validCommit(commit string) bool {
	if len(commit) != 40 && len(commit) != 64 {
		return false
	}
	_, err := hex.DecodeString(commit)
	return err == nil
}

func restoreStatePath(paths Paths, identifier string) string {
	return filepath.Join(filepath.Dir(paths.Root), "restore", restoreStateFileStem+identifier+".json")
}

func (r restoreRecord) validate() error {
	if r.Schema != restoreSchema || r.Repository == "" || !validRestoreID(r.Checkout) || !validCommit(r.Commit) || !validRestorePlan(r.Plan) {
		return errors.New("bootstrap restore state is invalid")
	}
	if r.Status != restorePendingState && r.Status != restoreCompleteState {
		return errors.New("bootstrap restore state has an invalid status")
	}
	seen := make(map[string]bool, len(r.Completed))
	for _, step := range r.Completed {
		if strings.TrimSpace(step) == "" || seen[step] {
			return errors.New("bootstrap restore state has invalid completed steps")
		}
		seen[step] = true
	}
	return nil
}

func (p restoreProgress) done(step string) bool {
	return slices.Contains(p.record.Completed, step)
}

func (p *restoreProgress) markDone(step string, machine Machine) error {
	if p.done(step) {
		return nil
	}
	if err := p.validatePending(machine); err != nil {
		return err
	}
	p.record.Completed = append(p.record.Completed, step)
	if err := p.save(); err != nil {
		p.record.Completed = p.record.Completed[:len(p.record.Completed)-1]
		return err
	}
	return nil
}

func (p *restoreProgress) finish(machine Machine) error {
	if err := p.validatePending(machine); err != nil {
		return err
	}
	p.record.Status = restoreCompleteState
	if err := p.save(); err != nil {
		p.record.Status = restorePendingState
		return err
	}
	return nil
}

func (p restoreProgress) save() error {
	if err := p.record.validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(restoreStatePath(p.paths, p.record.Checkout)), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(p.record)
	if err != nil {
		return err
	}
	return atomicWrite(restoreStatePath(p.paths, p.record.Checkout), append(data, '\n'), 0o600)
}
