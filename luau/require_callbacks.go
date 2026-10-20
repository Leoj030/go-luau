package luau

/*
#include <stdlib.h>
#include "bridge.h"
*/
import "C"

import (
	"fmt"
	"runtime/cgo"
	"unsafe"
)

// requirerOf finds the Requirer behind a callback's ctx value.
func requirerOf(ctx C.uintptr_t) Requirer { return cgo.Handle(ctx).Value().(Requirer) }

// cBytes copies b into C memory, with a NUL after it, for the caller to
// free. An empty slice still gets a valid allocation.
func cBytes(b []byte) *C.char {
	p := C.malloc(C.size_t(len(b) + 1))
	if len(b) > 0 {
		copy(unsafe.Slice((*byte)(p), len(b)), b)
	}
	*(*byte)(unsafe.Add(p, len(b))) = 0
	return (*C.char)(p)
}

// Every callback recovers panics: a Go panic must never unwind into C.

//export goReqAllowed
func goReqAllowed(ctx C.uintptr_t, chunkname *C.char) (ret C.int) {
	defer func() {
		if recover() != nil {
			ret = 0
		}
	}()
	if requirerOf(ctx).AllowRequire(C.GoString(chunkname)) {
		return 1
	}
	return 0
}

//export goReqReset
func goReqReset(ctx C.uintptr_t, chunkname *C.char) (ret C.int) {
	defer func() {
		if recover() != nil {
			ret = C.BRIDGE_NAV_NOT_FOUND
		}
	}()
	return C.int(requirerOf(ctx).Reset(C.GoString(chunkname)))
}

//export goReqJump
func goReqJump(ctx C.uintptr_t, path *C.char) (ret C.int) {
	defer func() {
		if recover() != nil {
			ret = C.BRIDGE_NAV_NOT_FOUND
		}
	}()
	return C.int(requirerOf(ctx).JumpToAlias(C.GoString(path)))
}

//export goReqToParent
func goReqToParent(ctx C.uintptr_t) (ret C.int) {
	defer func() {
		if recover() != nil {
			ret = C.BRIDGE_NAV_NOT_FOUND
		}
	}()
	return C.int(requirerOf(ctx).ToParent())
}

//export goReqToChild
func goReqToChild(ctx C.uintptr_t, name *C.char) (ret C.int) {
	defer func() {
		if recover() != nil {
			ret = C.BRIDGE_NAV_NOT_FOUND
		}
	}()
	return C.int(requirerOf(ctx).ToChild(C.GoString(name)))
}

//export goReqPresent
func goReqPresent(ctx C.uintptr_t) (ret C.int) {
	defer func() {
		if recover() != nil {
			ret = 0
		}
	}()
	if requirerOf(ctx).ModulePresent() {
		return 1
	}
	return 0
}

// goReqName returns one of the three names of the module under the cursor
// (which: 0 chunkname, 1 loadname, 2 cache key), or NULL on failure.
//
//export goReqName
func goReqName(ctx C.uintptr_t, which C.int) (ret *C.char) {
	defer func() {
		if recover() != nil {
			ret = nil
		}
	}()
	info, err := requirerOf(ctx).Module()
	if err != nil {
		return nil
	}
	switch which {
	case 0:
		return C.CString(info.Chunkname)
	case 1:
		return C.CString(info.Loadname)
	}
	return C.CString(info.CacheKey)
}

//export goReqConfigStatus
func goReqConfigStatus(ctx C.uintptr_t) (ret C.int) {
	defer func() {
		if recover() != nil {
			ret = C.BRIDGE_CFG_ABSENT
		}
	}()
	return C.int(requirerOf(ctx).ConfigStatus())
}

//export goReqConfig
func goReqConfig(ctx C.uintptr_t, outlen *C.size_t) (ret *C.char) {
	defer func() {
		if recover() != nil {
			ret = nil
		}
	}()
	data, err := requirerOf(ctx).Config()
	if err != nil {
		return nil
	}
	*outlen = C.size_t(len(data))
	return cBytes(data)
}

// goReqSource returns a module's code, or NULL with a message in *errmsg.
//
//export goReqSource
func goReqSource(ctx C.uintptr_t, loadname *C.char, outlen *C.size_t, errmsg **C.char) (ret *C.char) {
	defer func() {
		if r := recover(); r != nil {
			*errmsg = C.CString(fmt.Sprint("go panic: ", r))
			ret = nil
		}
	}()
	data, err := requirerOf(ctx).Source(C.GoString(loadname))
	if err != nil {
		*errmsg = C.CString(err.Error())
		return nil
	}
	*outlen = C.size_t(len(data))
	return cBytes(data)
}

// goReqAsync reports whether the Requirer lets modules yield.
//
//export goReqAsync
func goReqAsync(ctx C.uintptr_t) (ret C.int) {
	defer func() {
		if recover() != nil {
			ret = 0
		}
	}()
	if _, ok := requirerOf(ctx).(YieldingRequirer); ok {
		return 1
	}
	return 0
}
