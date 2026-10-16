package tests

import (
	"errors"
	"strings"
	"testing"

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

func TestSyntaxErrorReportedByLoad(t *testing.T) {
	L := newState(t)
	bc, err := luau.Compile(`local = =`)
	if err != nil {
		t.Fatalf("Compile should not fail on syntax errors: %v", err)
	}
	err = L.Load(bc, "=mychunk")
	if err == nil || !strings.Contains(err.Error(), "mychunk") ||
		!strings.Contains(err.Error(), "Expected identifier") {
		t.Errorf("got %v, want a parse error mentioning the chunk name", err)
	}
	if L.Top() != 0 {
		t.Errorf("stack not clean after failed Load: %d", L.Top())
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
