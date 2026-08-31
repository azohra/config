package config

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestCheckoutLockAdmitsOneWriter(t *testing.T) {
	paths := testPaths(t)
	release, err := LockCheckout(paths)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LockCheckout(paths); err == nil {
		t.Fatal("a second writer took the checkout lock")
	}
	release()
	second, err := LockCheckout(paths)
	if err != nil {
		t.Fatalf("the lock was not released: %v", err)
	}
	second()

	// Two checkouts are two locks.
	other := testPaths(t)
	first, err := LockCheckout(paths)
	if err != nil {
		t.Fatal(err)
	}
	defer first()
	elsewhere, err := LockCheckout(other)
	if err != nil {
		t.Fatalf("an unrelated checkout was refused: %v", err)
	}
	elsewhere()
}

func TestHoldInterruptBlocksTheHandlerUntilTheWriteFinishes(t *testing.T) {
	// The handler takes the write side, so it cannot proceed while any
	// critical section holds the read side.
	release := holdInterrupt()
	acquired := make(chan struct{})
	var once sync.Once
	go func() {
		interruptGuard.Lock()
		once.Do(func() { close(acquired) })
		interruptGuard.Unlock()
	}()
	select {
	case <-acquired:
		t.Fatal("an interrupt cut into a critical section")
	case <-time.After(50 * time.Millisecond):
	}
	release()
	select {
	case <-acquired:
	case <-time.After(2 * time.Second):
		t.Fatal("the interrupt never ran after the section finished")
	}
}

func TestSweepStagingRemovesOnlyItsOwnPrefix(t *testing.T) {
	dir := t.TempDir()
	abandoned := filepath.Join(dir, ".config-pwas.123")
	if err := os.MkdirAll(filepath.Join(abandoned, "Half.app"), 0o755); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(dir, "Gmail.app")
	if err := os.MkdirAll(keep, 0o755); err != nil {
		t.Fatal(err)
	}
	sweepStaging(dir, ".config-pwas.")
	if _, err := os.Stat(abandoned); !os.IsNotExist(err) {
		t.Fatal("abandoned staging survived the sweep")
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("the sweep removed an unrelated entry: %v", err)
	}
}
