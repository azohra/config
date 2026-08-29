package config

import (
	"encoding/json"
	"fmt"
)

// The plan crosses to the child as one argv element, which needs no escaping:
// exec passes arguments to the process directly, never through a shell.
func EncodeSelections(selections []Selection) (string, error) {
	data, err := json.Marshal(selections)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func DecodeSelections(encoded string) ([]Selection, error) {
	var selections []Selection
	if err := json.Unmarshal([]byte(encoded), &selections); err != nil {
		return nil, err
	}
	for _, selection := range selections {
		if selection.ID == "" || (selection.Action != Apply && selection.Action != Capture) {
			return nil, fmt.Errorf("invalid selection")
		}
	}
	return selections, nil
}

// ValidateSelections rejects stale or forged plans before any live writes.
func ValidateSelections(report Report, selections []Selection) error {
	seen := make(map[string]bool)
	for _, selection := range selections {
		if seen[selection.ID] {
			return fmt.Errorf("%s appears more than once", selection.ID)
		}
		seen[selection.ID] = true
		resource, ok := report.Resource(selection.ID)
		if !ok {
			return fmt.Errorf("unknown resource %q", selection.ID)
		}
		if !resource.Allows(selection.Action) {
			return fmt.Errorf("%s no longer allows %s; review the plan again", resource.Name, selection.Action)
		}
	}
	return nil
}
