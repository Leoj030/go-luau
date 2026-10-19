package luau

/*
#include "bridge.h"
*/
import "C"

import (
	"errors"
	"runtime/cgo"
)

// NavResult is a Requirer's answer to a navigation request.
type NavResult int

const (
	NavSuccess   NavResult = C.BRIDGE_NAV_SUCCESS   // the move worked
	NavAmbiguous NavResult = C.BRIDGE_NAV_AMBIGUOUS // more than one thing matched
	NavNotFound  NavResult = C.BRIDGE_NAV_NOT_FOUND // nothing matched
)

// ConfigStatus says whether the current location has a configuration file,
// and in which syntax.
type ConfigStatus int

const (
	ConfigAbsent    ConfigStatus = C.BRIDGE_CFG_ABSENT
	ConfigAmbiguous ConfigStatus = C.BRIDGE_CFG_AMBIGUOUS // more than one configuration file
	ConfigJSON      ConfigStatus = C.BRIDGE_CFG_JSON      // a .luaurc file
	ConfigLuau      ConfigStatus = C.BRIDGE_CFG_LUAU      // a .config.luau file
)

// ModuleInfo identifies the module a Requirer is pointing at.
type ModuleInfo struct {
	// Chunkname names the module in error messages and tracebacks, and is
	// what Luau passes to Reset when this module requires another. A name
	// starting with "@" is a file name.
	Chunkname string

	// Loadname is handed back to Source to fetch the module's code.
	Loadname string

	// CacheKey identifies the module in the require cache: two requires that
	// reach the same key share one loaded module.
	CacheKey string
}

// A Requirer is the host side of Luau's require-by-string. Luau does the
// resolving: it parses paths such as "./lib/util" and "@alias/util", finds
// aliases in configuration files, caches modules and detects cycles. The
// Requirer only answers questions about its own hierarchy of modules and
// directories.
//
// A Requirer keeps a cursor. Luau moves it with Reset, ToParent and ToChild,
// and then asks about wherever it points. Both modules and directories can
// have children and one parent, and a location can be a module, a directory
// or both. ModulePresent says which.
//
// The methods run while a script is running, on the goroutine that drives
// the State. They must not call back into the State. A panic in one is
// turned into a Luau error. FSRequirer is a ready-made implementation.
type Requirer interface {
	// AllowRequire reports whether the chunk with this name may call require.
	AllowRequire(chunkname string) bool

	// Reset points the cursor at the module that is calling require,
	// identified by its chunk name.
	Reset(chunkname string) NavResult

	// JumpToAlias points the cursor at the exact location an alias refers to.
	// Luau only calls it when it can't resolve the alias path relative to
	// its configuration file.
	JumpToAlias(path string) NavResult

	// ToParent and ToChild move the cursor up and down the hierarchy.
	ToParent() NavResult
	ToChild(name string) NavResult

	// ModulePresent reports whether the cursor points at a module, as
	// opposed to a directory.
	ModulePresent() bool

	// Module describes the module under the cursor. Luau only calls it when
	// ModulePresent is true.
	Module() (ModuleInfo, error)

	// ConfigStatus reports whether a configuration file belongs to the
	// location under the cursor. Without one, Luau moves up with ToParent
	// and asks again.
	ConfigStatus() ConfigStatus

	// Config returns the text of that configuration file. Luau parses it.
	Config() ([]byte, error)

	// Source returns the code of the module with this Loadname.
	Source(loadname string) ([]byte, error)
}

// OpenRequire gives scripts a global require function backed by r.
//
// Luau's require resolves paths relative to the calling script, so the
// chunks you load should be named after what r knows them as (for FSRequirer,
// "@dir/script.luau"). Call OpenRequire before Sandbox, because it adds a
// global. It can be called once per State, and only on the main State, not
// on a Thread.
func (s *State) OpenRequire(r Requirer) error {
	if !s.open() {
		return ErrClosed
	}
	if s.parent != nil {
		return errors.New("luau: OpenRequire must be called on the main State")
	}
	if r == nil {
		return errors.New("luau: OpenRequire needs a Requirer, got nil")
	}
	if s.reqHandle != 0 {
		return errors.New("luau: require is already open")
	}
	h := cgo.NewHandle(r)
	if C.bridge_openrequire(s.l, C.uintptr_t(h)) != 0 {
		h.Delete()
		return errors.New("luau: failed to open require (is the State already sandboxed?)")
	}
	s.reqHandle = h
	return nil
}
