package config

import (
	"os"
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
