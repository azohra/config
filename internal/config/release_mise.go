package config

import (
	"context"
	"errors"
	"strconv"
	"strings"
)

// releaseMiseVersion pins Config's private release transport, independently of machine tools.
const releaseMiseVersion = "2026.10.21"

func miseVersion(output string) (string, bool) {
	for _, field := range strings.Fields(output) {
		candidate := strings.TrimPrefix(field, "v")
		numbers := strings.Split(candidate, ".")
		if len(numbers) != 3 {
			continue
		}
		valid := true
		for _, number := range numbers {
			if _, err := strconv.Atoi(number); err != nil {
				valid = false
				break
			}
		}
		if valid {
			return candidate, true
		}
	}
	return "", false
}

func currentMiseVersionContext(ctx context.Context, runner Runner) (string, error) {
	if !runner.Exists("mise") {
		return "", errors.New("mise is unavailable")
	}
	result := runContext(ctx, runner, "mise", "--version")
	version, parsed := miseVersion(result.Stdout)
	if result.Err != nil || !parsed {
		return "", errors.New("mise version is unreadable")
	}
	return version, nil
}

func supportsReleaseMise(version string) bool {
	return version == releaseMiseVersion
}
