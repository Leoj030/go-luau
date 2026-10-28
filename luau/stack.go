package luau

/*
#include <stdlib.h>
#include "bridge.h"
*/
import "C"

import (
	"errors"
	"fmt"
	"reflect"
	"unsafe"
)

// cstr returns a pointer to the bytes of s for a C call that doesn't keep it.
// It is nil for an empty string.
func cstr(s string) *C.char {
	if len(s) == 0 {
		return nil
	}
	return (*C.char)(unsafe.Pointer(unsafe.StringData(s)))
}

// Top returns the number of values on the stack (inside a Func, the
// argument count). A closed State has none, so Top returns 0.
func (s *State) Top() int {
	if !s.open() {
		return 0
	}
	return int(C.bridge_gettop(s.l))
}

// abs converts a negative stack index into an absolute one.
func (s *State) abs(idx int) int {
	if idx < 0 {
		return s.Top() + idx + 1
	}
	return idx
}

// valid reports whether idx refers to an existing stack slot. Luau is not
// safe to ask about indices outside the stack, so this checks first.
func (s *State) valid(idx int) bool {
	return s.open() && C.bridge_typeof(s.l, C.int(idx)) >= 0
}

var typeNames = [...]string{
	C.BRIDGE_T_NIL:      "nil",
	C.BRIDGE_T_BOOLEAN:  "boolean",
	C.BRIDGE_T_NUMBER:   "number",
	C.BRIDGE_T_STRING:   "string",
	C.BRIDGE_T_TABLE:    "table",
	C.BRIDGE_T_FUNCTION: "function",
	C.BRIDGE_T_USERDATA: "userdata",
	C.BRIDGE_T_THREAD:   "thread",
}

// TypeName returns the Luau type name of the value at idx ("nil",
// "boolean", "number", "string", "table", "function", "userdata", ...), or
// "no value" if idx is not a valid stack position. It is one cgo call, and
// allocates nothing for the common types.
func (s *State) TypeName(idx int) string {
	if !s.open() {
		return "no value"
	}
	code := int(C.bridge_typeof(s.l, C.int(idx)))
	switch {
	case code < 0:
		return "no value"
	case code < len(typeNames):
		return typeNames[code]
	}
	return C.GoString(C.bridge_typename(s.l, C.int(idx))) // vector, buffer and the like
}

// Pop removes n values from the top of the stack.
func (s *State) Pop(n int) error {
	if !s.open() {
		return ErrClosed
	}
	if C.bridge_pop(s.l, C.int(n)) != 0 {
		return fmt.Errorf("luau: cannot pop %d values from a stack of %d", n, s.Top())
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
//
// A slice, map or struct is flattened in Go and built by Luau in a single
// cgo call, however many entries it has. To do the flattening on another
// goroutine, use Encode and PushEncoded.
func (s *State) Push(v any) error {
	if !s.open() {
		return ErrClosed
	}
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
		rc = C.bridge_pushlstring(s.l, cstr(x), C.size_t(len(x)))
	default:
		return s.pushOther(v)
	}
	if rc != 0 {
		return s.pushError()
	}
	return nil
}

// pushOther handles the values Push doesn't switch on directly: the other
// number types and named scalars, and everything that becomes a table.
func (s *State) pushOther(v any) error {
	switch rv := reflect.ValueOf(v); rv.Kind() {
	case reflect.Bool:
		return s.Push(rv.Bool())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return s.Push(float64(rv.Int()))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return s.Push(float64(rv.Uint()))
	case reflect.Float32, reflect.Float64:
		return s.Push(rv.Float())
	case reflect.String:
		return s.Push(rv.String())
	}
	return s.pushEncoded(v)
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
// A table is read in a single cgo call, however many entries it has.
//
// An index that doesn't exist is an error, which is different from a slot
// that holds nil.
func (s *State) Get(idx int) (any, error) {
	if !s.open() {
		return nil, ErrClosed
	}
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
		n, err := s.readNode(idx, 0)
		if err != nil {
			return nil, err
		}
		return n.toAny()
	default:
		return nil, fmt.Errorf("luau: unsupported type %s", tn)
	}
}

var errMalformed = errors.New("luau: internal error: malformed value")
