package luau

/*
#include "bridge.h"
*/
import "C"

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

var (
	// Returned (wrapped) when a script was stopped by Interrupt.
	ErrInterrupted = errors.New("luau: script interrupted")
	// Returned (wrapped) when a script exceeded the memory limit.
	ErrMemory = errors.New("luau: memory limit exceeded")
)

// ErrorKind says what went wrong.
type ErrorKind int

const (
	KindRuntime     ErrorKind = iota // the script raised an error, or a Go function it called failed
	KindSyntax                       // the source didn't parse
	KindMemory                       // the memory limit was exceeded
	KindInterrupted                  // stopped by Interrupt or a cancelled context
)

func (k ErrorKind) String() string {
	switch k {
	case KindRuntime:
		return "runtime"
	case KindSyntax:
		return "syntax"
	case KindMemory:
		return "memory"
	case KindInterrupted:
		return "interrupted"
	}
	return "unknown"
}

// Error is what Compile, Load, Call, DoString and Thread.Resume return when
// a script fails. Use errors.As to get at the fields:
//
//	var le *luau.Error
//	if errors.As(err, &le) { fmt.Println(le.Line, le.Column, le.Traceback) }
//
// errors.Is(err, ErrMemory) and errors.Is(err, ErrInterrupted) also work.
type Error struct {
	Kind ErrorKind

	// Message is the error text without the "chunk:line:" prefix.
	Message string

	// Chunk, Line and Column say where it happened. Line and Column count
	// from 1, and 0 means unknown.
	//
	// Syntax errors have a line and a column. Runtime errors have a line
	// but never a column: Luau's bytecode records lines only. For runtime
	// errors the position is read from the message Luau produced, and it is
	// 0 when the error carried none (error("x", 0), or an error raised by a
	// Go function).
	Chunk  string
	Line   int
	Column int

	// Traceback lists the stack frames at the point of the error, innermost
	// first, one per line. It is empty for syntax and memory errors.
	Traceback string

	// Value is the raw error value when it was a string, number or boolean.
	// For strings it is the full text including any "chunk:line:" prefix.
	Value any

	// When the script raised a table, function or userdata, HasRef is true
	// and Ref refers to it: read it with PushRef. Release it with Unref when
	// you are done. Otherwise it stays alive until the state closes.
	Ref    Ref
	HasRef bool
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString(e.Chunk)
	if e.Line > 0 {
		if b.Len() > 0 {
			b.WriteByte(':')
		}
		b.WriteString(strconv.Itoa(e.Line))
		if e.Column > 0 {
			b.WriteByte(':')
			b.WriteString(strconv.Itoa(e.Column))
		}
	}
	if b.Len() > 0 {
		b.WriteString(": ")
	}
	b.WriteString(e.Message)
	return b.String()
}

// Lets errors.Is match ErrMemory and ErrInterrupted.
func (e *Error) Unwrap() error {
	switch e.Kind {
	case KindMemory:
		return ErrMemory
	case KindInterrupted:
		return ErrInterrupted
	}
	return nil
}

// Splits Luau's "chunk:line: rest" prefix.
func splitLocation(msg string) (chunk string, line int, rest string, ok bool) {
	for i := 0; i < len(msg); i++ {
		if msg[i] != ':' {
			continue
		}
		j := i + 1
		for j < len(msg) && msg[j] >= '0' && msg[j] <= '9' {
			j++
		}
		if j == i+1 || j >= len(msg) || msg[j] != ':' {
			continue
		}
		n, err := strconv.Atoi(msg[i+1 : j])
		if err != nil {
			continue
		}
		return msg[:i], n, strings.TrimPrefix(msg[j+1:], " "), true
	}
	return "", 0, msg, false
}

// Returns the traceback recorded for the last error and clears it.
func (s *State) takeTraceback() string {
	var n C.size_t
	p := C.bridge_traceback(s.l, &n)
	if p == nil || n == 0 {
		return ""
	}
	tb := C.GoStringN(p, C.int(n))
	C.bridge_cleartraceback(s.l)
	return tb
}

// failWith builds an *Error from the error value on top of the stack, then
// truncates the stack back to base. status is the Luau status code.
func (s *State) failWith(base int, status C.int) *Error {
	e := &Error{Kind: KindRuntime}
	isString := false

	switch tn := s.TypeName(-1); tn {
	case "string":
		var n C.size_t
		p := C.bridge_tostring(s.l, -1, &n)
		e.Message = C.GoStringN(p, C.int(n)) // copy before the stack is truncated
		e.Value = e.Message
		isString = true
	case "nil":
		e.Message = "nil"
	case "boolean", "number":
		e.Value, _ = s.Get(-1)
		e.Message = fmt.Sprint(e.Value)
	case "no value":
		e.Message = "unknown error"
	default:
		e.Message = "error object is a " + tn + " value"
		if r, err := s.Ref(-1); err == nil {
			e.Ref, e.HasRef = r, true
		}
	}

	e.Traceback = s.takeTraceback()
	C.bridge_settop(s.l, C.int(base))

	switch {
	case status == C.bridge_errmem():
		e.Kind = KindMemory
	case C.bridge_isinterrupted(s.l) != 0:
		e.Kind = KindInterrupted
	}

	// Pull "chunk:line:" out of the message, but only if that location
	// really is one of the frames in the traceback. That keeps text like
	// "fetch: 404: not found" from a Go function from being misread.
	if isString && e.Kind != KindMemory {
		if chunk, line, rest, ok := splitLocation(e.Message); ok &&
			(e.Traceback == "" || strings.Contains(e.Traceback, chunk+":"+strconv.Itoa(line))) {
			e.Chunk, e.Line, e.Message = chunk, line, rest
		}
	}
	return e
}
