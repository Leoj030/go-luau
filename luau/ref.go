package luau

/*
#include "bridge.h"
*/
import "C"

import (
	"errors"
	"fmt"
)

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
