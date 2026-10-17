package tests

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Leoj030/go-luau/luau"
)

func newState(t *testing.T) *luau.State {
	t.Helper()
	L, err := luau.NewState()
	if err != nil {
		t.Fatalf("NewState: %v", err)
	}
	t.Cleanup(L.Close)
	return L
}

func TestDoString(t *testing.T) {
	L := newState(t)

	if err := L.DoString(`x = 1 + 2`); err != nil {
		t.Fatalf("valid script failed: %v", err)
	}

	err := L.DoString(`error("boom")`)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("runtime error: got %v, want error containing %q", err, "boom")
	}

	err = L.DoString(`local = =`)
	if err == nil || !strings.Contains(err.Error(), "Expected identifier") {
		t.Errorf("syntax error: got %v, want parse error", err)
	}

	// The state must still be usable after errors.
	if err := L.DoString(`y = 1`); err != nil {
		t.Errorf("state unusable after errors: %v", err)
	}
}

func TestGlobalsRoundTrip(t *testing.T) {
	L := newState(t)

	cases := []struct {
		name string
		in   any
		want any
	}{
		{"nil", nil, nil},
		{"true", true, true},
		{"false", false, false},
		{"float", 3.5, 3.5},
		{"int", 21, float64(21)}, // Luau numbers are doubles
		{"string", "héllo", "héllo"},
		{"empty string", "", ""},
		{"embedded NUL", "a\x00b", "a\x00b"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := L.SetGlobal("v", c.in); err != nil {
				t.Fatalf("SetGlobal: %v", err)
			}
			got, err := L.GetGlobal("v")
			if err != nil {
				t.Fatalf("GetGlobal: %v", err)
			}
			if got != c.want {
				t.Errorf("got %#v, want %#v", got, c.want)
			}
		})
	}
}

func TestGlobalsFromScript(t *testing.T) {
	L := newState(t)
	L.SetGlobal("name", "Go")
	L.SetGlobal("n", 21)

	if err := L.DoString(`result = "hello " .. name .. ", " .. n * 2`); err != nil {
		t.Fatal(err)
	}
	got, _ := L.GetGlobal("result")
	if got != "hello Go, 42" {
		t.Errorf("got %v, want %q", got, "hello Go, 42")
	}
}

func TestMissingGlobalIsNil(t *testing.T) {
	L := newState(t)
	got, err := L.GetGlobal("doesNotExist")
	if err != nil || got != nil {
		t.Errorf("got (%v, %v), want (nil, nil)", got, err)
	}
}

func TestStackIsBalanced(t *testing.T) {
	L := newState(t)
	L.SetGlobal("a", 1)
	L.GetGlobal("a")
	L.DoString(`error("x")`)
	if n := L.Top(); n != 0 {
		t.Errorf("stack has %d leftover values, want 0", n)
	}
}

func TestUnsupportedTypes(t *testing.T) {
	L := newState(t)

	if err := L.Push(struct{}{}); err == nil {
		t.Error("Push(struct) should fail")
	}

	L.DoString(`t = {}`)
	if _, err := L.GetGlobal("t"); err == nil {
		t.Error("GetGlobal of a table should fail until tables are supported")
	}
}

func TestRegisterGoFunction(t *testing.T) {
	L := newState(t)
	L.Register("add", func(c *luau.State) (int, error) {
		a, _ := c.Get(1)
		b, _ := c.Get(2)
		x, ok1 := a.(float64)
		y, ok2 := b.(float64)
		if !ok1 || !ok2 {
			return 0, errors.New("add: expected two numbers")
		}
		c.Push(x + y)
		return 1, nil
	})

	if err := L.DoString(`result = add(2, 3)`); err != nil {
		t.Fatal(err)
	}
	if got, _ := L.GetGlobal("result"); got != 5.0 {
		t.Errorf("add(2, 3) = %v, want 5", got)
	}
}

func TestGoErrorBecomesLuauError(t *testing.T) {
	L := newState(t)
	L.Register("fail", func(c *luau.State) (int, error) {
		return 0, errors.New("fail: nope")
	})

	// Surfaces as a Go error from DoString...
	err := L.DoString(`fail()`)
	if err == nil || !strings.Contains(err.Error(), "fail: nope") {
		t.Errorf("got %v, want error containing %q", err, "fail: nope")
	}

	// ...and is catchable by pcall inside the script.
	if err := L.DoString(`ok, msg = pcall(fail)`); err != nil {
		t.Fatal(err)
	}
	if ok, _ := L.GetGlobal("ok"); ok != false {
		t.Errorf("pcall ok = %v, want false", ok)
	}
	if msg, _ := L.GetGlobal("msg"); !strings.Contains(msg.(string), "fail: nope") {
		t.Errorf("pcall msg = %v", msg)
	}
}

func TestGoPanicBecomesLuauError(t *testing.T) {
	L := newState(t)
	L.Register("panicky", func(c *luau.State) (int, error) { panic("oops") })

	err := L.DoString(`panicky()`)
	if err == nil || !strings.Contains(err.Error(), "go panic: oops") {
		t.Errorf("got %v, want error containing %q", err, "go panic: oops")
	}

	// The process survived, and the state is still usable.
	if err := L.DoString(`z = 1`); err != nil {
		t.Errorf("state unusable after panic: %v", err)
	}
}

func TestCompileLoadCall(t *testing.T) {
	L := newState(t)

	bc, err := luau.Compile(`return 1 + 2, "x", true`)
	if err != nil {
		t.Fatal(err)
	}
	if err := L.Load(bc, "=test"); err != nil {
		t.Fatal(err)
	}
	res, err := L.Call()
	if err != nil {
		t.Fatal(err)
	}
	want := []any{3.0, "x", true}
	if len(res) != len(want) {
		t.Fatalf("got %v, want %v", res, want)
	}
	for i := range want {
		if res[i] != want[i] {
			t.Errorf("result %d: got %#v, want %#v", i, res[i], want[i])
		}
	}
	if L.Top() != 0 {
		t.Errorf("stack not clean: %d", L.Top())
	}
}

func TestCallWithArgsAndBytecodeReuse(t *testing.T) {
	L := newState(t)
	bc, _ := luau.Compile(`local a, b = ... return a + b`)

	// Compile once, run many times with different arguments.
	for _, c := range []struct{ a, b, want float64 }{{1, 2, 3}, {10, 20, 30}, {-1, 1, 0}} {
		if err := L.Load(bc, "=add"); err != nil {
			t.Fatal(err)
		}
		res, err := L.Call(c.a, c.b)
		if err != nil || len(res) != 1 || res[0] != c.want {
			t.Errorf("add(%v, %v) = %v, %v; want %v", c.a, c.b, res, err, c.want)
		}
	}

	// The same bytecode also loads into a different state.
	L2 := newState(t)
	L2.Load(bc, "=add")
	if res, _ := L2.Call(5, 6); len(res) != 1 || res[0] != 11.0 {
		t.Errorf("second state: got %v", res)
	}
}

func TestCallErrorsLeaveStackClean(t *testing.T) {
	L := newState(t)

	bc, _ := luau.Compile(`error("kaboom")`)
	L.Load(bc, "=boom")
	if _, err := L.Call(); err == nil || !strings.Contains(err.Error(), "kaboom") {
		t.Errorf("got %v, want runtime error", err)
	}
	if L.Top() != 0 {
		t.Errorf("stack not clean after error: %d", L.Top())
	}

	// A bad argument type is reported and the function is consumed.
	bc, _ = luau.Compile(`return 1`)
	L.Load(bc, "=x")
	if _, err := L.Call(struct{}{}); err == nil {
		t.Error("expected an error for an unsupported argument")
	}
	if L.Top() != 0 {
		t.Errorf("stack not clean after bad arg: %d", L.Top())
	}

	// A result of an unsupported type (a table) is an error, not a crash.
	bc, _ = luau.Compile(`return {}`)
	L.Load(bc, "=t")
	if _, err := L.Call(); err == nil {
		t.Error("expected an error for a table result")
	}
	if L.Top() != 0 {
		t.Errorf("stack not clean after table result: %d", L.Top())
	}
}

func TestCallEdgeCases(t *testing.T) {
	L := newState(t)
	if _, err := L.Call(); err == nil {
		t.Error("Call with an empty stack should fail")
	}
	if err := L.Load(nil, "=x"); err == nil {
		t.Error("Load of empty bytecode should fail")
	}
}

func TestCallLuauFromGoFunction(t *testing.T) {
	L := newState(t)
	bc, _ := luau.Compile(`return (...) * 2`)

	// A Go function that calls back into Luau, using the per-call state.
	L.Register("twice", func(c *luau.State) (int, error) {
		x, _ := c.Get(1)
		if err := c.Load(bc, "=twice"); err != nil {
			return 0, err
		}
		res, err := c.Call(x)
		if err != nil {
			return 0, err
		}
		c.Push(res[0])
		return 1, nil
	})

	if err := L.DoString(`result = twice(21)`); err != nil {
		t.Fatal(err)
	}
	if got, _ := L.GetGlobal("result"); got != 42.0 {
		t.Errorf("got %v, want 42", got)
	}
}

func TestBuildTableInGo(t *testing.T) {
	L := newState(t)
	if err := L.NewTable(0, 2); err != nil {
		t.Fatal(err)
	}
	L.Push("Go")
	L.SetField(-2, "name")
	L.Push(21)
	L.SetField(-2, "n")
	if err := L.PopGlobal("cfg"); err != nil {
		t.Fatal(err)
	}
	if L.Top() != 0 {
		t.Errorf("stack not clean: %d", L.Top())
	}

	if err := L.DoString(`result = cfg.name .. ":" .. cfg.n * 2`); err != nil {
		t.Fatal(err)
	}
	if got, _ := L.GetGlobal("result"); got != "Go:42" {
		t.Errorf("got %v, want Go:42", got)
	}
}

func TestReadTableInGo(t *testing.T) {
	L := newState(t)
	L.DoString(`t = {a = 1, list = {10, 20, 30}}`)
	L.PushGlobal("t")

	L.GetField(-1, "a")
	if v, _ := L.Get(-1); v != 1.0 {
		t.Errorf("t.a = %v, want 1", v)
	}
	L.Pop(1)

	L.GetField(-1, "missing")
	if v, _ := L.Get(-1); v != nil {
		t.Errorf("t.missing = %v, want nil", v)
	}
	L.Pop(1)

	L.GetField(-1, "list")
	if n, _ := L.Len(-1); n != 3 {
		t.Errorf("len = %d, want 3", n)
	}
	L.GetIndex(-1, 2)
	if v, _ := L.Get(-1); v != 20.0 {
		t.Errorf("list[2] = %v, want 20", v)
	}
	L.Pop(3) // 20, list, t
	if L.Top() != 0 {
		t.Errorf("stack not clean: %d", L.Top())
	}
}

func TestArrayInGo(t *testing.T) {
	L := newState(t)
	L.NewTable(3, 0)
	for i, v := range []string{"x", "y", "z"} {
		L.Push(v)
		L.SetIndex(-2, i+1)
	}
	L.PopGlobal("arr")
	L.DoString(`joined = table.concat(arr, ",")`)
	if got, _ := L.GetGlobal("joined"); got != "x,y,z" {
		t.Errorf("got %v, want x,y,z", got)
	}
}

func TestTableOpsRejectBadInput(t *testing.T) {
	L := newState(t)
	L.Push(5)
	if err := L.GetField(-1, "x"); err == nil {
		t.Error("GetField on a number should fail")
	}
	L.Push(1)
	if err := L.SetField(-2, "x"); err == nil {
		t.Error("SetField on a number should fail")
	}
	if _, err := L.Len(-1); err == nil {
		t.Error("Len on a number should fail")
	}
	if err := L.GetIndex(99, 1); err == nil {
		t.Error("GetIndex on an invalid index should fail")
	}
	if err := L.Pop(99); err == nil {
		t.Error("Pop past the bottom should fail")
	}
}

func TestForEach(t *testing.T) {
	L := newState(t)
	L.DoString(`t = {a = 1, b = 2, c = 3}`)
	L.PushGlobal("t")

	sum, count := 0.0, 0
	err := L.ForEach(-1, func() error {
		k, _ := L.Get(-2)
		v, _ := L.Get(-1)
		if _, ok := k.(string); !ok {
			return errors.New("key is not a string")
		}
		sum += v.(float64)
		count++
		return nil
	})
	if err != nil || sum != 6 || count != 3 {
		t.Errorf("sum=%v count=%d err=%v; want 6, 3, nil", sum, count, err)
	}
	if L.Top() != 1 {
		t.Errorf("stack height %d after ForEach, want 1 (just the table)", L.Top())
	}

	// An error from the callback stops iteration and restores the stack.
	stop := errors.New("stop")
	if err := L.ForEach(-1, func() error { return stop }); err != stop {
		t.Errorf("got %v, want the callback's error", err)
	}
	if L.Top() != 1 {
		t.Errorf("stack height %d after early exit, want 1", L.Top())
	}
	L.Pop(1)
}

func TestRefsKeepValuesAlive(t *testing.T) {
	L := newState(t)
	L.DoString(`function greet(name) return "hi " .. name end`)
	L.PushGlobal("greet")
	ref, err := L.Ref(-1)
	if err != nil {
		t.Fatal(err)
	}
	L.Pop(1)

	if err := L.DoString(`greet = nil`); err != nil {
		t.Fatal(err)
	}
	L.GC()

	// The same function can be called repeatedly.
	for _, name := range []string{"A", "B"} {
		if err := L.PushRef(ref); err != nil {
			t.Fatal(err)
		}
		res, err := L.Call(name)
		if err != nil || len(res) != 1 || res[0] != "hi "+name {
			t.Errorf("greet(%q) = %v, %v", name, res, err)
		}
	}
	L.Unref(ref)
}

func TestRefOfNilAndInvalid(t *testing.T) {
	L := newState(t)
	L.Push(nil)
	ref, err := L.Ref(-1)
	if err != nil {
		t.Fatal(err)
	}
	L.Pop(1)
	L.PushRef(ref)
	if v, _ := L.Get(-1); v != nil {
		t.Errorf("got %v, want nil", v)
	}
	L.Pop(1)
	L.Unref(ref)

	if _, err := L.Ref(5); err == nil {
		t.Error("Ref of an invalid index should fail")
	}
}

type counter struct{ n int }

type tracked struct{ finalized *int }

func (t *tracked) Finalize() { *t.finalized++ }

func TestUserdataRoundTrip(t *testing.T) {
	L := newState(t)
	c := &counter{n: 7}
	L.PushUserdata(c)
	if err := L.PopGlobal("obj"); err != nil {
		t.Fatal(err)
	}

	L.DoString(`kind = type(obj)`)
	if got, _ := L.GetGlobal("kind"); got != "userdata" {
		t.Errorf("type(obj) = %v, want userdata", got)
	}

	// A Go function receives the same object it was given.
	L.Register("count", func(s *luau.State) (int, error) {
		v, ok := s.ToUserdata(1)
		if !ok {
			return 0, errors.New("count: expected a userdata")
		}
		s.Push(v.(*counter).n)
		return 1, nil
	})
	L.DoString(`n = count(obj)`)
	if got, _ := L.GetGlobal("n"); got != 7.0 {
		t.Errorf("got %v, want 7", got)
	}
	if err := L.DoString(`count(5)`); err == nil {
		t.Error("count(5) should fail: not a userdata")
	}
}

func TestToUserdataRejectsOtherValues(t *testing.T) {
	L := newState(t)
	L.Push(5)
	L.NewTable(0, 0)
	L.Push("s")
	for _, idx := range []int{1, 2, 3, -1, 0, 99, -99} {
		if v, ok := L.ToUserdata(idx); ok {
			t.Errorf("ToUserdata(%d) = %v, true; want false", idx, v)
		}
	}
}

func TestUserdataMethods(t *testing.T) {
	L := newState(t)

	// Build one shared metatable: { __index = { inc = ..., value = ... } }
	L.NewTable(0, 1) // metatable
	L.NewTable(0, 2) // methods
	L.PushFunction("inc", func(s *luau.State) (int, error) {
		v, ok := s.ToUserdata(1)
		if !ok {
			return 0, errors.New("inc: expected a counter")
		}
		v.(*counter).n++
		return 0, nil
	})
	L.SetField(-2, "inc")
	L.PushFunction("value", func(s *luau.State) (int, error) {
		v, _ := s.ToUserdata(1)
		s.Push(v.(*counter).n)
		return 1, nil
	})
	L.SetField(-2, "value")
	L.SetField(-2, "__index")
	meta, err := L.Ref(-1)
	if err != nil {
		t.Fatal(err)
	}
	L.Pop(1)

	c := &counter{}
	L.PushUserdata(c)
	L.PushRef(meta)
	if err := L.SetMetatable(-2); err != nil {
		t.Fatal(err)
	}
	L.PopGlobal("c")

	if err := L.DoString(`c:inc() c:inc() c:inc() result = c:value()`); err != nil {
		t.Fatal(err)
	}
	if got, _ := L.GetGlobal("result"); got != 3.0 {
		t.Errorf("got %v, want 3", got)
	}
	if c.n != 3 {
		t.Errorf("Go object has n=%d, want 3", c.n)
	}
	L.Unref(meta)
}

func TestSetMetatableRejectsBadInput(t *testing.T) {
	L := newState(t)

	L.Push("a string")
	L.NewTable(0, 0)
	if err := L.SetMetatable(-2); err == nil {
		t.Error("setting a metatable on a string must fail (it would affect all strings)")
	}
	L.Pop(2)

	L.NewTable(0, 0)
	L.Push(5)
	if err := L.SetMetatable(-2); err == nil {
		t.Error("a non-table metatable must fail")
	}
	L.Pop(2)

	if err := L.SetMetatable(-1); err == nil {
		t.Error("SetMetatable on an empty stack must fail")
	}
}

func TestUserdataFinalizedByGC(t *testing.T) {
	L := newState(t)
	finalized := 0

	L.PushUserdata(&tracked{&finalized})
	L.PopGlobal("obj")

	L.GC()
	if finalized != 0 {
		t.Fatal("finalized while still reachable")
	}

	L.DoString(`obj = nil`)
	L.GC()
	if finalized != 1 {
		t.Errorf("finalized = %d after dropping and collecting, want 1", finalized)
	}
}

func TestUserdataFinalizedOnClose(t *testing.T) {
	finalized := 0
	L, err := luau.NewState()
	if err != nil {
		t.Fatal(err)
	}
	L.PushUserdata(&tracked{&finalized})
	L.PopGlobal("keep")
	L.Close()
	if finalized != 1 {
		t.Errorf("finalized = %d after Close, want 1", finalized)
	}
}

func TestLibrarySelection(t *testing.T) {
	L, err := luau.NewStateWith(luau.Options{Libs: luau.LibBase | luau.LibMath})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(L.Close)

	if err := L.DoString(`hasMath = math ~= nil  hasString = string ~= nil  hasOS = os ~= nil`); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{"hasMath": true, "hasString": false, "hasOS": false} {
		if got, _ := L.GetGlobal(name); got != want {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
}

func TestNoLibraries(t *testing.T) {
	L, err := luau.NewStateWith(luau.Options{Libs: luau.LibNone})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(L.Close)

	if err := L.DoString(`hasPrint = print ~= nil  hasMath = math ~= nil  x = 1 + 2`); err != nil {
		t.Fatal(err)
	}
	if got, _ := L.GetGlobal("hasPrint"); got != false {
		t.Errorf("print should not exist without the base library, got %v", got)
	}
	if got, _ := L.GetGlobal("hasMath"); got != false {
		t.Errorf("math should not exist, got %v", got)
	}
	if got, _ := L.GetGlobal("x"); got != 3.0 {
		t.Errorf("plain code should still run, got %v", got)
	}
}

// Guards against LibAll drifting from what luaL_openlibs opens.
// Guards against LibDefault drifting from what luaL_openlibs opens.
func TestLibDefaultMatchesDefaultSet(t *testing.T) {
	const dump = `
		local names = {}
		for k, v in pairs(_G) do
			names[#names + 1] = tostring(k)
			if type(v) == "table" then
				for k2 in pairs(v) do names[#names + 1] = tostring(k) .. "." .. tostring(k2) end
			end
		end
		table.sort(names)
		result = table.concat(names, ",")`

	a := newState(t) // zero-value options: the default set
	b, err := luau.NewStateWith(luau.Options{Libs: luau.LibDefault})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)

	if err := a.DoString(dump); err != nil {
		t.Fatal(err)
	}
	if err := b.DoString(dump); err != nil {
		t.Fatal(err)
	}
	ra, _ := a.GetGlobal("result")
	rb, _ := b.GetGlobal("result")
	if ra != rb {
		t.Errorf("LibDefault differs from the default set:\n default:    %v\n LibDefault: %v", ra, rb)
	}
}

func TestOptionalLibraries(t *testing.T) {
	L, err := luau.NewStateWith(luau.Options{Libs: luau.LibAll})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(L.Close)

	if err := L.DoString(`hasClass = class ~= nil  hasInteger = integer ~= nil`); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"hasClass", "hasInteger"} {
		if got, _ := L.GetGlobal(name); got != true {
			t.Errorf("%s = %v, want true", name, got)
		}
	}

	// A default state does not have them.
	d := newState(t)
	d.DoString(`hasClass = class ~= nil  hasInteger = integer ~= nil`)
	for _, name := range []string{"hasClass", "hasInteger"} {
		if got, _ := d.GetGlobal(name); got != false {
			t.Errorf("default state: %s = %v, want false", name, got)
		}
	}
}

func TestOptionsValidation(t *testing.T) {
	if _, err := luau.NewStateWith(luau.Options{MemoryLimit: -1}); err == nil {
		t.Error("a negative MemoryLimit should fail")
	}
}

func TestMemoryUsed(t *testing.T) {
	L := newState(t)
	before := L.MemoryUsed()
	if before <= 0 {
		t.Fatalf("a fresh state should report some memory, got %d", before)
	}
	if err := L.DoString(`big = string.rep("x", 1048576)`); err != nil {
		t.Fatal(err)
	}
	after := L.MemoryUsed()
	if after < before+(1<<20) {
		t.Errorf("used %d -> %d, expected growth of at least 1 MiB", before, after)
	}
	L.DoString(`big = nil`)
	L.GC()
	if L.MemoryUsed() >= after {
		t.Errorf("memory not released after GC: %d -> %d", after, L.MemoryUsed())
	}
}

func TestMemoryLimit(t *testing.T) {
	L, err := luau.NewStateWith(luau.Options{MemoryLimit: 8 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(L.Close)

	err = L.DoString(`local t = {} for i = 1, 1e9 do t[i] = i end`)
	if !errors.Is(err, luau.ErrMemory) {
		t.Fatalf("got %v, want ErrMemory", err)
	}
	if used := L.MemoryUsed(); used > 8<<20 {
		t.Errorf("used %d bytes, above the 8 MiB limit", used)
	}

	// The state is still usable once the garbage is collected.
	L.GC()
	if err := L.DoString(`y = 1`); err != nil {
		t.Errorf("state unusable after hitting the limit: %v", err)
	}
}

func TestMemoryLimitTooSmall(t *testing.T) {
	if _, err := luau.NewStateWith(luau.Options{MemoryLimit: 1024}); err == nil {
		t.Error("a 1 KiB limit can't even hold the libraries, so creation should fail")
	}
}

func TestInterruptStopsInfiniteLoop(t *testing.T) {
	L := newState(t)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	stop := L.WatchContext(ctx)
	start := time.Now()
	err := L.DoString(`while true do end`)
	stop()

	if !errors.Is(err, luau.ErrInterrupted) {
		t.Fatalf("got %v, want ErrInterrupted", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("took %v to stop", time.Since(start))
	}

	// After stop(), the state runs normally again.
	if err := L.DoString(`x = 1`); err != nil {
		t.Errorf("state unusable after an interrupt: %v", err)
	}
}

func TestInterruptFromAnotherGoroutine(t *testing.T) {
	L := newState(t)
	go func() {
		time.Sleep(30 * time.Millisecond)
		L.Interrupt()
	}()
	err := L.DoString(`local n = 0 while true do n = n + 1 end`)
	if !errors.Is(err, luau.ErrInterrupted) {
		t.Fatalf("got %v, want ErrInterrupted", err)
	}
	L.ClearInterrupt()
	if err := L.DoString(`x = 1`); err != nil {
		t.Errorf("state unusable after ClearInterrupt: %v", err)
	}
}

func TestWatchContextAlreadyCancelled(t *testing.T) {
	L := newState(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stop := L.WatchContext(ctx)
	defer stop()
	if err := L.DoString(`while true do end`); !errors.Is(err, luau.ErrInterrupted) {
		t.Errorf("got %v, want ErrInterrupted", err)
	}
}

func TestSandbox(t *testing.T) {
	L := newState(t)
	L.SetGlobal("hostValue", 1) // set up before sandboxing
	L.Sandbox()

	for _, src := range []string{`x = 1`, `math.floor = nil`, `string.upper = print`} {
		err := L.DoString(src)
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "readonly") {
			t.Errorf("%q: got %v, want a read-only error", src, err)
		}
	}

	// Reading is unaffected. (Results come back through Call, since scripts
	// can no longer assign globals.)
	bc, _ := luau.Compile(`return math.floor(hostValue + 1.5)`)
	L.Load(bc, "=read")
	res, err := L.Call()
	if err != nil || len(res) != 1 || res[0] != 2.0 {
		t.Errorf("read after sandbox: %v, %v", res, err)
	}

	// The host is locked out of the globals table too.
	if err := L.SetGlobal("late", 1); err == nil {
		t.Error("SetGlobal after Sandbox should fail")
	}
	if L.Top() != 0 {
		t.Errorf("stack not clean: %d", L.Top())
	}
}

func sameValues(got []any, want ...any) bool {
	if len(got) == 0 && len(want) == 0 {
		return true
	}
	return reflect.DeepEqual(got, want)
}

func TestThreadYieldsFromLua(t *testing.T) {
	L := newState(t)
	th, err := L.NewThread()
	if err != nil {
		t.Fatal(err)
	}
	defer th.Close()

	bc, _ := luau.Compile(`
		local a = ...
		local b = coroutine.yield(a + 1)
		local c = coroutine.yield(b * 2)
		return "done", c`)
	if err := th.Load(bc, "=co"); err != nil {
		t.Fatal(err)
	}
	if th.Status() != luau.ThreadSuspended {
		t.Errorf("status = %v, want suspended", th.Status())
	}

	res, done, err := th.Resume(10)
	if err != nil || done || !sameValues(res, 11.0) {
		t.Fatalf("resume 1: %v, %v, %v", res, done, err)
	}
	res, done, err = th.Resume(5)
	if err != nil || done || !sameValues(res, 10.0) {
		t.Fatalf("resume 2: %v, %v, %v", res, done, err)
	}
	res, done, err = th.Resume("x")
	if err != nil || !done || !sameValues(res, "done", "x") {
		t.Fatalf("resume 3: %v, %v, %v", res, done, err)
	}
	if th.Status() != luau.ThreadFinished {
		t.Errorf("status = %v, want finished", th.Status())
	}
	if _, _, err := th.Resume(); err == nil {
		t.Error("resuming a finished thread should fail")
	}
}

func TestThreadScriptError(t *testing.T) {
	L := newState(t)
	th, _ := L.NewThread()
	defer th.Close()

	bc, _ := luau.Compile(`error("kaboom")`)
	th.Load(bc, "=co")
	_, _, err := th.Resume()
	if err == nil || !strings.Contains(err.Error(), "kaboom") {
		t.Fatalf("got %v, want the script error", err)
	}
	if th.Status() != luau.ThreadFailed {
		t.Errorf("status = %v, want failed", th.Status())
	}
	if _, _, err := th.Resume(); err == nil {
		t.Error("resuming a failed thread should fail")
	}
}

func TestResumeRejectsBadSetup(t *testing.T) {
	L := newState(t)
	th, _ := L.NewThread()
	defer th.Close()

	if _, _, err := th.Resume(); err == nil {
		t.Error("resuming a thread with nothing loaded should fail")
	}
	th.Push(5) // not a function
	if _, _, err := th.ResumeN(0); err == nil {
		t.Error("resuming without a function should fail")
	}
}

func TestGoFunctionYields(t *testing.T) {
	L := newState(t)
	L.Register("wait", func(c *luau.State) (int, error) {
		return c.Yield(1) // hand the first argument to the resumer
	})

	th, _ := L.NewThread()
	defer th.Close()
	bc, _ := luau.Compile(`local got = wait(7) return got, "end"`)
	th.Load(bc, "=co")

	res, done, err := th.Resume()
	if err != nil || done || !sameValues(res, 7.0) {
		t.Fatalf("first resume: %v, %v, %v", res, done, err)
	}
	// The resume arguments become the results of wait().
	res, done, err = th.Resume("back")
	if err != nil || !done || !sameValues(res, "back", "end") {
		t.Fatalf("second resume: %v, %v, %v", res, done, err)
	}
}

func TestYieldOutsideCoroutineFailsCleanly(t *testing.T) {
	L := newState(t)
	L.Register("wait", func(c *luau.State) (int, error) { return c.Yield(0) })

	err := L.DoString(`wait()`)
	if err == nil || !strings.Contains(err.Error(), "yield") {
		t.Errorf("got %v, want a yield error", err)
	}
	if err := L.DoString(`x = 1`); err != nil {
		t.Errorf("state unusable afterwards: %v", err)
	}
}

func TestYieldMisuse(t *testing.T) {
	L := newState(t)
	if _, err := L.Yield(0); err == nil {
		t.Error("Yield outside a Go function should fail")
	}

	L.Register("bad", func(c *luau.State) (int, error) {
		c.Push(1)
		c.Yield(1)
		return 0, nil // says yield 1 value, returns 0
	})
	err := L.DoString(`bad()`)
	if err == nil || !strings.Contains(err.Error(), "yielded") {
		t.Errorf("got %v, want a mismatch error", err)
	}
}

func TestResumeNReadsTables(t *testing.T) {
	L := newState(t)
	th, _ := L.NewThread()
	defer th.Close()

	bc, _ := luau.Compile(`coroutine.yield({answer = 42}) return 1`)
	th.Load(bc, "=co")

	n, done, err := th.ResumeN(0)
	if err != nil || done || n != 1 {
		t.Fatalf("got %d, %v, %v", n, done, err)
	}
	if err := th.GetField(1, "answer"); err != nil {
		t.Fatal(err)
	}
	if v, _ := th.Get(-1); v != 42.0 {
		t.Errorf("answer = %v, want 42", v)
	}
	th.Pop(th.Top()) // clear the stack before resuming

	n, done, err = th.ResumeN(0)
	if err != nil || !done || n != 1 {
		t.Fatalf("got %d, %v, %v", n, done, err)
	}
	if v, _ := th.Get(1); v != 1.0 {
		t.Errorf("result = %v, want 1", v)
	}
	th.Pop(1)
}

func TestThreadSurvivesGC(t *testing.T) {
	L := newState(t)
	th, _ := L.NewThread()
	defer th.Close()

	bc, _ := luau.Compile(`coroutine.yield(1) return 2`)
	th.Load(bc, "=co")
	if _, done, err := th.Resume(); err != nil || done {
		t.Fatal("first resume failed")
	}

	L.GC()
	L.GC()
	res, done, err := th.Resume()
	if err != nil || !done || !sameValues(res, 2.0) {
		t.Errorf("after GC: %v, %v, %v", res, done, err)
	}
}

func TestThreadSandboxIsolation(t *testing.T) {
	L := newState(t)
	L.SetGlobal("shared", 10)
	L.Sandbox()

	bc, _ := luau.Compile(`x = (x or 0) + 1  return x, shared`)
	run := func() []any {
		th, err := L.NewThread()
		if err != nil {
			t.Fatal(err)
		}
		defer th.Close()
		th.SandboxThread()
		if err := th.Load(bc, "=iso"); err != nil {
			t.Fatal(err)
		}
		res, done, err := th.Resume()
		if err != nil || !done {
			t.Fatalf("run: %v, %v", done, err)
		}
		return res
	}

	// Each thread sees its own x, and both can read the shared global.
	if r := run(); !sameValues(r, 1.0, 10.0) {
		t.Errorf("first thread: %v", r)
	}
	if r := run(); !sameValues(r, 1.0, 10.0) {
		t.Errorf("second thread: %v (x leaked between threads?)", r)
	}
	if v, _ := L.GetGlobal("x"); v != nil {
		t.Errorf("x leaked into the main state: %v", v)
	}
}

func TestInterruptInsideCoroutine(t *testing.T) {
	L := newState(t)
	th, _ := L.NewThread()
	defer th.Close()

	bc, _ := luau.Compile(`while true do end`)
	th.Load(bc, "=spin")

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	stop := L.WatchContext(ctx)
	_, _, err := th.Resume()
	stop()

	if !errors.Is(err, luau.ErrInterrupted) {
		t.Errorf("got %v, want ErrInterrupted", err)
	}
	if th.Status() != luau.ThreadFailed {
		t.Errorf("status = %v, want failed", th.Status())
	}
}

func TestResumeDepthIsLimited(t *testing.T) {
	L := newState(t)
	bc, _ := luau.Compile(`recurse()`)
	L.Register("recurse", func(c *luau.State) (int, error) {
		th, err := c.NewThread() // creating threads inside a callback works too
		if err != nil {
			return 0, err
		}
		defer th.Close()
		if err := th.Load(bc, "=r"); err != nil {
			return 0, err
		}
		_, _, err = th.Resume()
		return 0, err
	})
	if err := L.DoString(`recurse()`); err == nil {
		t.Error("unbounded nested resumes should hit the depth limit, not crash")
	}
}

func TestSyntaxErrorHasLineAndColumn(t *testing.T) {
	_, err := luau.Compile("local a = 1\nlocal = =")
	var le *luau.Error
	if !errors.As(err, &le) {
		t.Fatalf("got %T (%v), want *luau.Error", err, err)
	}
	if le.Kind != luau.KindSyntax {
		t.Errorf("kind = %v, want syntax", le.Kind)
	}
	if le.Line != 2 {
		t.Errorf("line = %d, want 2", le.Line)
	}
	if le.Column < 1 {
		t.Errorf("column = %d, want a position", le.Column)
	}
	if !strings.Contains(le.Message, "Expected identifier") {
		t.Errorf("message = %q", le.Message)
	}
	if !strings.HasPrefix(err.Error(), "2:") {
		t.Errorf("Error() = %q, want it to start with the position", err.Error())
	}
	t.Logf("syntax error at %d:%d: %s", le.Line, le.Column, le.Message)
}

func TestSyntaxErrorThroughDoString(t *testing.T) {
	L := newState(t)
	err := L.DoString("x = 1\nlocal = =")
	var le *luau.Error
	if !errors.As(err, &le) {
		t.Fatalf("got %T (%v)", err, err)
	}
	if le.Kind != luau.KindSyntax || le.Chunk != "dostring" || le.Line != 2 || le.Column < 1 {
		t.Errorf("got %+v", le)
	}
	if L.Top() != 0 {
		t.Errorf("stack not clean: %d", L.Top())
	}
}

func TestLoadReportsErrorBytecode(t *testing.T) {
	L := newState(t)
	// Luau encodes a failed compile as bytecode: a 0 byte, then ":line: message".
	err := L.Load([]byte("\x00:3: oops"), "=mychunk")
	var le *luau.Error
	if !errors.As(err, &le) {
		t.Fatalf("got %T (%v)", err, err)
	}
	if le.Kind != luau.KindSyntax || le.Chunk != "mychunk" || le.Line != 3 || le.Message != "oops" {
		t.Errorf("got %+v", le)
	}
}

func TestRuntimeErrorFields(t *testing.T) {
	L := newState(t)
	err := L.DoString("local function inner()\n\terror(\"boom\")\nend\n\ninner()")
	var le *luau.Error
	if !errors.As(err, &le) {
		t.Fatalf("got %T (%v)", err, err)
	}
	if le.Kind != luau.KindRuntime || le.Chunk != "dostring" || le.Line != 2 || le.Message != "boom" {
		t.Errorf("got %+v", le)
	}
	if le.Column != 0 {
		t.Errorf("runtime errors have no column, got %d", le.Column)
	}
	if err.Error() != "dostring:2: boom" {
		t.Errorf("Error() = %q", err.Error())
	}
	if !strings.Contains(le.Traceback, "dostring:2") || strings.Count(le.Traceback, "\n") < 2 {
		t.Errorf("traceback looks wrong:\n%s", le.Traceback)
	}
	t.Logf("traceback:\n%s", le.Traceback)
}

func TestErrorWithoutPosition(t *testing.T) {
	L := newState(t)
	err := L.DoString(`error("plain", 0)`)
	var le *luau.Error
	if !errors.As(err, &le) {
		t.Fatalf("got %T (%v)", err, err)
	}
	if le.Line != 0 || le.Chunk != "" || le.Message != "plain" {
		t.Errorf("got %+v", le)
	}
}

func TestGoErrorTextIsNotMisreadAsPosition(t *testing.T) {
	L := newState(t)
	L.Register("fetch", func(c *luau.State) (int, error) {
		return 0, errors.New("fetch: 404: not found")
	})
	err := L.DoString(`fetch()`)
	var le *luau.Error
	if !errors.As(err, &le) {
		t.Fatalf("got %T (%v)", err, err)
	}
	if le.Line != 0 || le.Chunk != "" || le.Message != "fetch: 404: not found" {
		t.Errorf("got %+v", le)
	}
}

func TestNonStringErrorValues(t *testing.T) {
	L := newState(t)

	err := L.DoString(`error({code = 42})`)
	var le *luau.Error
	if !errors.As(err, &le) {
		t.Fatalf("got %T (%v)", err, err)
	}
	if !le.HasRef || le.Message != "error object is a table value" {
		t.Fatalf("got %+v", le)
	}
	if err := L.PushRef(le.Ref); err != nil {
		t.Fatal(err)
	}
	L.GetField(-1, "code")
	if v, _ := L.Get(-1); v != 42.0 {
		t.Errorf("code = %v, want 42", v)
	}
	L.Pop(2)
	L.Unref(le.Ref)

	err = L.DoString(`error(true)`)
	if !errors.As(err, &le) {
		t.Fatalf("got %T (%v)", err, err)
	}
	if le.Value != true || le.Message != "true" || le.HasRef {
		t.Errorf("got %+v", le)
	}
	if L.Top() != 0 {
		t.Errorf("stack not clean: %d", L.Top())
	}
}

func TestThreadErrorHasTraceback(t *testing.T) {
	L := newState(t)
	th, _ := L.NewThread()
	defer th.Close()

	bc, _ := luau.Compile("local function inner()\n\terror(\"deep\")\nend\ninner()")
	th.Load(bc, "=co")
	_, _, err := th.Resume()

	var le *luau.Error
	if !errors.As(err, &le) {
		t.Fatalf("got %T (%v)", err, err)
	}
	if le.Message != "deep" || le.Line != 2 || le.Chunk != "co" {
		t.Errorf("got %+v", le)
	}
	if le.Traceback == "" {
		t.Error("expected a traceback from the failed thread")
	}
	t.Logf("traceback:\n%s", le.Traceback)
}

func TestErrorKinds(t *testing.T) {
	t.Run("memory", func(t *testing.T) {
		L, err := luau.NewStateWith(luau.Options{MemoryLimit: 8 << 20})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(L.Close)

		err = L.DoString(`local t = {} for i = 1, 1e9 do t[i] = i end`)
		var le *luau.Error
		if !errors.As(err, &le) || le.Kind != luau.KindMemory || !errors.Is(err, luau.ErrMemory) {
			t.Errorf("got %v", err)
		}
	})

	t.Run("interrupted", func(t *testing.T) {
		L := newState(t)
		go func() {
			time.Sleep(30 * time.Millisecond)
			L.Interrupt()
		}()
		err := L.DoString(`while true do end`)
		L.ClearInterrupt()

		var le *luau.Error
		if !errors.As(err, &le) || le.Kind != luau.KindInterrupted || !errors.Is(err, luau.ErrInterrupted) {
			t.Errorf("got %v", err)
		}
	})
}
