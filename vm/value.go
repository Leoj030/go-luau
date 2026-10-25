package vm

import (
	"context"
	"errors"
	"fmt"

	"github.com/Leoj030/go-luau/luau"
)

var errReleased = errors.New("vm: the value was released")

// Value is a handle to a Lua value: a function, a table, a string, anything.
// It keeps the value alive until you call Release (or the VM is closed).
//
// A Value that a Go function receives as an argument is released
// automatically when the function returns. Call Retain to keep one longer.
// Values that Eval, Call, Get and VM.Get return are yours to release.
type Value struct {
	vm       *VM
	ref      luau.Ref
	typ      string
	released bool
}

// Type returns the Lua type name: "nil", "boolean", "number", "string",
// "table", "function", "userdata" and so on.
func (x *Value) Type() string { return x.typ }

// IsNil reports whether the value is nil.
func (x *Value) IsNil() bool { return x.typ == "nil" }

// Release lets Lua collect the value. It is safe to call more than once.
func (x *Value) Release() {
	if x == nil || x.released {
		return
	}
	x.released = true
	x.vm.cur.Unref(x.ref)
}

// Retain returns a second handle to the same value, which stays valid after
// the Go function that received this one returns. Release it when done.
func (x *Value) Retain() (*Value, error) {
	var out *Value
	err := x.onStack(func(s *luau.State) error {
		var err error
		out, err = x.vm.valueAt(s, -1)
		return err
	})
	return out, err
}

// onStack pushes the value, runs fn with it on top, and cuts the stack back
// afterwards.
func (x *Value) onStack(fn func(s *luau.State) error) error {
	if x == nil || x.released {
		return errReleased
	}
	s := x.vm.cur
	base := s.Top()
	if err := s.PushRef(x.ref); err != nil {
		return err
	}
	err := fn(s)
	s.Pop(s.Top() - base)
	return err
}

// Any converts the value to a Go value: nil, bool, float64, string, or for
// tables a []any, map[string]any or map[any]any. Functions, userdata and
// threads can't be converted, and give an error.
func (x *Value) Any() (any, error) {
	var out any
	err := x.onStack(func(s *luau.State) error {
		var err error
		out, err = s.Get(-1)
		return err
	})
	return out, err
}

// Decode fills the Go value that target points to, such as a struct, slice
// or map, from the Lua value. See luau.State.Decode for the rules.
func (x *Value) Decode(target any) error {
	return x.onStack(func(s *luau.State) error { return s.Decode(-1, target) })
}

// String describes the value: the text of a string, number or boolean, and
// "<table>" or "<function>" for the rest.
func (x *Value) String() string {
	if x == nil {
		return "<nil>"
	}
	out := "<" + x.typ + ">"
	err := x.onStack(func(s *luau.State) error {
		switch x.typ {
		case "nil", "boolean", "number", "string":
			a, err := s.Get(-1)
			if err != nil {
				return err
			}
			out = fmt.Sprint(a)
		}
		return nil
	})
	if err != nil {
		return "<released>"
	}
	return out
}

// Get returns t[key] for a table. Metamethods are not used. The key can be
// any value that Set accepts.
func (x *Value) Get(key any) (*Value, error) {
	var out *Value
	err := x.onStack(func(s *luau.State) error {
		if err := x.vm.push(s, "", key); err != nil {
			return err
		}
		if err := s.RawGet(-2); err != nil {
			return err
		}
		var err error
		out, err = x.vm.valueAt(s, -1)
		return err
	})
	return out, err
}

// Set does t[key] = value for a table. Metamethods are not used, and a
// frozen table gives an error.
func (x *Value) Set(key, value any) error {
	return x.onStack(func(s *luau.State) error {
		if err := x.vm.push(s, "", key); err != nil {
			return err
		}
		if err := x.vm.push(s, "", value); err != nil {
			return err
		}
		return s.RawSet(-3)
	})
}

// Len returns the length of a table's array part.
func (x *Value) Len() (int, error) {
	n := 0
	err := x.onStack(func(s *luau.State) error {
		var err error
		n, err = s.Len(-1)
		return err
	})
	return n, err
}

// Call calls a function with args and returns what it returns, as Values to
// release. A Go function, a map of them, another *Value and the other things
// that Set accepts can be passed as arguments.
func (x *Value) Call(args ...any) ([]*Value, error) {
	return x.CallContext(context.Background(), args...)
}

// CallContext is Call with a context. When ctx is done the call is stopped,
// and the error matches both ctx.Err() and ErrInterrupted.
func (x *Value) CallContext(ctx context.Context, args ...any) ([]*Value, error) {
	if x == nil || x.released {
		return nil, errReleased
	}
	if x.typ != "function" {
		return nil, fmt.Errorf("vm: cannot call a %s value", x.typ)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	v := x.vm
	th, err := v.cur.NewThread()
	if err != nil {
		return nil, err
	}
	defer th.Close()
	if err := th.PushRef(x.ref); err != nil {
		return nil, err
	}
	for _, a := range args {
		if err := v.push(th.State, "", a); err != nil {
			return nil, err
		}
	}
	return v.run(ctx, th, len(args))
}