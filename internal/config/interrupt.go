package config

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// interruptGuard is held for reading by every section that has to reach its
// own end, and for writing by the interrupt handler. A signal received inside
// a critical section waits for it rather than landing in the middle of it.
//
// Exactly one section takes it: a single file's write. Go's read lock is not
// re-entrant once a writer is waiting, so a hold wrapped around a loop of
// writes would deadlock the write inside it. A cut between two files is what
// the ownership ordering, the pending markers, and the sweeps are for.
var interruptGuard sync.RWMutex

// holdInterrupt marks the section an interrupt must not cut in half: the
// staging, rename, and sync of one file.
func holdInterrupt() func() {
	interruptGuard.RLock()
	return interruptGuard.RUnlock
}

// interruptGrace bounds how long an interrupt waits for a critical section.
// A section that has not finished by then is not going to.
const interruptGrace = 10 * time.Second

// OnInterrupt makes SIGINT and SIGTERM end the process between critical
// sections rather than inside one. Config writes real files on a real Mac;
// the terminal interface cancels operations with the same key, so leaving the
// default disposition meant a signal could land between a hook and the record
// that claims it, or between a staged file and its rename.
//
// The returned function restores the default disposition.
func OnInterrupt(out io.Writer) func() {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		select {
		case received := <-signals:
			finished := make(chan struct{})
			go func() {
				interruptGuard.Lock()
				close(finished)
			}()
			select {
			case <-finished:
			case <-time.After(interruptGrace):
				fmt.Fprintln(out, "\ninterrupted while writing; leaving the operation incomplete")
			}
			signal.Stop(signals)
			// Exit the way the signal would have, so a shell and a parent
			// Config see the interruption rather than an ordinary failure.
			if process, err := os.FindProcess(os.Getpid()); err == nil {
				_ = process.Signal(received)
			}
			os.Exit(130)
		case <-done:
		}
	}()
	return func() {
		signal.Stop(signals)
		close(done)
	}
}

// LockCheckout takes the advisory lock that makes Config one writer at a time
// for one managed checkout. Config's records are last-writer-wins files, and
// two applies converging the same Mac would interleave their writes.
func LockCheckout(paths Paths) (func(), error) {
	if err := os.MkdirAll(paths.StateDir, 0o700); err != nil {
		return nil, fmt.Errorf("create Config state directory: %w", err)
	}
	path := filepath.Join(paths.StateDir, "lock")
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open Config lock: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, fmt.Errorf("another Config is already working on %s", paths.Root)
	}
	return func() {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		file.Close()
	}, nil
}

// sweepStaging removes the staging entries an interrupted run leaves behind.
// Each one is created by Config, named by Config, and meaningless once the run
// that made it is gone, so the next run is the thing that can clean it up.
func sweepStaging(dir, prefix string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if len(name) > len(prefix) && name[:len(prefix)] == prefix {
			_ = os.RemoveAll(filepath.Join(dir, name))
		}
	}
}
