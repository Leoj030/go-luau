package tests

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Leoj030/go-luau/luau"
	"github.com/Leoj030/go-luau/vm"
)

type vec struct{ x, y float64 }
type other struct{ n int }
type handle struct{ finalized *int }

func (h *handle) Finalize() { *h.finalized++ }

func TestVMCallableModule(t *testing.T) {
	v := newVM(t)
	if err := v.EnableRequire(fstest.MapFS{}); err != nil {
		t.Fatal(err)
	}
	if err := v.Module("@t/call", map[string]any{
		"__call": func(a, b float64) float64 { return a + b },
		"add":    func(a, b float64) float64 { return a + b },
	}); err != nil {
		t.Fatal(err)
	}
	if err := v.Module("@t/low", map[string]any{
		// a low-level __call reports how many arguments it got: the module table is not one of them
		"__call": func(c *luau.State) (int, error) { return 1, c.Push(c.Top()) },
	}); err != nil {
		t.Fatal(err)
	}
	if err := v.Do(`
		local m = require("@t/call")
		r1 = m(2, 3)
		r2 = m.add(2, 3)
		r3 = require("@t/low")("a", "b")
		meta = getmetatable(m)`); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]any{"r1": 5.0, "r2": 5.0, "r3": 2.0, "meta": "The metatable is locked!"} {
		if got := vmGlobal(t, v, name); got != want {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}

	err := v.Do(`require("@t/call")("x", 3)`)
	if err == nil || !strings.Contains(err.Error(), "invalid argument #1 to 'call' (number expected, got string)") {
		t.Errorf("got %v, want a typed argument error without the module table counted", err)
	}
	if err := v.Do(`require("@t/call").extra = 1`); err == nil {
		t.Error("the module table should be read-only")
	}
	if err := v.Module("@t/bad", map[string]any{"__index": func() {}}); err == nil {
		t.Error("__index can't be set on a module")
	}
}

func TestVMPointerTypesFinalizeAndKeepMethods(t *testing.T) {
	v := newVM(t)
	finalized, closed := 0, 0
	vm.MustRegisterType[*handle](v, vm.TypeSpec{
		Name:    "Handle",
		Methods: map[string]any{"close": func(h *handle) { closed++ }},
	})
	v.Set("open", func() *handle { return &handle{&finalized} })

	if err := v.Do(`local h = open() h:close() h:close()`); err != nil {
		t.Fatal(err)
	}
	v.GC()
	v.GC()
	if closed != 2 || finalized != 1 {
		t.Errorf("closed %d times and finalized %d times, want 2 and 1", closed, finalized)
	}
}

func TestVMMustRegisterTypePanicsOnMistakes(t *testing.T) {
	v := newVM(t)
	vm.MustRegisterType[vec](v, vm.TypeSpec{Name: "Vec"})
	defer func() {
		if recover() == nil {
			t.Error("registering twice should panic")
		}
	}()
	vm.MustRegisterType[vec](v, vm.TypeSpec{Name: "Vec"})
}

func newVecVM(t *testing.T) *vm.VM {
	t.Helper()
	v := newVM(t)
	if err := vm.RegisterType[vec](v, vm.TypeSpec{
		Name: "Vec",
		Methods: map[string]any{
			"length": func(a vec) float64 { return math.Hypot(a.x, a.y) },
			"scale":  func(a vec, k float64) vec { return vec{a.x * k, a.y * k} },
			"x":      func(a vec) float64 { return a.x },
		},
		Meta: map[string]any{
			"__add":      func(a, b vec) vec { return vec{a.x + b.x, a.y + b.y} },
			"__eq":       func(a, b vec) bool { return a == b },
			"__tostring": func(a vec) string { return fmt.Sprintf("(%g, %g)", a.x, a.y) },
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := vm.RegisterType[other](v, vm.TypeSpec{Name: "Other"}); err != nil {
		t.Fatal(err)
	}
	v.Set("vec", func(x, y float64) vec { return vec{x, y} })
	v.Set("other", func(n int) other { return other{n} })
	return v
}

func TestVMRegisterType(t *testing.T) {
	v := newVecVM(t)
	if err := v.Do(`
		local a, b = vec(3, 4), vec(1, 2)
		len = a:length()
		sumx = (a + b):x()
		same = a == vec(3, 4)
		differ = a == b
		scaled = a:scale(2):x()
		text = tostring(a)
		kind = type(a)
		meta = getmetatable(a)`); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]any{
		"len": 5.0, "sumx": 4.0, "same": true, "differ": false, "scaled": 6.0,
		"text": "(3, 4)", "kind": "userdata", "meta": "The metatable is locked!",
	} {
		if got := vmGlobal(t, v, name); got != want {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
}

func TestVMRegisteredTypeArgumentErrors(t *testing.T) {
	v := newVecVM(t)
	for _, c := range []struct{ src, want string }{
		{`return vec(1, 2).length(5)`, "invalid argument #1 to 'length' (Vec expected, got number)"},
		{`return vec(1, 2).length()`, "missing argument #1 to 'length' (Vec expected)"},
		{`return vec(1, 2).length(other(1))`, "(Vec expected, got Other)"},
		{`return vec(1, 2) + 5`, "invalid argument #2 to '__add' (Vec expected, got number)"},
	} {
		err := v.Do(c.src)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want it to contain %q", c.src, err, c.want)
		}
	}
}

func TestVMRegisterTypeRules(t *testing.T) {
	v := newVM(t)
	if err := vm.RegisterType[vec](v, vm.TypeSpec{}); err == nil {
		t.Error("a type without a name should fail")
	}
	if err := vm.RegisterType[fmt.Stringer](v, vm.TypeSpec{Name: "S"}); err == nil {
		t.Error("an interface type should fail")
	}
	if err := vm.RegisterType[other](v, vm.TypeSpec{
		Name: "Other",
		Meta: map[string]any{"add": func(a, b other) other { return a }},
	}); err == nil {
		t.Error("a metamethod without the __ prefix should fail")
	}
	// The failed attempt must not leave the type half-registered.
	if err := vm.RegisterType[other](v, vm.TypeSpec{Name: "Other"}); err != nil {
		t.Errorf("registering again after a failure: %v", err)
	}
	if err := vm.RegisterType[other](v, vm.TypeSpec{Name: "Other"}); err == nil {
		t.Error("registering twice should fail")
	}
}

func TestVMRegisteredTypesAreOnlyDirect(t *testing.T) {
	v := newVecVM(t)
	if err := v.Set("a", func() []vec { return nil }); err == nil {
		t.Error("a slice of a registered type should be rejected")
	}
	if err := v.Set("b", func(s struct{ V vec }) {}); err == nil {
		t.Error("a struct field of a registered type should be rejected")
	}
	if err := v.Set("c", func(a *vec, rest ...vec) {}); err != nil {
		t.Errorf("a direct, optional and variadic use was rejected: %v", err)
	}
}

func TestVMOptionalUserdataAndModuleMembers(t *testing.T) {
	v := newVecVM(t)
	if err := v.EnableRequire(fstest.MapFS{}); err != nil {
		t.Fatal(err)
	}
	v.Set("optlen", func(a *vec) float64 {
		if a == nil {
			return -1
		}
		return math.Hypot(a.x, a.y)
	})
	if err := v.Module("@t/m", map[string]any{
		"origin": vec{},
		"frozen": vm.Frozen{"a": 1},
		"open":   map[string]any{"b": 2},
	}); err != nil {
		t.Fatal(err)
	}
	if err := v.Do(`
		local m = require("@t/m")
		r1 = optlen()
		r2 = optlen(vec(3, 4))
		r3 = optlen(nil)
		r4 = m.origin:length()
		m.open.b = 5
		r5 = m.open.b`); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]any{"r1": -1.0, "r2": 5.0, "r3": -1.0, "r4": 0.0, "r5": 5.0} {
		if got := vmGlobal(t, v, name); got != want {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
	err := v.Do(`require("@t/m").frozen.a = 5`)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "readonly") {
		t.Errorf("got %v, want a read-only error for a Frozen table", err)
	}
}
