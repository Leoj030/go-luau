package luau

/*
#include <stdlib.h>
#include "bridge.h"
*/
import "C"

import (
	"errors"
	"fmt"
	"unsafe"
)

// Assigns a Go value to a global variable in the Luau state,
// so scripts can read it by name (e.g. SetGlobal("name", "Go") lets a
// script use `name`).
//
// Globals are shared by every script running in this state. Scripts should
// copy them into locals for speed, and once the state is sandboxed, set
// your globals BEFORE sandboxing, because scripts can't modify them after.
//
// Supported value types: nil, bool, int, int64, float64, string.
func (s *State) SetGlobal(name string, v any) error {
	top := s.Top()
	if err := s.Push(v); err != nil {
		return err
	}
	cname := C.CString(name)
	defer C.free(unsafe.Pointer(cname))
	if C.bridge_setglobal(s.l, cname) != 0 {
		C.bridge_settop(s.l, C.int(top))
		return fmt.Errorf("luau: failed to set global %q", name)
	}
	return nil
}

// Reads a global variable from the Luau state and returns it as
// a Go value. A global that doesn't exist comes back as nil with no error.
//
// Numbers always come back as float64, because Luau numbers are doubles.
// Tables, functions and other reference types aren't supported yet and
// return an error.
func (s *State) GetGlobal(name string) (any, error) {
	top := s.Top()
	defer C.bridge_settop(s.l, C.int(top))
	cname := C.CString(name)
	defer C.free(unsafe.Pointer(cname))
	if C.bridge_getglobal(s.l, cname) != 0 {
		return nil, fmt.Errorf("luau: failed to get global %q", name)
	}
	return s.Get(-1)
}

// PushGlobal pushes the global called name without converting it, so it
// works for any type (functions, tables, ...). Pop it when you're done.
func (s *State) PushGlobal(name string) error {
	cname := C.CString(name)
	defer C.free(unsafe.Pointer(cname))
	if C.bridge_getglobal(s.l, cname) != 0 {
		return fmt.Errorf("luau: failed to get global %q", name)
	}
	return nil
}

// PopGlobal pops the value on top of the stack and assigns it to the
// global called name.
func (s *State) PopGlobal(name string) error {
	top := s.Top()
	if top < 1 {
		return errors.New("luau: PopGlobal needs a value on the stack")
	}
	cname := C.CString(name)
	defer C.free(unsafe.Pointer(cname))
	if C.bridge_setglobal(s.l, cname) != 0 {
		C.bridge_settop(s.l, C.int(top-1))
		return fmt.Errorf("luau: failed to set global %q", name)
	}
	return nil
}
