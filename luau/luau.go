package luau

/*
#cgo CPPFLAGS: -I${SRCDIR} -I${SRCDIR}/include
#cgo CXXFLAGS: -std=c++17
#include <stdlib.h>
#include "bridge.h"
*/
import "C"

import (
	"fmt"
	"errors"
	"unsafe"
	"runtime/cgo"

	_ "github.com/Leoj030/go-luau/luau/core"
)

// A Go function callable from Luau. Read arguments from the stack
// (index 1 is the first argument, Top() is the count), push results, and
// return how many you pushed. A non-nil error becomes a Luau error.
type Func func(L *State) (nresults int, err error)

type State struct {
	l      *C.lua_State
	parent *State // non-nil for the per-call view handed to Go functions

	handle cgo.Handle
	funcs  map[int]Func
	nextID int
	names  []*C.char // debug names; Luau keeps these pointers, so they live until Close
}

func NewState() (*State, error) {
	s := &State{funcs: map[int]Func{}}
	s.handle = cgo.NewHandle(s)
	s.l = C.bridge_newstate(C.uintptr_t(s.handle))
	if s.l == nil {
		s.handle.Delete()
		return nil, errors.New("luau: failed to create state")
	}
	return s, nil
}

func (s *State) Close() {
	if s.parent != nil || s.l == nil {
		return // call views don't own the state
	}
	C.bridge_close(s.l)
	s.l = nil
	s.handle.Delete()
	for _, n := range s.names {
		C.free(unsafe.Pointer(n))
	}
	s.names, s.funcs = nil, nil
}

// Returns the number of values on the stack (inside a Func: the argument count).
func (s *State) Top() int { return int(C.bridge_gettop(s.l)) }

// Compiles and runs src. Lua errors come back as a Go error.
func (s *State) DoString(src string) error {
	csrc := C.CString(src)
	defer C.free(unsafe.Pointer(csrc))
	name := C.CString("=dostring")
	defer C.free(unsafe.Pointer(name))

	if status := C.bridge_dostring(s.l, csrc, C.size_t(len(src)), name); status != 0 {
		var n C.size_t
		p := C.bridge_tostring(s.l, -1, &n)
		msg := "unknown error"
		if p != nil {
			msg = C.GoStringN(p, C.int(n))
		}
		C.bridge_settop(s.l, 0)
		return errors.New(msg)
	}
	return nil
}

/*
	Converts a Go value to its Luau equivalent and pushes it on the stack.
	Strings are copied into Luau, so the Go string can be freed or modified afterwards.
*/
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

/*
	Reads the Luau value at stack index idx (negative counts from the top,
	so -1 is the top value) and converts it to a Go value. It doesn't pop it.
*/
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

/* 
	Assigns a Go value to a global variable in the Luau state,
	so scripts can read it by name (e.g. SetGlobal("name", "Go") lets a
	script use `name`).

	Globals are shared by every script running in this state. Scripts should
	copy them into locals for speed, and once the state is sandboxed, set
	your globals BEFORE sandboxing, because scripts can't modify them after.

	Supported value types: nil, bool, int, int64, float64, string.
*/
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

/*
	Reads a global variable from the Luau state and returns it as
	a Go value. A global that doesn't exist comes back as nil with no error.

	Numbers always come back as float64, because Luau numbers are doubles.
	Tables, functions and other reference types aren't supported yet and
	return an error.
*/
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

// Pushes a Go function onto the stack as a Luau function.
func (s *State) PushFunction(name string, fn Func) error {
	id := s.nextID
	s.nextID++
	s.funcs[id] = fn

	cname := C.CString(name)
	s.names = append(s.names, cname) // freed in Close, not now

	if C.bridge_pushgofunction(s.l, C.int(id), cname) != 0 {
		delete(s.funcs, id)
		return errors.New("luau: failed to push function")
	}
	return nil
}

// Exposes a Go function to scripts as a global.
func (s *State) Register(name string, fn Func) error {
	if err := s.PushFunction(name, fn); err != nil {
		return err
	}
	cname := C.CString(name)
	defer C.free(unsafe.Pointer(cname))
	if C.bridge_setglobal(s.l, cname) != 0 {
		return fmt.Errorf("luau: failed to set global %q", name)
	}
	return nil
}

//export goCallback
func goCallback(L *C.lua_State, id C.int) (ret C.int) {
	// A Go panic must never unwind into C, so turn it into a Luau error.
	defer func() {
		if r := recover(); r != nil {
			(&State{l: L}).Push(fmt.Sprint("go panic: ", r))
			ret = -1
		}
	}()

	main := cgo.Handle(C.bridge_getuserdata(L)).Value().(*State)
	fn := main.funcs[int(id)]
	if fn == nil {
		panic("unknown function id")
	}

	// L may be a coroutine, not the main thread, so wrap this exact L.
	call := &State{l: L, parent: main}
	n, err := fn(call)
	if err == nil && (n < 0 || n > call.Top()) {
		err = errors.New("go function returned more results than it pushed")
	}
	if err != nil {
		call.Push(err.Error())
		return -1
	}
	return C.int(n)
}
