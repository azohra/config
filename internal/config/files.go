package config

import (
	"os"
	"path/filepath"
	"strings"
)

func samePath(left, right string) bool {
	left, leftErr := filepath.Abs(left)
	right, rightErr := filepath.Abs(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	if resolved, err := filepath.EvalSymlinks(left); err == nil {
		left = resolved
	}
	if resolved, err := filepath.EvalSymlinks(right); err == nil {
		right = resolved
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

// stagingInfix separates a target's name from the random suffix CreateTemp
// appends, so AtomicWrite and the sweep that collects its residue spell the
// same name once.
const stagingInfix = ".tmp."

// strandedWrite reports whether a name is the residue of an interrupted
// AtomicWrite — exactly "<file>.tmp.<random digits>", and nothing looser. A
// sweep that matched every name merely containing ".tmp." would delete a file
// somebody else put in the repository, moments before Config commits it.
func strandedWrite(name string) bool {
	index := strings.LastIndex(name, stagingInfix)
	if index <= 0 {
		return false
	}
	suffix := name[index+len(stagingInfix):]
	if suffix == "" {
		return false
	}
	for _, digit := range suffix {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}

// AtomicWrite publishes complete bytes durably and defers interruption until
// the replacement and its directory entry are both synced.
func AtomicWrite(path string, data []byte, mode os.FileMode) error {
	defer holdInterrupt()()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, filepath.Base(path)+stagingInfix+"*")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if err := temp.Chmod(mode); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	// Rename alone is atomic against a concurrent reader but not durable across
	// a crash: without these syncs the directory entry can survive the bytes.
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	return syncDirectory(dir)
}

func syncDirectory(dir string) error {
	handle, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = handle.Sync()
	if closeErr := handle.Close(); err == nil {
		err = closeErr
	}
	return err
}
