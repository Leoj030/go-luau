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
	"unsafe"
)

// A Go function callable from Luau. Read arguments from the stack
// (index 1 is the first argument, Top() is the count), push results, and
// return how many you pushed. A non-nil error becomes a Luau error.
type Func func(L *State) (nresults int, err error)

// Pushes a Go function onto the stack as a Luau function.
func (s *State) PushFunction(name string, fn Func) error {
	r := s.root()
	id := r.nextID
	r.nextID++
	r.funcs[id] = fn

	cname := C.CString(name)
	r.names = append(r.names, cname) // freed in Close, not now

	if C.bridge_pushgofunction(s.l, C.int(id), cname) != 0 {
		delete(r.funcs, id)
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
	call := &State{l: L, parent: main, call: true}
	n, err := fn(call)
	if err == nil && (n < 0 || n > call.Top()) {
		err = errors.New("go function returned more results than it pushed")
	}
	if err == nil && call.yielding && n != call.yieldN {
		err = fmt.Errorf("go function yielded %d values but returned %d", call.yieldN, n)
	}
	if err != nil {
		call.Push(err.Error())
		return -1
	}
	if call.yielding {
		return C.int(-n - 2) // the trampoline decodes this as a yield
	}
	return C.int(n)
}

// Suspends the running coroutine from inside a Go function. Call it as
// the function's return statement:
//
//	return c.Yield(1)
//
// The top n stack values are handed to whoever resumed the coroutine. When
// it is resumed again, the values passed to Resume become the results of
// the call that yielded.
//
// Yielding only works while a coroutine is running under Thread.Resume (or
// ResumeN). Called from a plain DoString or Call, the script fails with
// Luau's "attempt to yield across metamethod/C-call boundary" error.
func (s *State) Yield(nresults int) (int, error) {
	if !s.call {
		return 0, errors.New("luau: Yield can only be called from a Go function")
	}
	if nresults < 0 || nresults > s.Top() {
		return 0, fmt.Errorf("luau: cannot yield %d values from a stack of %d", nresults, s.Top())
	}
	s.yielding = true
	s.yieldN = nresults
	return nresults, nil
}
