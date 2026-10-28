package config

import (
	"context"
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

type releaseMiseInstaller struct {
	Destination string
	URL         string
	SHA256      string
	Size        int64
	Client      *http.Client
}

func releaseMiseInstallerAt(destination string) releaseMiseInstaller {
	asset, checksum := "", ""
	var size int64
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "darwin/arm64":
		asset = "mise-v" + releaseMiseVersion + "-macos-arm64"
		checksum = "b20d30788a819fade398a1cb92aee56c383e3c34324162ec39fb8567dee79ba6"
		size = 138821904
	case "darwin/amd64":
		asset = "mise-v" + releaseMiseVersion + "-macos-x64"
		checksum = "5dde6e3fcda4d8a68b95cc2180dacd58e7b940f86e4579991e13540a5d009055"
		size = 169964688
	}
	return releaseMiseInstaller{
		Destination: destination,
		URL:         "https://github.com/jdx/mise/releases/download/v" + releaseMiseVersion + "/" + asset,
		SHA256:      checksum,
		Size:        size,
		Client:      &http.Client{Timeout: 5 * time.Minute},
	}
}

func (i releaseMiseInstaller) Install() error {
	if i.URL == "" || i.SHA256 == "" || i.Size <= 0 {
		return fmt.Errorf("mise %s has no release for %s/%s", releaseMiseVersion, runtime.GOOS, runtime.GOARCH)
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
		return fmt.Errorf("download mise %s: %w", releaseMiseVersion, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download mise %s: %s", releaseMiseVersion, response.Status)
	}
	if response.ContentLength >= 0 && response.ContentLength != i.Size {
		return fmt.Errorf("download mise %s: release size is %d bytes, want %d", releaseMiseVersion, response.ContentLength, i.Size)
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
		return fmt.Errorf("download mise %s: release size is %d bytes, want %d", releaseMiseVersion, written, i.Size)
	}
	if actual := fmt.Sprintf("%x", hash.Sum(nil)); actual != i.SHA256 {
		return fmt.Errorf("verify mise %s: checksum is %s, want %s", releaseMiseVersion, actual, i.SHA256)
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

func ensureReleaseMise(runner Runner, install func() error) error {
	return ensureReleaseMiseContext(context.Background(), runner, install)
}

func ensureReleaseMiseContext(ctx context.Context, runner Runner, install func() error) error {
	version, err := currentMiseVersionContext(ctx, runner)
	if err == nil && supportsReleaseMise(version) {
		return nil
	}
	if install == nil {
		if err != nil {
			return err
		}
		return fmt.Errorf("mise %s is unsupported; install mise %s", version, releaseMiseVersion)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := install(); err != nil {
		return err
	}
	version, err = currentMiseVersionContext(ctx, runner)
	if err != nil {
		return err
	}
	if !supportsReleaseMise(version) {
		return fmt.Errorf("mise %s is unsupported; install mise %s", version, releaseMiseVersion)
	}
	return nil
}
