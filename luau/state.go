package luau

/*
#include <stdlib.h>
#include "bridge.h"
*/
import "C"

import (
	"errors"
	"runtime/cgo"
	"unsafe"
)

type State struct {
	l      *C.lua_State
	parent *State // non-nil for call views and threads: the main state

	call     bool // this is the per-call view handed to a Go function
	yielding bool // a Go function asked to yield (call views only)
	yieldN   int
	depth    int // nested Resume depth (main state only)

	handle    cgo.Handle
	funcs     map[int]Func
	nextID    int
	names     []*C.char  // debug names; Luau keeps these pointers, so they live until Close
	reqHandle cgo.Handle // the Requirer behind require, if OpenRequire was called
}

// Returns the state that owns the Go function registry: the main
// state itself, or the main state behind a call view or a thread.
func (s *State) root() *State {
	if s.parent != nil {
		return s.parent
	}
	return s
}

// Creates a state with the default libraries and no memory limit.
func NewState() (*State, error) { return NewStateWith(Options{}) }

// Creates a state configured by opts.
func NewStateWith(opts Options) (*State, error) {
	if opts.MemoryLimit < 0 {
		return nil, errors.New("luau: MemoryLimit must not be negative")
	}
	libs := opts.Libs
	if libs&LibNone != 0 {
		libs = LibNone // "none" wins over any other flag
	}

	s := &State{funcs: map[int]Func{}}
	s.handle = cgo.NewHandle(s)
	s.l = C.bridge_newstate(C.uintptr_t(s.handle), C.uint(libs), C.size_t(opts.MemoryLimit))
	if s.l == nil {
		s.handle.Delete()
		return nil, errors.New("luau: failed to create state (is MemoryLimit too small?)")
	}
	return s, nil
}

// Close frees the State and everything in it. It is safe to call more than
// once. Afterwards every method returns ErrClosed (or a zero value, for the
// ones that return no error), and so does every Thread of this State.
func (s *State) Close() {
	if s.parent != nil || s.l == nil {
		return // call views don't own the state
	}
	C.bridge_close(s.l)
	s.l = nil
	s.handle.Delete()
	if s.reqHandle != 0 {
		s.reqHandle.Delete()
		s.reqHandle = 0
	}
	for _, n := range s.names {
		C.free(unsafe.Pointer(n))
	}
	s.names, s.funcs = nil, nil
}

// GC runs a full garbage collection cycle. Values that are no longer
// reachable (and have no Ref) are freed, and userdata destructors run.
func (s *State) GC() {
	if s.open() {
		C.bridge_gc(s.l)
	}
}

// open reports whether s can still be used: s itself has not been closed,
// and neither has the main State it belongs to.
func (s *State) open() bool { return s.l != nil && s.root().l != nil }
