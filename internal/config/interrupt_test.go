package config

import (
	"os"
	"path/filepath"
	"strings"
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

func TestAnInterruptWaitsForTheWritesInFlight(t *testing.T) {
	release := holdInterrupt()
	idle := make(chan struct{})
	go func() {
		awaitWrites()
		close(idle)
	}()
	select {
	case <-idle:
		t.Fatal("an interrupt cut into a write")
	case <-time.After(50 * time.Millisecond):
	}
	release()
	select {
	case <-idle:
	case <-time.After(2 * time.Second):
		t.Fatal("the interrupt never ran after the write finished")
	}
}

func TestANestedHoldDoesNotDeadlockTheWriteUnderIt(t *testing.T) {
	// A write inside a write is what a loop that holds and then calls
	// atomicWrite produces. Under a read lock this deadlocked: the waiting
	// interrupt blocks the inner hold, which the outer hold is waiting for.
	outer := holdInterrupt()
	nested := make(chan func(), 1)
	go func() {
		awaitWrites()
	}()
	time.Sleep(20 * time.Millisecond) // let the interrupt start waiting
	done := make(chan struct{})
	go func() {
		nested <- holdInterrupt()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a hold taken inside another hold blocked")
	}
	(<-nested)()
	outer()

	idle := make(chan struct{})
	go func() {
		awaitWrites()
		close(idle)
	}()
	select {
	case <-idle:
	case <-time.After(2 * time.Second):
		t.Fatal("the count did not return to zero")
	}
}

func TestAtomicWriteIsTheWriteAnInterruptWaitsFor(t *testing.T) {
	// The behaviour, not the call sites: a write in progress holds the
	// interrupt, and the count is balanced when it returns.
	path := filepath.Join(t.TempDir(), "artifact")
	if err := atomicWrite(path, []byte("state\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	idle := make(chan struct{})
	go func() {
		awaitWrites()
		close(idle)
	}()
	select {
	case <-idle:
	case <-time.After(2 * time.Second):
		t.Fatal("atomicWrite left a write in flight")
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

func TestOnlyOneSectionHoldsTheInterrupt(t *testing.T) {
	// Go's read lock is not re-entrant once a writer waits, so a hold wrapped
	// around a loop whose body writes would deadlock on the inner hold: the
	// interrupt would wait out its whole grace and report a write that was
	// never in flight. One caller, in the one place that writes a file.
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	callers := map[string]int{}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if count := strings.Count(string(body), "holdInterrupt()"); count > 0 {
			callers[name] = count
		}
	}
	// interrupt.go declares it; files.go is the one caller.
	delete(callers, "interrupt.go")
	if len(callers) != 1 || callers["files.go"] != 1 {
		t.Fatalf("holdInterrupt is taken in %v, want only files.go once", callers)
	}
}
