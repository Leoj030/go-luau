package vm

import (
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/Leoj030/go-luau/luau"
)

const lockedMessage = "The metatable is locked!"

type typeRegistry map[reflect.Type]*userType

// userType is a Go type registered with RegisterType.
type userType struct {
	name string
	typ  reflect.Type
	meta luau.Ref // the metatable, pinned
}

// Frozen is a map that Set and Module push as a read-only table, so a script
// can't add to it or change it. Module already freezes the module table
// itself. Use Frozen for the tables inside it.
type Frozen map[string]any

// TypeSpec describes how values of a Go type look to scripts.
type TypeSpec struct {
	// Name is what the type is called in error messages, such as "Duration".
	Name string

	// Methods are called with a colon, value:method(...). The value is the
	// first parameter of the Go function. Members can be typed functions,
	// low-level functions, or plain values.
	Methods map[string]any

	// Meta holds the metamethods: "__add", "__sub", "__mul", "__div",
	// "__eq", "__lt", "__le", "__tostring", "__len", "__call" and so on. Each
	// is called with the operands in order, so for "a + b" the Go function
	// gets a and b. Names must start with "__".
	Meta map[string]any
}

// RegisterType makes values of the Go type T appear to scripts as userdata
// with the methods and metamethods in spec. After it, a typed function that
// returns a T pushes one, and a parameter of type T (or *T, which is
// optional) reads one back, with Luau-style errors that name the type.
//
// The userdata holds a copy of the value, so a script can't change the Go
// value. To give scripts a handle to one shared object, register a pointer
// type such as *Counter.
//
// Registered types can only be direct parameters and results. They can't be
// fields of a struct or elements of a slice or map, because those are copied
// as tables, which can't hold userdata. Module members and map values pushed
// with Set or Module can be registered values.
//
// The metatable is locked: getmetatable returns a message instead of it, and
// scripts can't replace it. __index and __type are set for you. Register
// types before running scripts.
func RegisterType[T any](v *VM, spec TypeSpec) error {
	typ := reflect.TypeOf((*T)(nil)).Elem()
	switch {
	case spec.Name == "":
		return errors.New("vm: RegisterType needs a Name")
	case typ.Kind() == reflect.Interface:
		return fmt.Errorf("vm: cannot register the interface type %s: values are matched by their concrete type", typ)
	case v.types[typ] != nil:
		return fmt.Errorf("vm: type %s is already registered", typ)
	}

	ut := &userType{name: spec.Name, typ: typ}
	if v.types == nil {
		v.types = typeRegistry{}
	}
	v.types[typ] = ut // registered first: its own methods mention it
	if err := v.buildMeta(ut, spec); err != nil {
		delete(v.types, typ)
		return err
	}
	return nil
}

func (v *VM) buildMeta(ut *userType, spec TypeSpec) error {
	s := v.cur
	base := s.Top()
	if err := v.fillMeta(s, ut, spec); err != nil {
		s.Pop(s.Top() - base)
		return err
	}
	ref, err := s.Ref(-1)
	s.Pop(s.Top() - base)
	if err != nil {
		return err
	}
	ut.meta = ref
	return nil
}

// fillMeta leaves the finished metatable on top of the stack.
func (v *VM) fillMeta(s *luau.State, ut *userType, spec TypeSpec) error {
	if err := s.NewTable(0, len(spec.Meta)+3); err != nil {
		return err
	}
	for name, fn := range spec.Meta {
		if !strings.HasPrefix(name, "__") {
			return fmt.Errorf("vm: type %s: metamethod %q must start with __", ut.name, name)
		}
		switch name {
		case "__index", "__metatable", "__type":
			return fmt.Errorf("vm: type %s: %s is set by RegisterType", ut.name, name)
		}
		if err := v.push(s, name, fn); err != nil {
			return fmt.Errorf("vm: type %s: %s: %w", ut.name, name, err)
		}
		if err := s.SetField(-2, name); err != nil {
			return err
		}
	}

	if err := s.NewTable(0, len(spec.Methods)); err != nil {
		return err
	}
	for name, fn := range spec.Methods {
		if err := v.push(s, name, fn); err != nil {
			return fmt.Errorf("vm: type %s: method %s: %w", ut.name, name, err)
		}
		if err := s.SetField(-2, name); err != nil {
			return err
		}
	}
	if err := s.Freeze(-1); err != nil {
		return err
	}
	if err := s.SetField(-2, "__index"); err != nil {
		return err
	}

	for field, value := range map[string]string{"__metatable": lockedMessage, "__type": ut.name} {
		if err := s.Push(value); err != nil {
			return err
		}
		if err := s.SetField(-2, field); err != nil {
			return err
		}
	}
	return s.Freeze(-1)
}

// pushUserdata pushes x as userdata with the metatable of its registered type.
func (v *VM) pushUserdata(s *luau.State, ut *userType, x any) error {
	if err := s.PushUserdata(x); err != nil {
		return err
	}
	if err := s.PushRef(ut.meta); err != nil {
		return err
	}
	return s.SetMetatable(-2)
}

// userdataArg reads the argument at idx as a value of the registered type ut.
func (v *VM) userdataArg(c *luau.State, fname string, idx int, missing bool, ut *userType) (reflect.Value, error) {
	if missing {
		return reflect.Value{}, fmt.Errorf("missing argument #%d to '%s' (%s expected)", idx, fname, ut.name)
	}
	have := c.TypeName(idx)
	if have == "userdata" {
		if x, ok := c.ToUserdata(idx); ok {
			rv := reflect.ValueOf(x)
			if rv.IsValid() && rv.Type() == ut.typ {
				return rv, nil
			}
			have = v.nameOf(x) // a different Go type: say which one
		}
	}
	return reflect.Value{}, fmt.Errorf("invalid argument #%d to '%s' (%s expected, got %s)", idx, fname, ut.name, have)
}

func (v *VM) nameOf(x any) string {
	if x != nil {
		if ut := v.types[reflect.TypeOf(x)]; ut != nil {
			return ut.name
		}
	}
	return "userdata"
}

// direct reports whether t may be a direct parameter or result: a *Value, a
// registered type, or a pointer to one.
func (v *VM) direct(t reflect.Type) bool {
	if t == valueType || v.types[t] != nil {
		return true
	}
	return t.Kind() == reflect.Pointer && v.types[t.Elem()] != nil
}

// MustRegisterType is RegisterType that panics on failure. A library's
// constructor can use it, because registration only fails through a mistake
// in the program (registering twice, or a bad spec), not through anything a
// script does.
func MustRegisterType[T any](v *VM, spec TypeSpec) {
	if err := RegisterType[T](v, spec); err != nil {
		panic(err)
	}
}
