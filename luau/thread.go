package luau

/*
#include "bridge.h"
*/
import "C"

import (
	"errors"
	"fmt"
)

// ThreadStatus describes where a coroutine is in its life.
type ThreadStatus int

const (
	ThreadRunning   ThreadStatus = iota // executing right now
	ThreadSuspended                     // loaded but not started, or yielded
	ThreadNormal                        // has resumed another thread and is waiting for it
	ThreadFinished                      // ran to completion, or has nothing loaded
	ThreadFailed                        // stopped with an error and can't be resumed
)

func (st ThreadStatus) String() string {
	switch st {
	case ThreadRunning:
		return "running"
	case ThreadSuspended:
		return "suspended"
	case ThreadNormal:
		return "normal"
	case ThreadFinished:
		return "finished"
	case ThreadFailed:
		return "failed"
	}
	return "unknown"
}

// maxResumeDepth bounds nested resumes (a Go function that resumes a thread
// that calls the same Go function ...), which would otherwise exhaust the C stack.
const maxResumeDepth = 64

// Thread is a Luau coroutine. It embeds *State, so Load, Push, Get,
// SetGlobal and the rest operate on the thread's own stack. Load a chunk
// onto it and run it with Resume.
//
// A Thread stays alive until you call Close, even if nothing in Luau
// refers to it. Close threads before closing the State they came from.
type Thread struct {
	*State
	main    *State
	ref     Ref
	started bool
}

// Creates a coroutine in the same state. Nothing is left on the stack.
func (s *State) NewThread() (*Thread, error) {
	if !s.open() {
		return nil, ErrClosed
	}
	root := s.root()
	var ref C.int
	co := C.bridge_newthread(s.l, &ref)
	if co == nil {
		return nil, errors.New("luau: failed to create thread")
	}
	return &Thread{
		State: &State{l: co, parent: root},
		main:  root,
		ref:   Ref(ref),
	}, nil
}

// Close lets Luau collect the thread. The Thread must not be used afterwards.
func (t *Thread) Close() {
	if t.State.l == nil {
		return
	}
	if t.main.l != nil {
		C.bridge_unref(t.main.l, C.int(t.ref))
	}
	t.State.l = nil
}

// Gives the thread its own globals table: reads fall through
// to the state's globals, but assignments stay private to this thread. Call
// it before Load, because a chunk captures its environment when it is
// loaded. For real isolation, call State.Sandbox on the main state first;
// otherwise scripts can still modify the shared library tables.
func (t *Thread) SandboxThread() {
	if t.State.open() {
		C.bridge_sandboxthread(t.State.l)
	}
}

// Reports the thread's state.
func (t *Thread) Status() ThreadStatus {
	if t.State.l == nil || t.main.l == nil {
		return ThreadFinished
	}
	return ThreadStatus(C.bridge_costatus(t.main.l, t.State.l))
}

// The stack-level form of Resume. The caller prepares the
// thread's stack:
//
//   - first resume: the function, then nargs arguments (Load, then Push)
//   - later resumes: exactly nargs values, which become the results of the
//     call that yielded
//
// On success the yielded or returned values are left on the thread's stack,
// nresults of them, and done says whether the function finished (true) or
// yielded (false). Read them with Get, GetField and friends, then Pop them
// before the next resume. This is the form to use when results can be
// tables or other values Get can't convert.
//
// On a script error the stack is cleared and the error is returned, wrapping
// ErrMemory or ErrInterrupted where they apply. The thread is then
// ThreadFailed. If the thread is a module that yielded while it was loading
// (see YieldingRequirer), finishing or failing it also hands the result to the
// coroutine that is waiting for it in require.
func (t *Thread) ResumeN(nargs int) (nresults int, done bool, err error) {
	if cerr := t.checkResumable(nargs, false); cerr != nil {
		return 0, false, cerr
	}
	return t.run(nargs, false)
}

// ResumeError resumes a suspended thread by raising err at the point where
// it yielded, as if that call had failed. If a pcall around that point
// catches it, the thread carries on. Otherwise the thread fails and the
// error is returned. The thread must have started already and have nothing
// on its stack. It is how a runtime fails a require whose module failed
// (see YieldingRequirer).
func (t *Thread) ResumeError(err error) (nresults int, done bool, resumeErr error) {
	if err == nil {
		err = errors.New("error")
	}
	if cerr := t.checkResumable(0, true); cerr != nil {
		return 0, false, cerr
	}
	if perr := t.Push(err.Error()); perr != nil {
		return 0, false, perr
	}
	return t.run(1, true)
}

// checkResumable verifies that the thread can be resumed with nargs values
// on its stack. raise is true for ResumeError, which can only continue a
// thread that has already started.
func (t *Thread) checkResumable(nargs int, raise bool) error {
	if t.State.l == nil || t.main.l == nil {
		return ErrClosed
	}
	switch t.Status() {
	case ThreadSuspended:
	case ThreadFinished:
		return errors.New("luau: thread has finished, or has nothing loaded")
	case ThreadFailed:
		return errors.New("luau: thread failed and cannot be resumed")
	default:
		return errors.New("luau: thread is already running")
	}

	want := nargs
	if !t.started {
		if raise {
			return errors.New("luau: ResumeError needs a thread that has already started")
		}
		want++ // the function itself
		if t.TypeName(1) != "function" {
			return errors.New("luau: load a chunk onto the thread before the first resume")
		}
	}
	if nargs < 0 || t.Top() != want {
		return fmt.Errorf("luau: thread stack holds %d values, expected %d", t.Top(), want)
	}
	if t.main.depth >= maxResumeDepth {
		return errors.New("luau: too many nested resumes")
	}
	return nil
}

// run resumes the thread, normally or with an error, and reports the outcome.
func (t *Thread) run(nargs int, raise bool) (nresults int, done bool, err error) {
	t.started = true
	t.main.depth++
	var status C.int
	if raise {
		status = C.bridge_resumeerror(t.State.l, t.main.l)
	} else {
		status = C.bridge_resume(t.State.l, t.main.l, C.int(nargs))
	}
	t.main.depth--

	switch {
	case status == 0:
		n := t.Top()
		t.settleModule(nil)
		return n, true, nil
	case status == C.bridge_yieldcode():
		return t.Top(), false, nil
	case status == C.bridge_breakcode():
		C.bridge_settop(t.State.l, 0)
		return 0, false, errors.New("luau: thread stopped at a breakpoint (not supported)")
	}
	C.bridge_tracethread(t.State.l)
	failure := t.failWith(0, status)
	t.settleModule(failure)
	return 0, false, failure
}

// settleModule is called when a thread finishes or fails. If the thread is a
// module that yielded while loading, it hands the outcome to the coroutine
// waiting in require, through the Requirer's ModuleFinished.
func (t *Thread) settleModule(modErr error) {
	root := t.main
	if root.reqHandle == 0 {
		return // require isn't open, so nothing can be waiting
	}
	var ref C.int
	wl := C.bridge_takewaiter(root.l, t.State.l, &ref)
	if wl == nil {
		return // t is not a module that yielded while loading
	}
	waiter := &Thread{State: &State{l: wl, parent: root}, main: root, ref: Ref(ref), started: true}

	var failure error
	switch {
	case modErr != nil:
		failure = fmt.Errorf("error while running module: %w", modErr)
	case t.Top() != 1:
		failure = errors.New("module must return a single value")
	case C.bridge_xpush(t.State.l, wl) != 0:
		failure = ErrStackOverflow
	}

	fin, ok := root.reqHandle.Value().(YieldingRequirer)
	if !ok { // can't happen: a module only waits if its Requirer supports it
		waiter.Close()
		return
	}
	fin.ModuleFinished(waiter, failure)
}

// Runs the thread until it yields, returns or fails.
//
// The first call starts the chunk loaded with Load, and args are its
// arguments. Later calls pass args as the results of the call that yielded.
// It returns the yielded or returned values, and done = true when the
// function finished.
//
// Values are converted like Get, so a yielded table or function makes Resume
// return an error (the thread has still advanced). Use ResumeN to read those.
func (t *Thread) Resume(args ...any) (results []any, done bool, err error) {
	if t.State.l == nil || t.main.l == nil {
		return nil, false, ErrClosed
	}
	base := t.Top()
	if C.bridge_checkstack(t.State.l, C.int(len(args)+1)) == 0 {
		return nil, false, ErrStackOverflow
	}
	for _, a := range args {
		if err := t.Push(a); err != nil {
			C.bridge_settop(t.State.l, C.int(base))
			return nil, false, err
		}
	}

	n, done, err := t.ResumeN(len(args))
	if err != nil {
		return nil, false, err
	}
	results = make([]any, 0, n)
	for i := 1; i <= n; i++ {
		v, gerr := t.Get(i)
		if gerr != nil {
			C.bridge_settop(t.State.l, 0)
			return nil, done, gerr
		}
		results = append(results, v)
	}
	C.bridge_settop(t.State.l, 0)
	return results, done, nil
}

// Thread returns a handle to the coroutine this Go function was called
// from, so the runtime can resume it later. It is the building block for
// asynchronous functions. A function like fetch calls Thread, starts its
// work on another goroutine, and returns Yield(0). When the work is done,
// the goroutine hands the result to the goroutine that owns the State, which
// calls Resume on the handle with that result. The values passed to Resume
// become the results of the call that yielded.
//
// The rules:
//
//   - Thread can only be called from a Go function that is running inside a
//     coroutine, which means under Thread.Resume or ResumeN. From the main
//     thread (a plain DoString or Call) it returns an error.
//   - The handle pins the coroutine. Close it when you are done: until then
//     the coroutine can't be garbage-collected, even if nothing resumes it.
//   - A State belongs to one goroutine. The goroutine doing the work must not
//     touch the handle or the State, so it should send its result over a
//     channel to the owner.
//   - It is meant for coroutines your runtime resumes itself, such as those
//     made with NewThread. A coroutine that a script resumes with
//     coroutine.resume gets its yielded values in the script, and resuming
//     it from Go as well mixes two resumers.
func (s *State) Thread() (*Thread, error) {
	if !s.open() {
		return nil, ErrClosed
	}
	if !s.call {
		return nil, errors.New("luau: Thread can only be called from a Go function")
	}
	var ref C.int
	switch C.bridge_pinthread(s.l, &ref) {
	case 0:
	case 1:
		return nil, errors.New("luau: Thread needs a coroutine, but this Go function was called from the main thread")
	default:
		return nil, errors.New("luau: failed to pin the running coroutine")
	}
	root := s.root()
	return &Thread{
		State:   &State{l: s.l, parent: root},
		main:    root,
		ref:     Ref(ref),
		started: true, // it is already running, so Resume continues it
	}, nil
}

// Reset returns the thread to the state of a new one, so a chunk can be
// loaded onto it and resumed again. It works on a thread that finished,
// failed or is suspended (a suspended one is abandoned), and drops
// everything on its stack. Reusing a thread this way is cheaper than closing
// it and creating another, so it is the way to run many short tasks. It
// fails for a thread that is running.
func (t *Thread) Reset() error {
	if !t.State.open() {
		return ErrClosed
	}
	if C.bridge_resetthread(t.main.l, t.State.l) != 0 {
		return errors.New("luau: cannot reset a thread that is running")
	}
	t.started = false
	return nil
}
