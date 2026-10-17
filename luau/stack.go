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

// Returns the number of values on the stack (inside a Func: the argument count).
func (s *State) Top() int { return int(C.bridge_gettop(s.l)) }

// Converts a negative stack index into an absolute one, using the
// stack height at the time of the call.
func (s *State) abs(idx int) int {
	if idx < 0 {
		return s.Top() + idx + 1
	}
	return idx
}

// Reports whether idx refers to an existing stack slot. Luau is not
// safe to ask about indices outside the stack, so check first.
func (s *State) valid(idx int) bool {
	top := s.Top()
	return (idx >= 1 && idx <= top) || (idx <= -1 && idx >= -top)
}

// Returns the Luau type name of the value at idx ("nil", "boolean",
// "number", "string", "table", "function", "userdata", ...), or "no value"
// if idx is not a valid stack position.
func (s *State) TypeName(idx int) string {
	if !s.valid(idx) {
		return "no value"
	}
	return C.GoString(C.bridge_typename(s.l, C.int(idx)))
}

// Pop removes n values from the top of the stack.
func (s *State) Pop(n int) error {
	top := s.Top()
	if n < 0 || n > top {
		return fmt.Errorf("luau: cannot pop %d values from a stack of %d", n, top)
	}
	C.bridge_settop(s.l, C.int(top-n))
	return nil
}

// Converts a Go value to its Luau equivalent and pushes it on the stack.
// Strings are copied into Luau, so the Go string can be freed or modified afterwards.
func (s *State) Push(v any) error {
	switch x := v.(type) {
	case nil:
		C.bridge_pushnil(s.l)
	case bool:
		b := 0
		if x {
			b = 1
		}
		C.bridge_pushboolean(s.l, C.int(b))
	case int:
		C.bridge_pushnumber(s.l, C.double(x))
	case int64:
		C.bridge_pushnumber(s.l, C.double(x))
	case float64:
		C.bridge_pushnumber(s.l, C.double(x))
	case string:
		var p *C.char
		if len(x) > 0 {
			p = (*C.char)(unsafe.Pointer(unsafe.StringData(x)))
		}
		if C.bridge_pushlstring(s.l, p, C.size_t(len(x))) != 0 {
			return errors.New("luau: failed to push string")
		}
	default:
		return fmt.Errorf("luau: cannot push %T", v)
	}
	return nil
}

// Reads the Luau value at stack index idx (negative counts from the top,
// so -1 is the top value) and converts it to a Go value. It doesn't pop it.
func (s *State) Get(idx int) (any, error) {
	switch C.GoString(C.bridge_typename(s.l, C.int(idx))) {
	case "nil":
		return nil, nil
	case "boolean":
		return C.bridge_toboolean(s.l, C.int(idx)) != 0, nil
	case "number":
		var ok C.int
		return float64(C.bridge_tonumber(s.l, C.int(idx), &ok)), nil
	case "string":
		var n C.size_t
		p := C.bridge_tostring(s.l, C.int(idx), &n)
		return C.GoStringN(p, C.int(n)), nil
	default:
		return nil, fmt.Errorf("luau: unsupported type %s", C.GoString(C.bridge_typename(s.l, C.int(idx))))
	}
}
