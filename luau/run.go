package luau

/*
#include <stdlib.h>
#include "bridge.h"
*/
import "C"

import (
	"errors"
	"unsafe"
)

// multRet asks Luau to return every result (LUA_MULTRET).
const multRet = -1

// Compile turns Luau source into bytecode. The result can be cached and
// loaded into any number of states.
//
// A syntax error is returned as an *Error (Kind KindSyntax) with the line
// and column where parsing failed. Chunk is empty, because Compile doesn't
// know the chunk's name.
func Compile(src string) ([]byte, error) {
	csrc := C.CString(src) // never NULL, even for an empty string
	defer C.free(unsafe.Pointer(csrc))

	var n C.size_t
	p := C.bridge_compile(csrc, C.size_t(len(src)), &n)
	if p == nil {
		return nil, errors.New("luau: compile failed (out of memory)")
	}
	defer C.free(unsafe.Pointer(p)) // luau_compile returns a malloc'd buffer

	bc := C.GoBytes(unsafe.Pointer(p), C.int(n))
	if len(bc) > 0 && bc[0] == 0 {
		// Luau encodes a failed compile as bytecode that starts with 0 and
		// holds the message. It only has the line, so ask the parser for
		// the full position.
		return nil, syntaxError(csrc, len(src), bc)
	}
	return bc, nil
}

func syntaxError(csrc *C.char, n int, bc []byte) *Error {
	var line, col C.int
	var msg *C.char
	if C.bridge_parseerror(csrc, C.size_t(n), &line, &col, &msg) != 0 {
		defer C.free(unsafe.Pointer(msg))
		return &Error{Kind: KindSyntax, Message: C.GoString(msg), Line: int(line), Column: int(col)}
	}
	// Fallback: use what the compiler said (":line: message").
	e := &Error{Kind: KindSyntax, Message: string(bc[1:])}
	if _, l, rest, ok := splitLocation(e.Message); ok {
		e.Line, e.Message = l, rest
	}
	return e
}

// Load loads bytecode and pushes the resulting chunk onto the stack as a
// function.
//
// chunkname labels the chunk in error messages. A name starting with "="
// is used as-is ("=script" appears as "script:1: ..."), and "@" marks a
// file name.
//
// If the bytecode is an encoded compile error, Load returns it as an *Error
// with Kind KindSyntax (it has a line but no column, because only Compile
// can find the column).
func (s *State) Load(bytecode []byte, chunkname string) error {
	if !s.open() {
		return ErrClosed
	}
	if len(bytecode) == 0 {
		return errors.New("luau: empty bytecode")
	}
	base := s.Top()
	name := C.CString(chunkname)
	defer C.free(unsafe.Pointer(name))

	if st := C.bridge_load(s.l, name, (*C.char)(unsafe.Pointer(&bytecode[0])), C.size_t(len(bytecode))); st != 0 {
		e := s.failWith(base, st)
		if bytecode[0] == 0 {
			e.Kind = KindSyntax
		}
		return e
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
	if !s.open() {
		return nil, ErrClosed
	}
	if s.Top() < 1 {
		return nil, errors.New("luau: Call needs a function on the stack")
	}
	base := s.Top() - 1 // number of values below the function

	if C.bridge_checkstack(s.l, C.int(len(args)+1)) == 0 {
		C.bridge_settop(s.l, C.int(base))
		return nil, ErrStackOverflow
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

// DoString compiles and runs src, discarding any results. Failures are
// returned as an *Error, with the chunk named "dostring".
func (s *State) DoString(src string) error {
	if !s.open() {
		return ErrClosed
	}
	bc, err := Compile(src)
	if err != nil {
		var le *Error
		if errors.As(err, &le) {
			le.Chunk = "dostring"
		}
		return err
	}
	base := s.Top()
	if err := s.Load(bc, "=dostring"); err != nil {
		return err
	}
	if C.bridge_checkstack(s.l, 1) == 0 { // room for the error handler
		C.bridge_settop(s.l, C.int(base))
		return ErrStackOverflow
	}
	if st := C.bridge_pcall(s.l, 0, 0); st != 0 { // 0 results: nothing to convert
		return s.failWith(base, st)
	}
	return nil
}
