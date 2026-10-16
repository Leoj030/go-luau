// Package core holds the vendored Luau sources. It exists so cgo compiles
// them once and Go caches the result separately from the bridge code.
package core

/*
#cgo CPPFLAGS: -I${SRCDIR} -I${SRCDIR}/../include
#cgo CXXFLAGS: -std=c++17
*/
import "C"
