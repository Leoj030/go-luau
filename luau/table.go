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

// checkTable guards every raw table operation. Luau does not validate
// types in release builds, so touching a non-table would corrupt memory.
func (s *State) checkTable(idx int) error {
	if tn := s.TypeName(idx); tn != "table" {
		return fmt.Errorf("luau: stack index %d is %s, not a table", idx, tn)
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
