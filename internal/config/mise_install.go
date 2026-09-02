package config

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

type miseInstaller struct {
	Destination string
	URL         string
	SHA256      string
	Size        int64
	Client      *http.Client
}

func testedMiseInstaller(paths Paths) miseInstaller {
	return testedMiseInstallerAt(misePath(paths))
}

func testedMiseInstallerAt(destination string) miseInstaller {
	asset, checksum := "", ""
	var size int64
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "darwin/arm64":
		asset = "mise-v" + testedMiseVersion + "-macos-arm64"
		checksum = "3cfbe3295dba1a7e43bd02653517a8cc21135ba91f0635b45c98f1ebecc5513f"
		size = 89793376
	case "darwin/amd64":
		asset = "mise-v" + testedMiseVersion + "-macos-x64"
		checksum = "0718a2aa14a96545a287f77a172d700247bb2d33016e5cf29fce1a05e45ac47a"
		size = 107279616
	}
	return miseInstaller{
		Destination: destination,
		URL:         "https://github.com/jdx/mise/releases/download/v" + testedMiseVersion + "/" + asset,
		SHA256:      checksum,
		Size:        size,
		Client:      &http.Client{Timeout: 5 * time.Minute},
	}
}

func (i miseInstaller) Install() error {
	if i.URL == "" || i.SHA256 == "" || i.Size <= 0 {
		return fmt.Errorf("mise %s has no release for %s/%s", testedMiseVersion, runtime.GOOS, runtime.GOARCH)
	}
	if !filepath.IsAbs(i.Destination) {
		return errors.New("mise destination is not absolute")
	}
	client := i.Client
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Get(i.URL)
	if err != nil {
		return fmt.Errorf("download mise %s: %w", testedMiseVersion, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download mise %s: %s", testedMiseVersion, response.Status)
	}
	if response.ContentLength >= 0 && response.ContentLength != i.Size {
		return fmt.Errorf("download mise %s: release size is %d bytes, want %d", testedMiseVersion, response.ContentLength, i.Size)
	}

	parent := filepath.Dir(i.Destination)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("create mise command directory: %w", err)
	}
	staged, err := os.CreateTemp(parent, ".mise-install-*")
	if err != nil {
		return fmt.Errorf("stage mise command: %w", err)
	}
	stagedName := staged.Name()
	defer os.Remove(stagedName)

	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(staged, hash), io.LimitReader(response.Body, i.Size+1))
	chmodErr := staged.Chmod(0o755)
	syncErr := staged.Sync()
	closeErr := staged.Close()
	for _, failure := range []struct {
		name string
		err  error
	}{
		{"write mise command", copyErr},
		{"make mise command executable", chmodErr},
		{"sync mise command", syncErr},
		{"close mise command", closeErr},
	} {
		if failure.err != nil {
			return fmt.Errorf("%s: %w", failure.name, failure.err)
		}
	}
	if written != i.Size {
		return fmt.Errorf("download mise %s: release size is %d bytes, want %d", testedMiseVersion, written, i.Size)
	}
	if actual := fmt.Sprintf("%x", hash.Sum(nil)); actual != i.SHA256 {
		return fmt.Errorf("verify mise %s: checksum is %s, want %s", testedMiseVersion, actual, i.SHA256)
	}
	if err := os.Rename(stagedName, i.Destination); err != nil {
		return fmt.Errorf("install mise command: %w", err)
	}
	directory, err := os.Open(parent)
	if err == nil {
		err = directory.Sync()
		if closeDirectoryErr := directory.Close(); err == nil {
			err = closeDirectoryErr
		}
	}
	if err != nil {
		return fmt.Errorf("sync mise command directory: %w", err)
	}
	return nil
}

func ensureTestedMise(runner Runner, install func() error) error {
	version, err := currentMiseVersion(runner)
	if err == nil && supportsTestedMise(version) {
		return nil
	}
	if install == nil {
		if err != nil {
			return err
		}
		return fmt.Errorf("mise %s is unsupported; install mise %s", version, testedMiseVersion)
	}
	if err := install(); err != nil {
		return err
	}
	return requireTestedMise(runner)
}
