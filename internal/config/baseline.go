package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const baselineSchema = 1

type Baseline struct {
	Schema   int             `json:"schema"`
	Resource string          `json:"resource"`
	Content  json.RawMessage `json:"content"`
}

type Baselines struct {
	Dir string
}

func (b Baselines) Load(resource string) (Baseline, bool, error) {
	data, err := os.ReadFile(filepath.Join(b.Dir, resource+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return Baseline{}, false, nil
	}
	if err != nil {
		return Baseline{}, false, err
	}
	// A baseline is a cache, not a record. An unreadable one means the same
	// thing as an absent one — no last agreement — so a resource whose two
	// sides differ asks for a direction instead of guessing.
	var baseline Baseline
	if err := json.Unmarshal(data, &baseline); err != nil {
		return Baseline{}, false, nil
	}
	if baseline.Schema != baselineSchema || baseline.Resource != resource || !json.Valid(baseline.Content) {
		return Baseline{}, false, nil
	}
	return baseline, true, nil
}

func (b Baselines) Save(resource string, content json.RawMessage) error {
	if !json.Valid(content) {
		return fmt.Errorf("invalid %s baseline", resource)
	}
	if current, ok, err := b.Load(resource); err != nil {
		return err
	} else if ok && bytes.Equal(current.Content, content) {
		return nil
	}
	if err := os.MkdirAll(b.Dir, 0o700); err != nil {
		return err
	}
	baseline := Baseline{Schema: baselineSchema, Resource: resource, Content: content}
	data, err := json.Marshal(baseline)
	if err != nil {
		return err
	}
	return AtomicWrite(filepath.Join(b.Dir, resource+".json"), append(data, '\n'), 0o600)
}

func classify(saved, live json.RawMessage, baseline Baseline, hasBaseline bool) State {
	if len(saved) == 0 || len(live) == 0 {
		return Unavailable
	}
	if bytes.Equal(saved, live) {
		return Current
	}
	if !hasBaseline {
		return Unknown
	}
	savedAtBase := bytes.Equal(saved, baseline.Content)
	liveAtBase := bytes.Equal(live, baseline.Content)
	switch {
	case savedAtBase && !liveAtBase:
		return LiveChanged
	case liveAtBase && !savedAtBase:
		return SavedChanged
	default:
		return Conflict
	}
}
