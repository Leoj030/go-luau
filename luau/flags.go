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

// SetFastFlag turns one of Luau's fast flags on or off. New language
// features and other experiments arrive behind flags, so this is how to try
// one, such as "DebugLuauIfLocalSyntax". The name is exactly as Luau spells
// it. It returns an error if this build has no flag with that name, which is
// also what happens for flags that belong to the type checker, because
// package luau doesn't include it.
//
// Flags are global to the process and are read without locking by every
// State and by Compile. Set them once at startup, before creating a State or
// compiling anything, and never while a script runs.
func SetFastFlag(name string, enabled bool) error {
	cname := C.CString(name)
	defer C.free(unsafe.Pointer(cname))
	on := C.int(0)
	if enabled {
		on = 1
	}
	if C.bridge_setfastflag(cname, on) == 0 {
		return fmt.Errorf("luau: no fast flag called %q in this build", name)
	}
	return nil
}

// FastFlag reports the value of a fast flag, and whether this build has a
// flag with that name.
func FastFlag(name string) (enabled, exists bool) {
	cname := C.CString(name)
	defer C.free(unsafe.Pointer(cname))
	switch C.bridge_getfastflag(cname) {
	case 1:
		return true, true
	case 0:
		return false, true
	}
	return false, false
}

// FastFlags returns every fast flag in this build with its current value.
// Use it to find out which flags exist, for example before turning on an
// experimental feature.
func FastFlags() map[string]bool {
	n := int(C.bridge_fastflagcount())
	out := make(map[string]bool, n)
	for i := 0; i < n; i++ {
		var on C.int
		if name := C.bridge_fastflagat(C.int(i), &on); name != nil {
			out[C.GoString(name)] = on != 0
		}
	}
	return out
}
