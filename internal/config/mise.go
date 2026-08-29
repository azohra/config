package config

import (
	"strconv"
	"strings"
)

const minimumMiseVersion = "2026.8.14"

// misePath returns Config's canonical execution substrate.
func misePath(paths Paths) string {
	return paths.InHome(".local", "bin", "mise")
}

func miseVersion(output string) (string, bool) {
	for _, field := range strings.Fields(output) {
		candidate := strings.TrimPrefix(field, "v")
		parts := strings.SplitN(candidate, "-", 2)
		numbers := strings.Split(parts[0], ".")
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
			return parts[0], true
		}
	}
	return "", false
}

func miseVersionAtLeast(output, minimum string) bool {
	current, ok := miseVersion(output)
	if !ok {
		return false
	}
	minimum, ok = miseVersion(minimum)
	if !ok {
		return false
	}
	minimumParts := strings.Split(minimum, ".")
	for index, part := range strings.Split(current, ".") {
		got, _ := strconv.Atoi(part)
		want, _ := strconv.Atoi(minimumParts[index])
		if got != want {
			return got > want
		}
	}
	return true
}
