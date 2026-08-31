package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTrackedArtifactsAreDecodedStrictly(t *testing.T) {
	// The Chrome PWA snapshot had its own decode without the trailing-data
	// check, so a tracked artifact could carry a second document nobody read.
	paths := testPaths(t)
	valid := `{"schema":1,"apps":[]}`
	for name, body := range map[string]string{
		"trailing data": valid + "\n{\"schema\":1,\"apps\":[]}\n",
		"unknown field": `{"schema":1,"apps":[],"extra":true}`,
		"truncated":     `{"schema":1,"apps":[`,
	} {
		if err := atomicWrite(chromePWASnapshotPath(paths), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		bidir := testBidirectional(paths, dockRunner{})
		if _, _, _, err := bidir.chromePWASaved(); err == nil {
			t.Errorf("a saved PWA snapshot with %s was accepted", name)
		}
	}
	if err := os.WriteFile(chromePWASnapshotPath(paths), []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := testBidirectional(paths, dockRunner{}).chromePWASaved(); err != nil {
		t.Fatalf("a well-formed snapshot was refused: %v", err)
	}
}

func TestEveryTrackedArtifactDecoderIsTheStrictOne(t *testing.T) {
	// One decoder is only one authority if every artifact reads through it.
	// Trailing data is the check the weakest of the three used to omit.
	paths := testPaths(t)
	trailing := func(document string) []byte { return []byte(document + "\n" + document + "\n") }

	if err := atomicWrite(finderFavoritesSnapshotPath(paths),
		trailing(`{"schema":1,"favorites":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := testBidirectional(paths, dockRunner{}).finderFavoritesSaved(); err == nil {
		t.Error("a saved Finder Favorites snapshot with trailing data was accepted")
	}

	hooks := t.TempDir()
	manifest := `{"schema":1,"hooks":{}}`
	if err := os.WriteFile(filepath.Join(hooks, repositoryHookManifestName), trailing(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readRepositoryHookManifest(hooks); err == nil {
		t.Error("a hook ownership manifest with trailing data was accepted")
	}
}

func TestContentDigestIsOneSpelling(t *testing.T) {
	digest := contentDigest([]byte("#!/bin/sh\nexit 0\n"))
	if !strings.HasPrefix(digest, "sha256:") {
		t.Fatalf("digest = %q", digest)
	}
	if !validContentDigest(digest) {
		t.Fatalf("the digest Config writes does not validate: %q", digest)
	}
	for _, invalid := range []string{"", "sha256:", "deadbeef", digest + "0", strings.TrimPrefix(digest, "sha256:")} {
		if validContentDigest(invalid) {
			t.Errorf("%q validated as a content digest", invalid)
		}
	}
	if contentDigest([]byte("a")) == contentDigest([]byte("b")) {
		t.Fatal("different bytes produced the same digest")
	}
}
