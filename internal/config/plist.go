package config

import (
	"fmt"

	"howett.net/plist"
)

func decodePlist(data []byte) (map[string]any, error) {
	var values map[string]any
	if _, err := plist.Unmarshal(data, &values); err != nil {
		return nil, err
	}
	if values == nil {
		return nil, fmt.Errorf("plist root is not a dictionary")
	}
	return values, nil
}
