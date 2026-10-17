package luau

/*
#include "bridge.h"
*/
import "C"

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
