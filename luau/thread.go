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
// ThreadFailed.
func (t *Thread) ResumeN(nargs int) (nresults int, done bool, err error) {
	if t.State.l == nil || t.main.l == nil {
		return 0, false, ErrClosed
	}
	switch t.Status() {
	case ThreadSuspended:
	case ThreadFinished:
		return 0, false, errors.New("luau: thread has finished, or has nothing loaded")
	case ThreadFailed:
		return 0, false, errors.New("luau: thread failed and cannot be resumed")
	default:
		return 0, false, errors.New("luau: thread is already running")
	}

	want := nargs
	if !t.started {
		want++ // the function itself
		if t.TypeName(1) != "function" {
			return 0, false, errors.New("luau: load a chunk onto the thread before the first resume")
		}
	}
	if nargs < 0 || t.Top() != want {
		return 0, false, fmt.Errorf("luau: thread stack holds %d values, expected %d", t.Top(), want)
	}
	if t.main.depth >= maxResumeDepth {
		return 0, false, errors.New("luau: too many nested resumes")
	}

	t.started = true
	t.main.depth++
	status := C.bridge_resume(t.State.l, t.main.l, C.int(nargs))
	t.main.depth--

	switch {
	case status == 0:
		return t.Top(), true, nil
	case status == C.bridge_yieldcode():
		return t.Top(), false, nil
	case status == C.bridge_breakcode():
		C.bridge_settop(t.State.l, 0)
		return 0, false, errors.New("luau: thread stopped at a breakpoint (not supported)")
	}
	C.bridge_tracethread(t.State.l)
	return 0, false, t.failWith(0, status)
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
