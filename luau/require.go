package luau

/*
#include "bridge.h"
*/
import "C"

import (
	"errors"
	"fmt"
	"runtime/cgo"
	"strings"
	"unsafe"
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

// RegisterModuleTable pops the table on top of the stack and registers it as
// the module that require(path) returns. Unlike RegisterModule, the table
// can hold anything: functions, constants, nested tables.
//
// The path must begin with "@", such as "@std/process". Paths are matched
// without regard to case, and a registered module wins over a file at the
// same path, so a .luau stub for type checkers can sit beside it.
//
// Once registered, the module table is frozen, so no script can replace
// what other scripts will see. Tables inside it stay as they are, so freeze
// them with Freeze if scripts shouldn't change them. The values are a copy
// taken now: for something that should stay current, such as a clock, put a
// function in the module.
//
// If the path or the stack is invalid, nothing changes. Otherwise the table
// is popped, whether or not registration succeeds. Call OpenRequire so that
// scripts can use the module.
func (s *State) RegisterModuleTable(path string) error {
	if !s.open() {
		return ErrClosed
	}
	if !strings.HasPrefix(path, "@") || strings.ContainsRune(path, '\x00') {
		return errors.New("luau: RegisterModuleTable path must begin with @ and contain no NUL bytes")
	}
	top := s.Top()
	if top < 1 {
		return errors.New("luau: RegisterModuleTable needs a table on the stack")
	}
	if err := s.checkTable(-1); err != nil {
		return err
	}

	// From here on the table is consumed.
	defer s.restore(top - 1)
	if err := s.Push(path); err != nil {
		return err
	}
	if C.bridge_registermodule(s.l, C.int(top+1), C.int(top)) != 0 {
		return fmt.Errorf("luau: failed to register module %q", path)
	}
	C.bridge_setreadonly(s.l, C.int(top), 1) // after registering, in case registration writes to the table
	return nil
}

// RegisterModule exposes Go functions as the module that require(path)
// returns. It is the short form of RegisterModuleTable for a module that is
// only functions, and it follows the same rules.
func (s *State) RegisterModule(path string, functions map[string]Func) error {
	if !s.open() {
		return ErrClosed
	}
	if !strings.HasPrefix(path, "@") || strings.ContainsRune(path, '\x00') {
		return errors.New("luau: RegisterModule path must begin with @ and contain no NUL bytes")
	}
	for name, fn := range functions {
		if strings.ContainsRune(name, '\x00') {
			return fmt.Errorf("luau: RegisterModule function name %q contains a NUL byte", name)
		}
		if fn == nil {
			return fmt.Errorf("luau: RegisterModule function %q is nil", name)
		}
	}

	base := s.Top()
	if err := s.NewTable(0, len(functions)); err != nil {
		return err
	}
	for name, fn := range functions {
		if err := s.PushFunction(name, fn); err != nil {
			s.restore(base)
			return err
		}
		if err := s.SetField(-2, name); err != nil {
			s.restore(base)
			return err
		}
	}
	if err := s.RegisterModuleTable(path); err != nil {
		s.restore(base)
		return err
	}
	return nil
}

// ClearRequireCache forgets every module that require has loaded, so the
// next require of each one runs its file again. Modules registered with
// RegisterModule stay registered.
//
// It only affects future requires. A script that already holds a module
// keeps using that copy, a module that required another keeps the copy it
// got, and whatever a module did when it ran (such as registering a
// callback) is not undone. For a real reload, clear everything.
func (s *State) ClearRequireCache() error {
	if !s.open() {
		return ErrClosed
	}
	if C.bridge_clearrequirecache(s.l) != 0 {
		return errors.New("luau: failed to clear the require cache")
	}
	return nil
}

// ClearRequireCacheEntry forgets one loaded module, so the next require of
// it runs it again. cacheKey is the key the Requirer reported for the module
// in ModuleInfo.CacheKey. For FSRequirer that is the path inside the file
// system, such as "lib/util.luau". An unknown key does nothing. The same
// limits apply as for ClearRequireCache.
func (s *State) ClearRequireCacheEntry(cacheKey string) error {
	if !s.open() {
		return ErrClosed
	}
	if strings.ContainsRune(cacheKey, '\x00') {
		return errors.New("luau: a cache key can't contain NUL bytes")
	}
	var p *C.char
	if len(cacheKey) > 0 {
		p = (*C.char)(unsafe.Pointer(unsafe.StringData(cacheKey)))
	}
	if C.bridge_clearrequirecacheentry(s.l, p, C.size_t(len(cacheKey))) != 0 {
		return errors.New("luau: failed to clear the require cache entry")
	}
	return nil
}

// YieldingRequirer is a Requirer that lets modules yield while they load,
// for example by waiting on a timer or a network request at the top level of
// the module, as in Roblox. A Requirer that doesn't implement it keeps the
// old behavior: a module that yields fails with "module can not yield".
//
// The protocol:
//
//  1. A script running in a coroutine calls require, and the module yields.
//     The coroutine that called require is now suspended, and your resume of
//     it returns with done == false and no values.
//  2. You resume the module's thread yourself when its wait is over, using a
//     handle from Thread() inside the Go function that yielded it. Use
//     ResumeN for module threads, not Resume, because a module may return
//     values Resume can't convert.
//  3. When that thread finishes or fails, ModuleFinished is called, inside
//     that same Resume call. waiter is the suspended coroutine that called
//     require. On success the module's result is already on its stack: resume
//     it with waiter.ResumeN(1), and require returns the result. On failure
//     err describes the error: resume it with waiter.ResumeError(err), and
//     require raises it where it was called.
//
// ModuleFinished should only queue that resume for your scheduler, not do it
// on the spot. The waiter is pinned, so Close it when you are done.
//
// A module can only yield when require is called from a coroutine. From the
// main thread it fails as before. If a module is required again while it is
// still loading, it loads a second time.
type YieldingRequirer interface {
	Requirer

	// ModuleFinished is called when a module that yielded while loading has
	// finished (err is nil) or failed.
	ModuleFinished(waiter *Thread, err error)
}
