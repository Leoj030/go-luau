package luau

/*
#cgo CPPFLAGS: -I${SRCDIR} -I${SRCDIR}/include
#cgo CXXFLAGS: -std=c++17
#include <stdlib.h>
#include "bridge.h"
*/
import "C"

import (
	"errors"
	"unsafe"
)

type State struct{ l *C.lua_State }

func NewState() (*State, error) {
	l := C.bridge_newstate()
	if l == nil {
		return nil, errors.New("luau: failed to create state")
	}
	return &State{l: l}, nil
}

func (s *State) Close() {
	if s.l != nil {
		C.bridge_close(s.l)
		s.l = nil
	}
}

// DoString compiles and runs src. Lua errors come back as a Go error.
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
			msg = C.GoStringN(p, C.int(n))  // copy now, the pointer dies when we pop
		}
		C.bridge_settop(s.l, 0)
		return errors.New(msg)
	}
	return nil
}