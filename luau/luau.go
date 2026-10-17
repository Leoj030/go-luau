// Package luau provides Go bindings for the Luau scripting language.
//
// A State owns a Luau VM. Scripts are compiled to bytecode with Compile,
// loaded with Load and run with Call, and Go functions are exposed to
// scripts with Register. The API is stack-based and mirrors Luau's C API.
//
// The files in this package are grouped by topic:
//
//	state.go     creating and closing a State
//	options.go   Options and the standard library flags
//	stack.go     the stack: Top, Pop, Push, Get, TypeName
//	run.go       Compile, Load, Call, DoString
//	errors.go    the Error type and how errors are built
//	globals.go   SetGlobal, GetGlobal, PushGlobal, PopGlobal
//	table.go     tables
//	ref.go       references that keep values alive from Go
//	callback.go  Go functions callable from Luau, and Yield
//	userdata.go  Go values held by scripts
//	thread.go    coroutines
//	sandbox.go   Sandbox, memory use and cancellation
package luau

/*
#cgo CPPFLAGS: -I${SRCDIR} -I${SRCDIR}/include
#cgo CXXFLAGS: -std=c++17
#include <stdlib.h>
#include "bridge.h"
*/
import "C"

import (
	// The vendored Luau sources live in their own package so Go caches them
	// separately from this one. This import links them in.
	_ "github.com/Leoj030/go-luau/luau/core"
)
