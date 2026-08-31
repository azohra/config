package config

import (
	"os"
	"path/filepath"
)

// A marker records a fact that the write about to happen will destroy, so a
// run killed between the two halves of an operation can be finished by the
// next one. Markers describe this Mac mid-operation rather than its
// configuration, so they live beside the baselines, outside the repository.
const dockRestartMarker = "dock-restart"

func relaunchMarker(bundle string) string { return "relaunch-" + bundle }

func markerPath(paths Paths, name string) string {
	return filepath.Join(paths.StateDir, "pending", name)
}

func setMarker(paths Paths, name string) error {
	return atomicWrite(markerPath(paths, name), []byte(name+"\n"), 0o600)
}

func markerSet(paths Paths, name string) bool {
	info, err := os.Stat(markerPath(paths, name))
	return err == nil && info.Mode().IsRegular()
}

func clearMarker(paths Paths, name string) {
	_ = os.Remove(markerPath(paths, name))
}
