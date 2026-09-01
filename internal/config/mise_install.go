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

const maxMiseReleaseSize = 100 << 20

type miseInstaller struct {
	Destination string
	URL         string
	SHA256      string
	Client      *http.Client
}

func testedMiseInstaller(paths Paths) miseInstaller {
	return testedMiseInstallerAt(misePath(paths))
}

func testedMiseInstallerAt(destination string) miseInstaller {
	asset, checksum := "", ""
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "darwin/arm64":
		asset = "mise-v" + testedMiseVersion + "-macos-arm64"
		checksum = "50eeb4b907fb5fd4ad87a5fec0e55735bb16dfe00c725c9c3dc40852afd55b06"
	case "darwin/amd64":
		asset = "mise-v" + testedMiseVersion + "-macos-x64"
		checksum = "d4c68596addfd102717699243acfb795177dc025e7895bad581317c43fadb4ef"
	}
	return miseInstaller{
		Destination: destination,
		URL:         "https://github.com/jdx/mise/releases/download/v" + testedMiseVersion + "/" + asset,
		SHA256:      checksum,
		Client:      &http.Client{Timeout: 5 * time.Minute},
	}
}

func (i miseInstaller) Install() error {
	if i.URL == "" || i.SHA256 == "" {
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
	if response.ContentLength > maxMiseReleaseSize {
		return fmt.Errorf("download mise %s: release is too large", testedMiseVersion)
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
	written, copyErr := io.Copy(io.MultiWriter(staged, hash), io.LimitReader(response.Body, maxMiseReleaseSize+1))
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
	if written > maxMiseReleaseSize {
		return fmt.Errorf("download mise %s: release is too large", testedMiseVersion)
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
