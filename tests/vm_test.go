package tests

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Leoj030/go-luau/luau"
	"github.com/Leoj030/go-luau/vm"
)

func newVM(t *testing.T, opts ...vm.Option) *vm.VM {
	t.Helper()
	v, err := vm.New(opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(v.Close)
	return v
}

func vmGlobal(t *testing.T, v *vm.VM, name string) any {
	t.Helper()
	x, err := v.Get(name)
	if err != nil {
		t.Fatal(err)
	}
	got, err := x.Any()
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestVMGlobalsAndDo(t *testing.T) {
	v := newVM(t)
	if err := v.Set("name", "ceilo"); err != nil {
		t.Fatal(err)
	}
	if err := v.Set("config", map[string]any{"port": 8080, "tags": []string{"a"}}); err != nil {
		t.Fatal(err)
	}
	if err := v.Do(`result = name .. ":" .. config.port .. config.tags[1]`); err != nil {
		t.Fatal(err)
	}
	if got := vmGlobal(t, v, "result"); got != "ceilo:8080a" {
		t.Errorf("got %v, want ceilo:8080a", got)
	}
}

func TestVMEval(t *testing.T) {
	v := newVM(t)
	res, err := v.Eval(`return 1 + 2, "x", {1, 2}`)
	if err != nil || len(res) != 3 {
		t.Fatalf("got %v, %v", res, err)
	}
	a, _ := res[0].Any()
	b, _ := res[1].Any()
	c, _ := res[2].Any()
	if a != 3.0 || b != "x" || !reflect.DeepEqual(c, []any{1.0, 2.0}) {
		t.Errorf("got %v, %v, %v", a, b, c)
	}
	if res[2].Type() != "table" || res[1].String() != "x" {
		t.Errorf("Type() = %q, String() = %q", res[2].Type(), res[1].String())
	}
}

func TestVMGoFunctions(t *testing.T) {
	v := newVM(t)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(v.Set("add", func(a, b float64) float64 { return a + b }))
	must(v.Set("greet", func(name string, times *int) string {
		n := 1
		if times != nil {
			n = *times
		}
		return strings.Repeat(name, n)
	}))
	must(v.Set("sum", func(nums ...float64) float64 {
		total := 0.0
		for _, n := range nums {
			total += n
		}
		return total
	}))
	must(v.Set("fail", func(msg string) (string, error) { return "", errors.New(msg) }))
	must(v.Set("pair", func() (string, int) { return "a", 1 }))

	must(v.Do(`
		r1 = add(2, 3)
		r2 = greet("x")
		r3 = greet("y", 3)
		r4 = sum(1, 2, 3)
		local a, b = pair()
		r5 = a .. b
		ok, err = pcall(fail, "bad")`))

	for name, want := range map[string]any{"r1": 5.0, "r2": "x", "r3": "yyy", "r4": 6.0, "r5": "a1", "ok": false} {
		if got := vmGlobal(t, v, name); got != want {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
	if got := vmGlobal(t, v, "err"); !strings.Contains(got.(string), "bad") {
		t.Errorf("err = %v, want it to contain bad", got)
	}
}

func TestVMBadArgumentsAreReported(t *testing.T) {
	v := newVM(t)
	v.Set("half", func(n int) int { return n / 2 })
	for _, c := range []struct{ src, want string }{
		{`half(1.5)`, "invalid argument #1 to 'half' (1.5 isn't a whole number"},
		{`half("x")`, "invalid argument #1 to 'half' (number expected, got string)"},
		{`half()`, "missing argument #1 to 'half' (number expected)"},
	} {
		err := v.Do(c.src)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want it to contain %q", c.src, err, c.want)
		}
	}
}

func TestVMArgumentErrorsUseLuauWording(t *testing.T) {
	v := newVM(t)
	if err := v.Set("add", func(a float64, b bool) float64 { return a + 2 }); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ src, want string }{
		{`add(2, 3)`, "invalid argument #2 to 'add' (boolean expected, got number)"},
		{`add(2)`, "missing argument #2 to 'add' (boolean expected)"},
		{`add(nil, true)`, "invalid argument #1 to 'add' (number expected, got nil)"},
		{`add({}, true)`, "invalid argument #1 to 'add' (number expected, got table)"},
	} {
		err := v.Do(c.src)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want it to contain %q", c.src, err, c.want)
		}
	}
	if err := v.Do(`print(add(1, true))`); err != nil {
		t.Errorf("a correct call failed: %v", err)
	}
}

func TestVMSetRejectsSignaturesThatCannotWork(t *testing.T) {
	v := newVM(t)
	for name, fn := range map[string]any{
		"channel parameter": func(c chan int) {},
		"func parameter":    func(f func()) {},
		"interface":         func(r io.Reader) {},
		"map key":           func(m map[[2]int]string) {},
		"struct field":      func(s struct{ C chan int }) {},
		"complex result":    func() complex128 { return 0 },
	} {
		err := v.Set("f", fn)
		if err == nil || !strings.Contains(err.Error(), "f") {
			t.Errorf("%s: Set returned %v, want an error", name, err)
		}
	}

	err := v.Set("f", func(c chan int) {})
	if err == nil || !strings.Contains(err.Error(), "parameter 1") {
		t.Errorf("got %v, want it to name parameter 1", err)
	}

	// Signatures that do work are accepted.
	err = v.Set("g", func(a any, b *vm.Value, c []string, d map[string]int, e struct{ X int }, f ...float64) (int, error) {
		return 0, nil
	})
	if err != nil {
		t.Errorf("a valid signature was rejected: %v", err)
	}
}

func TestVMCallFunctionsFromGo(t *testing.T) {
	v := newVM(t)
	res, err := v.Eval(`return function(a, b) return a * b, "done" end`)
	if err != nil {
		t.Fatal(err)
	}
	out, err := res[0].Call(6, 7)
	if err != nil || len(out) != 2 {
		t.Fatalf("got %v, %v", out, err)
	}
	if n, _ := out[0].Any(); n != 42.0 {
		t.Errorf("got %v, want 42", n)
	}
	if s, _ := out[1].Any(); s != "done" {
		t.Errorf("got %v, want done", s)
	}

	res, _ = v.Eval(`return function() error("boom") end`)
	_, err = res[0].Call()
	var le *vm.Error
	if !errors.As(err, &le) || le.Kind != luau.KindRuntime || le.Message != "boom" {
		t.Errorf("got %v, want a runtime error saying boom", err)
	}
	if _, err := out[0].Call(); err == nil {
		t.Error("calling a number should fail")
	}
}

func TestVMTableValues(t *testing.T) {
	v := newVM(t)
	res, err := v.Eval(`return {a = 1, list = {10, 20}, f = function() return "hi" end}`)
	if err != nil {
		t.Fatal(err)
	}
	tbl := res[0]

	a, _ := tbl.Get("a")
	if got, _ := a.Any(); got != 1.0 {
		t.Errorf("a = %v, want 1", got)
	}
	list, _ := tbl.Get("list")
	if got, _ := list.Any(); !reflect.DeepEqual(got, []any{10.0, 20.0}) {
		t.Errorf("list = %v", got)
	}
	if n, err := list.Len(); err != nil || n != 2 {
		t.Errorf("Len = %d, %v; want 2", n, err)
	}
	f, _ := tbl.Get("f")
	out, err := f.Call()
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := out[0].Any(); got != "hi" {
		t.Errorf("f() = %v, want hi", got)
	}

	if err := tbl.Set("b", "new"); err != nil {
		t.Fatal(err)
	}
	v.Set("tbl", tbl)
	v.Do(`seen = tbl.b`)
	if got := vmGlobal(t, v, "seen"); got != "new" {
		t.Errorf("got %v, want new", got)
	}
}

func TestVMModulesAndRequire(t *testing.T) {
	v := newVM(t)
	if err := v.EnableRequire(fstest.MapFS{
		"main.luau": &fstest.MapFile{Data: []byte(`result = require("./lib").answer`)},
		"lib.luau":  &fstest.MapFile{Data: []byte(`return {answer = 42}`)},
	}); err != nil {
		t.Fatal(err)
	}
	err := v.Module("@std/math2", map[string]any{
		"double": func(x float64) float64 { return x * 2 },
		"pi":     3.14,
		"nested": map[string]any{"triple": func(x float64) float64 { return x * 3 }},
	})
	if err != nil {
		t.Fatal(err)
	}

	res, err := v.Eval(`local m = require("@std/math2") return m.double(21), m.pi, m.nested.triple(2)`)
	if err != nil || len(res) != 3 {
		t.Fatalf("got %v, %v", res, err)
	}
	for i, want := range []any{42.0, 3.14, 6.0} {
		if got, _ := res[i].Any(); got != want {
			t.Errorf("result %d = %v, want %v", i, got, want)
		}
	}

	if err := v.DoFile("main.luau"); err != nil {
		t.Fatal(err)
	}
	if got := vmGlobal(t, v, "result"); got != 42.0 {
		t.Errorf("got %v, want 42", got)
	}
}

func TestVMCallbacksReceiveFunctions(t *testing.T) {
	v := newVM(t)
	var arg *vm.Value
	v.Set("apply", func(f *vm.Value, x float64) (float64, error) {
		arg = f
		out, err := f.Call(x) // runs on the thread that called apply
		if err != nil {
			return 0, err
		}
		var r float64
		return r, out[0].Decode(&r)
	})
	if err := v.Do(`result = apply(function(n) return n * 10 end, 4)`); err != nil {
		t.Fatal(err)
	}
	if got := vmGlobal(t, v, "result"); got != 40.0 {
		t.Errorf("got %v, want 40", got)
	}
	// A Value passed to a Go function is released when it returns.
	if _, err := arg.Call(1); err == nil {
		t.Error("the argument should have been released")
	}
}

func TestVMRetainKeepsAValue(t *testing.T) {
	v := newVM(t)
	var saved *vm.Value
	v.Set("keep", func(f *vm.Value) error {
		var err error
		saved, err = f.Retain()
		return err
	})
	if err := v.Do(`keep(function() return "later" end)`); err != nil {
		t.Fatal(err)
	}
	out, err := saved.Call()
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := out[0].Any(); got != "later" {
		t.Errorf("got %v, want later", got)
	}
	saved.Release()
	saved.Release() // twice is fine
	if _, err := saved.Call(); err == nil {
		t.Error("a released value should not be callable")
	}
}

func TestVMErrors(t *testing.T) {
	v := newVM(t)

	err := v.Do("x = 1\nlocal = =")
	var le *vm.Error
	if !errors.As(err, &le) || le.Kind != luau.KindSyntax || le.Line != 2 || le.Column < 1 || le.Chunk != "script" {
		t.Errorf("syntax error: got %v (%+v)", err, le)
	}

	err = v.Do("local function f()\n\terror(\"boom\")\nend\nf()")
	le = nil
	if !errors.As(err, &le) || le.Kind != luau.KindRuntime || le.Message != "boom" || le.Line != 2 || le.Traceback == "" {
		t.Errorf("runtime error: got %v (%+v)", err, le)
	}
}

func TestVMContextStopsAScript(t *testing.T) {
	v := newVM(t)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := v.DoContext(ctx, `while true do end`)
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, vm.ErrInterrupted) {
		t.Fatalf("got %v, want it to match DeadlineExceeded and ErrInterrupted", err)
	}
	if err := v.Do(`x = 1`); err != nil {
		t.Errorf("the VM is unusable afterwards: %v", err)
	}
}

func TestVMMemoryLimit(t *testing.T) {
	v := newVM(t, vm.WithMemoryLimit(8<<20))
	err := v.Do(`local t = {} for i = 1, 1e9 do t[i] = i end`)
	if !errors.Is(err, vm.ErrMemory) {
		t.Fatalf("got %v, want ErrMemory", err)
	}
	v.GC()
	if err := v.Do(`y = 1`); err != nil {
		t.Errorf("the VM is unusable after hitting the limit: %v", err)
	}
}

func TestVMSandbox(t *testing.T) {
	v := newVM(t)
	v.Set("a", 1)
	v.Sandbox()

	err := v.Do(`b = 2`)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "readonly") {
		t.Errorf("got %v, want a read-only error", err)
	}
	res, err := v.Eval(`return a`)
	if err != nil || len(res) != 1 {
		t.Fatalf("got %v, %v", res, err)
	}
	if got, _ := res[0].Any(); got != 1.0 {
		t.Errorf("got %v, want 1", got)
	}
}

func TestVMValuesAfterClose(t *testing.T) {
	v, err := vm.New()
	if err != nil {
		t.Fatal(err)
	}
	res, _ := v.Eval(`return "x"`)
	v.Close()

	if _, err := res[0].Any(); !errors.Is(err, vm.ErrClosed) {
		t.Errorf("got %v, want ErrClosed", err)
	}
	res[0].Release() // must not crash
}

func TestVMLowLevelEscapeHatch(t *testing.T) {
	v := newVM(t)
	if v.State().Top() != 0 {
		t.Errorf("the stack should be empty, Top() = %d", v.State().Top())
	}
	// A low-level function mixes in with the high-level ones.
	v.Set("raw", func(c *luau.State) (int, error) {
		return 1, c.Push("raw")
	})
	if err := v.Do(`r = raw()`); err != nil {
		t.Fatal(err)
	}
	if got := vmGlobal(t, v, "r"); got != "raw" {
		t.Errorf("got %v, want raw", got)
	}
}
