package luau

/*
#include <stdlib.h>
#include "bridge.h"
*/
import "C"

import (
	"errors"
	"fmt"
	"runtime/cgo"
)

// Finalizer is an optional interface for Go values stored in userdata.
// Finalize runs when Luau garbage-collects the userdata (or when the state
// closes), so you can release resources there.
//
// Finalize runs in the middle of a garbage collection, so it must NOT call
// back into the State, and it should be quick.
type Finalizer interface{ Finalize() }

// PushUserdata pushes a userdata that holds the Go value v. v stays alive
// as long as Luau can still reach the userdata.
func (s *State) PushUserdata(v any) error {
	if !s.open() {
		return ErrClosed
	}
	h := cgo.NewHandle(v)
	if C.bridge_pushuserdata(s.l, C.uintptr_t(h)) != 0 {
		h.Delete()
		return errors.New("luau: failed to create userdata")
	}
	return nil
}

// ToUserdata returns the Go value stored in the userdata at idx. The bool
// is false if idx is invalid or isn't a userdata created by PushUserdata.
func (s *State) ToUserdata(idx int) (any, bool) {
	if !s.valid(idx) {
		return nil, false
	}
	h := C.bridge_touserdata(s.l, C.int(idx))
	if h == 0 {
		return nil, false
	}
	return cgo.Handle(h).Value(), true
}

// Pops the table on top of the stack and makes it the
// metatable of the table or userdata at idx. Use it to give userdata
// methods (an __index table) or operators. Keep a Ref to a shared
// metatable instead of building one per object.
//
// Only tables and userdata are accepted: Luau would otherwise apply the
// metatable to EVERY value of that type (for example all strings).
func (s *State) SetMetatable(idx int) error {
	if !s.open() {
		return ErrClosed
	}
	top := s.Top()
	if top < 2 {
		return errors.New("luau: SetMetatable needs a target and a metatable on the stack")
	}
	if tn := s.TypeName(-1); tn != "table" {
		return fmt.Errorf("luau: metatable must be a table, got %s", tn)
	}
	target := s.abs(idx)
	if !s.valid(target) || target >= top {
		return fmt.Errorf("luau: stack index %d is not a valid target", idx)
	}
	if tn := s.TypeName(target); tn != "table" && tn != "userdata" {
		return fmt.Errorf("luau: cannot set a metatable on a %s", tn)
	}
	if C.bridge_setmetatable(s.l, C.int(target)) == 0 {
		C.bridge_settop(s.l, C.int(top-1))
		return errors.New("luau: failed to set metatable")
	}
	return nil // the metatable was popped
}

//export goUserdataFinalizer
func goUserdataFinalizer(h C.uintptr_t) {
	handle := cgo.Handle(h)
	defer handle.Delete()
	defer func() { recover() }() // a panic must never unwind into C
	if f, ok := handle.Value().(Finalizer); ok {
		f.Finalize()
	}
}
