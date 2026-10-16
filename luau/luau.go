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
	"context"
	"sync"

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

// Selects standard libraries for NewStateWith.
type Lib uint32

const (
	LibBase      Lib = C.BRIDGE_LIB_BASE
	LibCoroutine Lib = C.BRIDGE_LIB_COROUTINE
	LibTable     Lib = C.BRIDGE_LIB_TABLE
	LibOS        Lib = C.BRIDGE_LIB_OS
	LibString    Lib = C.BRIDGE_LIB_STRING
	LibBit32     Lib = C.BRIDGE_LIB_BIT32
	LibBuffer    Lib = C.BRIDGE_LIB_BUFFER
	LibUTF8      Lib = C.BRIDGE_LIB_UTF8
	LibClass     Lib = C.BRIDGE_LIB_CLASS
	LibMath      Lib = C.BRIDGE_LIB_MATH
	LibDebug     Lib = C.BRIDGE_LIB_DEBUG
	LibVector    Lib = C.BRIDGE_LIB_VECTOR
	LibInteger   Lib = C.BRIDGE_LIB_INTEGER

	// LibNone opens no libraries at all, not even the base library.
	LibNone Lib = C.BRIDGE_LIB_NONE

	// LibDefault is the set a zero-value Options opens: what luaL_openlibs
	// opens in the vendored Luau version. It leaves out LibClass and
	// LibInteger, which Luau provides but does not open by default.
	LibDefault = LibBase | LibCoroutine | LibTable | LibOS | LibString | LibBit32 |
		LibBuffer | LibUTF8 | LibMath | LibDebug | LibVector

	// LibAll opens every library this binding knows about, including the
	// ones that are off by default.
	LibAll = LibDefault | LibClass | LibInteger
)

// Configures a new State.
type Options struct {
	// Picks the standard libraries to open, for example
	// LibBase|LibMath|LibString|LibTable. The zero value opens Luau's
	// default set (what luaL_openlibs opens). Use LibNone for no libraries.
	Libs Lib

	// Caps the memory the state may allocate, in bytes. Going
	// over it makes the running script fail with ErrMemory. 0 means no limit.
	MemoryLimit int
}

var (
	// Returned (wrapped) when a script was stopped by Interrupt.
	ErrInterrupted = errors.New("luau: script interrupted")
	// Returned (wrapped) when a script exceeded the memory limit.
	ErrMemory = errors.New("luau: memory limit exceeded")
)

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

// Returns the number of bytes the state currently has allocated.
func (s *State) MemoryUsed() int { return int(C.bridge_memused(s.l)) }

// Locks the state down for running scripts: library tables and the
// globals table become read-only, so scripts can no longer modify the
// standard libraries or assign new globals.
//
// Call it after opening libraries and setting up everything your scripts
// need (SetGlobal, Register, ...). Once sandboxed, those fail too, because
// the globals table is read-only for you as well.
func (s *State) Sandbox() { C.bridge_sandbox(s.l) }

// Asks the running script to stop. It is safe to call from any
// goroutine. The script fails with an error wrapping ErrInterrupted at its
// next safepoint (a loop iteration or a call), and keeps failing until
// ClearInterrupt is called. Never call it after Close.
func (s *State) Interrupt() { C.bridge_interrupt(s.l) }

// Clears a pending interrupt so the state can run again.
func (s *State) ClearInterrupt() { C.bridge_clearinterrupt(s.l) }

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

// multRet asks Luau to return every result (LUA_MULTRET).
const multRet = -1

// Turns Luau source into bytecode. The result can be cached and
// loaded into any number of states.
//
// Compile does not report syntax errors: Luau encodes a failed compile as
// special bytecode, and Load returns the error (with the chunk name).
func Compile(src string) ([]byte, error) {
	csrc := C.CString(src) // never NULL, even for an empty string
	defer C.free(unsafe.Pointer(csrc))

	var n C.size_t
	p := C.bridge_compile(csrc, C.size_t(len(src)), &n)
	if p == nil {
		return nil, errors.New("luau: compile failed (out of memory)")
	}
	defer C.free(unsafe.Pointer(p)) // luau_compile returns a malloc'd buffer
	return C.GoBytes(unsafe.Pointer(p), C.int(n)), nil
}

// Builds a Go error from the error value on top of the stack,
// then truncates the stack back to base. status is the Luau status code.
func (s *State) failWith(base int, status C.int) error {
	var n C.size_t
	msg := ""
	if p := C.bridge_tostring(s.l, -1, &n); p != nil {
		msg = C.GoStringN(p, C.int(n)) // copy before the stack is truncated
	} else {
		msg = "error object of type " + C.GoString(C.bridge_typename(s.l, -1))
	}
	C.bridge_settop(s.l, C.int(base))

	switch {
	case status == C.bridge_errmem():
		return fmt.Errorf("%w: %s", ErrMemory, msg)
	case C.bridge_isinterrupted(s.l) != 0:
		return fmt.Errorf("%w: %s", ErrInterrupted, msg)
	}
	return errors.New(msg)
}

// Loads bytecode and pushes the resulting chunk onto the stack as a
// function. Syntax errors from Compile are reported here.
//
// chunkname labels the chunk in error messages. A name starting with "="
// is used as-is ("=script" appears as "script:1: ..."), and "@" marks a
// file name.
func (s *State) Load(bytecode []byte, chunkname string) error {
	if len(bytecode) == 0 {
		return errors.New("luau: empty bytecode")
	}
	base := s.Top()
	name := C.CString(chunkname)
	defer C.free(unsafe.Pointer(name))

	if st := C.bridge_load(s.l, name, (*C.char)(unsafe.Pointer(&bytecode[0])), C.size_t(len(bytecode))); st != 0 {
		return s.failWith(base, st)
	}
	return nil
}

// Calls the function on top of the stack with args and returns its
// results as Go values. It consumes the function, and the stack is left
// as it was below it, on success and on error alike.
//
// Results use the same conversions as Get, so a result of an unsupported
// type (such as a table) returns an error.
func (s *State) Call(args ...any) ([]any, error) {
	if s.Top() < 1 {
		return nil, errors.New("luau: Call needs a function on the stack")
	}
	base := s.Top() - 1 // number of values below the function

	if C.bridge_checkstack(s.l, C.int(len(args)+1)) == 0 {
		C.bridge_settop(s.l, C.int(base))
		return nil, errors.New("luau: stack overflow")
	}
	for _, a := range args {
		if err := s.Push(a); err != nil {
			C.bridge_settop(s.l, C.int(base))
			return nil, err
		}
	}
	if st := C.bridge_pcall(s.l, C.int(len(args)), multRet); st != 0 {
		return nil, s.failWith(base, st)
	}

	// The results now occupy base+1 .. Top().
	n := s.Top() - base
	results := make([]any, 0, n)
	for i := 1; i <= n; i++ {
		v, err := s.Get(base + i)
		if err != nil {
			C.bridge_settop(s.l, C.int(base))
			return nil, err
		}
		results = append(results, v)
	}
	C.bridge_settop(s.l, C.int(base))
	return results, nil
}

// Compiles and runs src, discarding any results. Luau errors are
// returned as a Go error.
func (s *State) DoString(src string) error {
	bc, err := Compile(src)
	if err != nil {
		return err
	}
	base := s.Top()
	if err := s.Load(bc, "=dostring"); err != nil {
		return err
	}
	if st := C.bridge_pcall(s.l, 0, 0); st != 0 {
		return s.failWith(base, st)
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

// abs converts a negative stack index into an absolute one, using the
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

// checkTable guards every raw table operation. Luau does not validate
// types in release builds, so touching a non-table would corrupt memory.
func (s *State) checkTable(idx int) error {
	if tn := s.TypeName(idx); tn != "table" {
		return fmt.Errorf("luau: stack index %d is %s, not a table", idx, tn)
	}
	return nil
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

// Pushes a new empty table. narr and nrec are size hints for the
// array and hash parts (0 is fine).
func (s *State) NewTable(narr, nrec int) error {
	if C.bridge_createtable(s.l, C.int(narr), C.int(nrec)) != 0 {
		return errors.New("luau: failed to create table")
	}
	return nil
}

// Pushes t[key], where t is the table at idx. The key must not
// contain NUL bytes. Raw access: metamethods are not invoked.
func (s *State) GetField(idx int, key string) error {
	if err := s.checkTable(idx); err != nil {
		return err
	}
	ck := C.CString(key)
	defer C.free(unsafe.Pointer(ck))
	if C.bridge_rawgetfield(s.l, C.int(idx), ck) != 0 {
		return fmt.Errorf("luau: failed to get field %q", key)
	}
	return nil
}

// Validates the "table at idx, value on top" layout that the
// Set* functions use, and returns the table's absolute index.
func (s *State) checkStore(idx int) (int, error) {
	top := s.Top()
	t := s.abs(idx)
	if top < 2 || t < 1 || t >= top {
		return 0, errors.New("luau: need a table and, above it, a value to store")
	}
	return t, s.checkTable(t)
}

// Does t[key] = value, where t is the table at idx and value is
// the top of the stack. The value is popped. Raw access.
func (s *State) SetField(idx int, key string) error {
	t, err := s.checkStore(idx)
	if err != nil {
		return err
	}
	top := s.Top()
	ck := C.CString(key)
	defer C.free(unsafe.Pointer(ck))
	if C.bridge_rawsetfield(s.l, C.int(t), ck) != 0 {
		C.bridge_settop(s.l, C.int(top-1))
		return fmt.Errorf("luau: failed to set field %q", key)
	}
	return nil
}

// Pushes t[n] (arrays are 1-based in Luau), where t is the table
// at idx. Raw access.
func (s *State) GetIndex(idx, n int) error {
	if err := s.checkTable(idx); err != nil {
		return err
	}
	if C.bridge_rawgeti(s.l, C.int(idx), C.int(n)) != 0 {
		return fmt.Errorf("luau: failed to get index %d", n)
	}
	return nil
}

// Does t[n] = value (1-based), where t is the table at idx and
// value is the top of the stack. The value is popped. Raw access.
func (s *State) SetIndex(idx, n int) error {
	t, err := s.checkStore(idx)
	if err != nil {
		return err
	}
	top := s.Top()
	if C.bridge_rawseti(s.l, C.int(t), C.int(n)) != 0 {
		C.bridge_settop(s.l, C.int(top-1))
		return fmt.Errorf("luau: failed to set index %d", n)
	}
	return nil
}

// Returns the raw length of the array part of the table at idx (like
// the # operator without metamethods).
func (s *State) Len(idx int) (int, error) {
	if err := s.checkTable(idx); err != nil {
		return 0, err
	}
	return int(C.bridge_objlen(s.l, C.int(idx))), nil
}

// Advances a table traversal. With the table at idx and a key on top
// of the stack (push nil to start), it pops the key and pushes the next
// key and value, returning true. At the end it pops the key, pushes
// nothing and returns false. Iteration order is unspecified. Most callers
// want ForEach instead.
func (s *State) Next(idx int) (bool, error) {
	if s.Top() < 2 {
		return false, errors.New("luau: Next needs a table and a key on the stack")
	}
	t := s.abs(idx)
	if err := s.checkTable(t); err != nil {
		return false, err
	}
	switch C.bridge_next(s.l, C.int(t)) {
	case 0:
		return false, nil
	case -1:
		return false, errors.New("luau: table traversal failed")
	}
	return true, nil
}

// Calls fn once per entry of the table at idx. While fn runs, the
// key is at stack index -2 and the value at -1. fn must leave the stack as
// it found it. If fn returns an error, iteration stops, the stack is
// restored and the error is returned.
//
// Don't convert a key with anything that changes it in place (Get is safe).
func (s *State) ForEach(idx int, fn func() error) error {
	t := s.abs(idx)
	if err := s.checkTable(t); err != nil {
		return err
	}
	base := s.Top()
	s.Push(nil) // first key
	for {
		ok, err := s.Next(t)
		if err != nil {
			C.bridge_settop(s.l, C.int(base))
			return err
		}
		if !ok {
			return nil // Next already popped the key
		}
		if err := fn(); err != nil {
			C.bridge_settop(s.l, C.int(base))
			return err
		}
		s.Pop(1) // drop the value, keep the key for the next step
	}
}

// A handle that keeps a Luau value alive so Go can hold on to it
// across calls. Release it with Unref when you're done: references are
// never collected automatically (they do go away when the state closes).
// A Ref is valid in the state that created it and in all its coroutines.
type Ref int

// Pins the value at idx and returns a handle to it. The value stays on
// the stack; pop it yourself if you don't need it there.
func (s *State) Ref(idx int) (Ref, error) {
	if s.TypeName(idx) == "no value" {
		return 0, fmt.Errorf("luau: stack index %d is not valid", idx)
	}
	var r C.int
	if C.bridge_ref(s.l, C.int(idx), &r) != 0 {
		return 0, errors.New("luau: failed to create reference")
	}
	return Ref(r), nil
}

// Pushes the value r refers to onto the stack.
func (s *State) PushRef(r Ref) error {
	if C.bridge_getref(s.l, C.int(r)) != 0 {
		return errors.New("luau: failed to push reference")
	}
	return nil
}

// Releases a reference. The value can be collected afterwards, and r
// must not be used again.
func (s *State) Unref(r Ref) { C.bridge_unref(s.l, C.int(r)) }

// GC runs a full garbage collection cycle. Values that are no longer
// reachable (and have no Ref) are freed, and userdata destructors run.
func (s *State) GC() { C.bridge_gc(s.l) }

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