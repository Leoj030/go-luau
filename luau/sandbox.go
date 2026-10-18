package luau

/*
#include "bridge.h"
*/
import "C"

import (
	"context"
	"sync"
)

// MemoryUsed returns the number of bytes the state currently has allocated,
// or 0 if it has been closed.
func (s *State) MemoryUsed() int {
	if !s.open() {
		return 0
	}
	return int(C.bridge_memused(s.l))
}

// Sandbox locks the state down for running scripts: library tables and the
// globals table become read-only, so scripts can no longer modify the
// standard libraries or assign new globals.
//
// Call it after opening libraries and setting up everything your scripts
// need (SetGlobal, Register, ...). Once sandboxed, those fail too, because
// the globals table is read-only for you as well.
func (s *State) Sandbox() {
	if s.open() {
		C.bridge_sandbox(s.l)
	}
}

// Interrupt asks the running script to stop. It is safe to call from any
// goroutine while the State is open. The script fails with an error
// wrapping ErrInterrupted at its next safepoint (a loop iteration or a
// call), and keeps failing until ClearInterrupt is called.
//
// On a closed State it does nothing, but it must never run at the same
// time as Close.
func (s *State) Interrupt() {
	if s.open() {
		C.bridge_interrupt(s.l)
	}
}

// ClearInterrupt clears a pending interrupt so the state can run again.
func (s *State) ClearInterrupt() {
	if s.open() {
		C.bridge_clearinterrupt(s.l)
	}
}

// Interrupts the state when ctx is done. Call the returned
// function when the script has finished: it stops the watcher, waits for it
// to exit, and clears any interrupt so the state can be reused. Call it
// before Close.
//
//	stop := L.WatchContext(ctx)
//	err := L.DoString(src)
//	stop()
func (s *State) WatchContext(ctx context.Context) (stop func()) {
	s.ClearInterrupt()
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		select {
		case <-ctx.Done():
			s.Interrupt()
		case <-done:
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			close(done)
			<-finished
			s.ClearInterrupt()
		})
	}
}
