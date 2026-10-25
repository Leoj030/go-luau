package vm

import (
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/Leoj030/go-luau/luau"
)

var (
	errorType = reflect.TypeOf((*error)(nil)).Elem()
	valueType = reflect.TypeOf((*Value)(nil))
)

// push pushes x onto s and leaves the stack as it was if that fails. name
// labels a function in tracebacks.
func (v *VM) push(s *luau.State, name string, x any) error {
	base := s.Top()
	if err := v.pushAny(s, name, x); err != nil {
		s.Pop(s.Top() - base)
		return err
	}
	return nil
}

// pushAny pushes x, adding what luau.State.Push can't do: Go functions, Values,
// and maps and slices that contain them.
func (v *VM) pushAny(s *luau.State, name string, x any) error {
	if name == "" {
		name = "go"
	}
	switch t := x.(type) {
	case *Value:
		if t == nil {
			return s.Push(nil)
		}
		if t.released || t.vm != v {
			return errors.New("vm: the value was released, or belongs to another VM")
		}
		return s.PushRef(t.ref)
	case luau.Func:
		return s.PushFunction(name, v.enterFunc(t))
	case func(*luau.State) (int, error):
		return s.PushFunction(name, v.enterFunc(luau.Func(t)))
	}

	rv := reflect.ValueOf(x)
	switch rv.Kind() {
	case reflect.Func:
		f, err := v.wrapFunc(name, x)
		if err != nil {
			return err
		}
		return s.PushFunction(name, f)
	case reflect.Map:
		if rv.Type().Key().Kind() == reflect.String {
			return v.pushMap(s, rv)
		}
	case reflect.Slice:
		if rv.Type().Elem().Kind() == reflect.Interface {
			return v.pushSlice(s, rv)
		}
	}
	return s.Push(x)
}

func (v *VM) pushMap(s *luau.State, rv reflect.Value) error {
	if err := s.NewTable(0, rv.Len()); err != nil {
		return err
	}
	iter := rv.MapRange()
	for iter.Next() {
		key := iter.Key().String()
		if err := s.Push(key); err != nil {
			return err
		}
		if err := v.pushAny(s, key, iter.Value().Interface()); err != nil {
			return err
		}
		if err := s.RawSet(-3); err != nil {
			return err
		}
	}
	return nil
}

func (v *VM) pushSlice(s *luau.State, rv reflect.Value) error {
	if err := s.NewTable(rv.Len(), 0); err != nil {
		return err
	}
	for i := 0; i < rv.Len(); i++ {
		if err := v.pushAny(s, "", rv.Index(i).Interface()); err != nil {
			return err
		}
		if err := s.SetIndex(-2, i+1); err != nil {
			return err
		}
	}
	return nil
}

// enterFunc wraps a low-level Go function so that the VM's methods work
// inside it, on the thread that called it.
func (v *VM) enterFunc(f luau.Func) luau.Func {
	return func(c *luau.State) (int, error) {
		restore := v.enter(c)
		defer restore()
		return f(c)
	}
}

// wrapFunc turns an ordinary Go function into a Luau function. Arguments are
// decoded with luau.State.Decode, so they can be numbers, strings, bools,
// slices, maps, structs, pointers, any, or *Value for a function or table you
// want to keep as a handle. A missing or nil argument is an error, except for
// a pointer, any or *Value parameter, which stays nil. The last parameter can
// be variadic. The results are pushed in order, and a final error result, if
// non-nil, raises a Lua error instead.
//
// The signature is checked here, so a function that can never work is
// rejected when it is registered. The script's values are checked when it
// calls the function.
func (v *VM) wrapFunc(name string, fn any) (luau.Func, error) {
	rv := reflect.ValueOf(fn)
	if rv.Kind() != reflect.Func || rv.IsNil() {
		return nil, fmt.Errorf("vm: %s is not a function", name)
	}
	t := rv.Type()
	numIn := t.NumIn()
	variadic := t.IsVariadic()
	fixed := numIn
	if variadic {
		fixed--
	}
	numOut := t.NumOut()
	returnsErr := numOut > 0 && t.Out(numOut-1) == errorType
	valueOuts := numOut
	if returnsErr {
		valueOuts--
	}

	for i := 0; i < numIn; i++ {
		pt := t.In(i)
		if variadic && i == numIn-1 {
			pt = pt.Elem()
		}
		if err := checkType(pt, true, map[reflect.Type]bool{}); err != nil {
			return nil, fmt.Errorf("vm: %s: parameter %d: %w", name, i+1, err)
		}
	}
	for i := 0; i < valueOuts; i++ {
		if err := checkType(t.Out(i), false, map[reflect.Type]bool{}); err != nil {
			return nil, fmt.Errorf("vm: %s: result %d: %w", name, i+1, err)
		}
	}

	return func(c *luau.State) (int, error) {
		restore := v.enter(c)
		defer restore()

		// Values passed in are released when the function returns.
		var temps []*Value
		defer func() {
			for _, x := range temps {
				x.Release()
			}
		}()

		argc := c.Top()
		in := make([]reflect.Value, numIn)
		for i := 0; i < fixed; i++ {
			a, err := v.argument(c, name, i+1, argc, t.In(i), &temps)
			if err != nil {
				return 0, err
			}
			in[i] = a
		}

		var out []reflect.Value
		if variadic {
			sliceType := t.In(fixed)
			extra := argc - fixed
			if extra < 0 {
				extra = 0
			}
			rest := reflect.MakeSlice(sliceType, 0, extra)
			for i := fixed; i < argc; i++ {
				a, err := v.argument(c, name, i+1, argc, sliceType.Elem(), &temps)
				if err != nil {
					return 0, err
				}
				rest = reflect.Append(rest, a)
			}
			in[fixed] = rest
			out = rv.CallSlice(in)
		} else {
			out = rv.Call(in)
		}

		if returnsErr {
			if e, _ := out[len(out)-1].Interface().(error); e != nil {
				return 0, e
			}
		}
		for i := 0; i < valueOuts; i++ {
			if err := v.push(c, "", out[i].Interface()); err != nil {
				return 0, fmt.Errorf("%s: result %d: %w", name, i+1, err)
			}
		}
		return valueOuts, nil
	}, nil
}

// argument reads the argument at stack index idx as a value of type typ,
// and reports a mismatch in the same words as Luau's own functions.
func (v *VM) argument(c *luau.State, fname string, idx, argc int, typ reflect.Type, temps *[]*Value) (reflect.Value, error) {
	missing := idx > argc

	// A *Value takes anything: it is a handle, not a conversion.
	if typ == valueType {
		if missing {
			return reflect.Zero(typ), nil
		}
		x, err := v.valueAt(c, idx)
		if err != nil {
			return reflect.Value{}, fmt.Errorf("invalid argument #%d to '%s' (%w)", idx, fname, err)
		}
		*temps = append(*temps, x)
		return reflect.ValueOf(x), nil
	}

	// Pointers and any are optional: a missing or nil argument leaves them nil.
	elem, optional := typ, false
	for elem.Kind() == reflect.Pointer {
		elem, optional = elem.Elem(), true
	}
	if elem.Kind() == reflect.Interface {
		optional = true
	}
	want := wantLua(elem)

	have := "no value"
	if !missing {
		have = c.TypeName(idx)
	}
	if missing || have == "nil" {
		if optional {
			return reflect.Zero(typ), nil
		}
		name := want
		if name == "" {
			name = elem.String()
			if elem.Kind() == reflect.Slice && elem.Elem().Kind() == reflect.Uint8 {
				name = "string"
			}
		}
		if missing {
			return reflect.Value{}, fmt.Errorf("missing argument #%d to '%s' (%s expected)", idx, fname, name)
		}
		return reflect.Value{}, fmt.Errorf("invalid argument #%d to '%s' (%s expected, got nil)", idx, fname, name)
	}
	if want != "" && want != have {
		return reflect.Value{}, fmt.Errorf("invalid argument #%d to '%s' (%s expected, got %s)", idx, fname, want, have)
	}

	ptr := reflect.New(typ)
	if err := c.Decode(idx, ptr.Interface()); err != nil {
		return reflect.Value{}, fmt.Errorf("invalid argument #%d to '%s' (%s)", idx, fname, cleanDecodeError(err))
	}
	return ptr.Elem(), nil
}

// wantLua returns the Lua type that a parameter of Go type t needs, or ""
// when anything goes or Decode does the checking.
func wantLua(t reflect.Type) string {
	switch t.Kind() {
	case reflect.Bool:
		return "boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return "number"
	case reflect.String:
		return "string"
	case reflect.Map, reflect.Struct, reflect.Array:
		return "table"
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 {
			return "" // a []byte takes a string or a table of numbers
		}
		return "table"
	}
	return ""
}

// cleanDecodeError drops the package prefixes from a decode error, so it
// reads well inside an argument error.
func cleanDecodeError(err error) string {
	s := err.Error()
	for _, prefix := range []string{"luau: decode: ", "luau: decode ", "luau: "} {
		if strings.HasPrefix(s, prefix) {
			return strings.TrimPrefix(s, prefix)
		}
	}
	return s
}

// checkType reports why a Go type can't be converted to or from a Lua value,
// or nil if it can. param is true for a parameter, which has to be filled
// from a Lua value, and false for a result, which only has to be pushed.
func checkType(t reflect.Type, param bool, seen map[reflect.Type]bool) error {
	if t == valueType {
		return nil
	}
	if seen[t] {
		return nil // a type that refers to itself, such as a linked list
	}
	seen[t] = true

	switch t.Kind() {
	case reflect.Bool, reflect.String,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return nil
	case reflect.Interface:
		if param && t.NumMethod() != 0 {
			return fmt.Errorf("%s is an interface with methods, so a Lua value can't fill it; use any", t)
		}
		return nil
	case reflect.Pointer, reflect.Slice, reflect.Array:
		return checkType(t.Elem(), param, seen)
	case reflect.Map:
		switch t.Key().Kind() {
		case reflect.String, reflect.Bool, reflect.Interface,
			reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
			reflect.Float32, reflect.Float64:
		default:
			return fmt.Errorf("%s has keys that a Lua table can't have (use strings, numbers or booleans)", t)
		}
		return checkType(t.Elem(), param, seen)
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() || f.Tag.Get("luau") == "-" {
				continue
			}
			if err := checkType(f.Type, param, seen); err != nil {
				return fmt.Errorf("field %s: %w", f.Name, err)
			}
		}
		return nil
	case reflect.Func:
		if param {
			return errors.New("a Go function can't be filled from a Lua value; take a *vm.Value to receive a function")
		}
		return nil
	}
	return fmt.Errorf("%s can't be converted to or from a Lua value", t)
}