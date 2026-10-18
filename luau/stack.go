package luau

/*
#include <stdlib.h>
#include "bridge.h"
*/
import "C"

import (
	"fmt"
	"unsafe"
)

// Top returns the number of values on the stack (inside a Func, the
// argument count). A closed State has none, so Top returns 0.
func (s *State) Top() int {
	if !s.open() {
		return 0
	}
	return int(C.bridge_gettop(s.l))
}

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
	if !s.open() {
		return ErrClosed
	}
	top := s.Top()
	if n < 0 || n > top {
		return fmt.Errorf("luau: cannot pop %d values from a stack of %d", n, top)
	}
	C.bridge_settop(s.l, C.int(top-n))
	return nil
}

// pushError says why a push failed: the stack is full, or Luau ran out of
// memory.
func (s *State) pushError() error {
	if C.bridge_checkstack(s.l, 1) == 0 {
		return ErrStackOverflow
	}
	return ErrMemory
}

// CheckStack makes sure the stack has room for n more values, growing it if
// needed. Push and the other functions that add values already reserve space
// for themselves, so CheckStack is only for failing early before a large
// batch of pushes. It returns ErrStackOverflow if the stack can't grow that
// far.
func (s *State) CheckStack(n int) error {
	if !s.open() {
		return ErrClosed
	}
	if n < 0 {
		return fmt.Errorf("luau: CheckStack needs a count of 0 or more, got %d", n)
	}
	if C.bridge_checkstack(s.l, C.int(n)) == 0 {
		return ErrStackOverflow
	}
	return nil
}

// Push converts a Go value to its Luau equivalent and pushes it on the
// stack. Strings are copied into Luau, so the Go string can be freed or
// modified afterwards.
//
// The supported values are:
//
//	nil                                  nil
//	bool                                 boolean
//	any int, uint or float type          number (integers beyond 2^53 lose precision)
//	string                               string
//	[]byte                               string
//	slices and arrays                    array table, 1-based
//	maps with string, number or
//	boolean keys                         table
//	structs                              table keyed by field name
//	pointers                             the value they point to (nil gives nil)
//
// Structs convert their exported fields only. A `luau` tag renames a field
// (`luau:"score"`), `luau:"-"` skips it, and `luau:"score,omitempty"` skips
// it while it holds its zero value. An embedded struct is an ordinary
// field, named after its type.
//
// Named types such as `type ID int` convert by their kind. Everything nests,
// to a depth of 100 levels, and the table is a copy: changing it in Luau
// doesn't change the Go value. If anything fails, nothing is left on the
// stack. Push returns ErrStackOverflow if the stack is full.
func (s *State) Push(v any) error {
	if !s.open() {
		return ErrClosed
	}
	base := s.Top()
	if err := s.pushValue(v, 0); err != nil {
		s.restore(base)
		return err
	}
	return nil
}

// pushValue pushes v and recurses into slices and maps. depth counts the
// levels of nesting so far.
func (s *State) pushValue(v any, depth int) error {
	var rc C.int
	switch x := v.(type) {
	case nil:
		rc = C.bridge_pushnil(s.l)
	case bool:
		b := 0
		if x {
			b = 1
		}
		rc = C.bridge_pushboolean(s.l, C.int(b))
	case int:
		rc = C.bridge_pushnumber(s.l, C.double(x))
	case int64:
		rc = C.bridge_pushnumber(s.l, C.double(x))
	case float64:
		rc = C.bridge_pushnumber(s.l, C.double(x))
	case string:
		var p *C.char
		if len(x) > 0 {
			p = (*C.char)(unsafe.Pointer(unsafe.StringData(x)))
		}
		rc = C.bridge_pushlstring(s.l, p, C.size_t(len(x)))
	default:
		return s.pushComposite(v, depth)
	}
	if rc != 0 {
		return s.pushError()
	}
	return nil
}

// Get reads the Luau value at stack index idx (negative counts from the
// top, so -1 is the top value) and converts it to a Go value. It doesn't
// pop it.
//
// The conversions are:
//
//	nil        nil
//	boolean    bool
//	number     float64
//	string     string
//	table      []any if its keys are exactly 1..n (an empty table too),
//	           map[string]any if every key is a string,
//	           map[any]any otherwise (keys are string, float64 or bool)
//
// Tables are read raw (metamethods are ignored) and converted recursively,
// to a depth of 100 levels, so a table that contains itself is an error.
// Functions, userdata, threads, vectors and buffers are errors too, and so
// is a table with a key that isn't a string, number or boolean. Shared
// sub-tables are copied once per reference.
//
// An index that doesn't exist is an error, which is different from a slot
// that holds nil.
func (s *State) Get(idx int) (any, error) {
	if !s.open() {
		return nil, ErrClosed
	}
	return s.get(idx, 0)
}

// get is Get with the nesting depth of the table being converted.
func (s *State) get(idx, depth int) (any, error) {
	switch tn := s.TypeName(idx); tn {
	case "no value":
		return nil, fmt.Errorf("luau: stack index %d is not valid", idx)
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
	case "table":
		return s.tableToGo(idx, depth)
	default:
		return nil, fmt.Errorf("luau: unsupported type %s", tn)
	}
}
