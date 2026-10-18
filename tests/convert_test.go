package tests

import (
	"math"
	"reflect"
	"testing"

	"github.com/Leoj030/go-luau/luau"
)

type testID int

func TestSlicesBecomeArrays(t *testing.T) {
	L := newState(t)
	if err := L.SetGlobal("xs", []int{10, 20, 30}); err != nil {
		t.Fatal(err)
	}
	if err := L.DoString(`sum = xs[1] + xs[2] + xs[3]  n = #xs`); err != nil {
		t.Fatal(err)
	}
	if got, _ := L.GetGlobal("sum"); got != 60.0 {
		t.Errorf("sum = %v, want 60", got)
	}
	if got, _ := L.GetGlobal("n"); got != 3.0 {
		t.Errorf("n = %v, want 3", got)
	}
}

func TestMapsBecomeTables(t *testing.T) {
	L := newState(t)
	cfg := map[string]any{
		"name":   "Go",
		"n":      21,
		"tags":   []string{"a", "b"},
		"nested": map[string]any{"ok": true},
	}
	if err := L.SetGlobal("cfg", cfg); err != nil {
		t.Fatal(err)
	}
	if err := L.DoString(`r = cfg.name .. cfg.n .. cfg.tags[2] .. tostring(cfg.nested.ok)`); err != nil {
		t.Fatal(err)
	}
	if got, _ := L.GetGlobal("r"); got != "Go21btrue" {
		t.Errorf("got %v, want Go21btrue", got)
	}
}

func TestRoundTrip(t *testing.T) {
	L := newState(t)
	in := map[string]any{
		"name":   "Go",
		"n":      21,
		"tags":   []string{"a", "b"},
		"nested": map[string]any{"ok": true},
	}
	want := map[string]any{
		"name":   "Go",
		"n":      21.0,
		"tags":   []any{"a", "b"},
		"nested": map[string]any{"ok": true},
	}
	if err := L.SetGlobal("v", in); err != nil {
		t.Fatal(err)
	}
	got, err := L.GetGlobal("v")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v\nwant %#v", got, want)
	}
}

func TestReadTablesFromScript(t *testing.T) {
	L := newState(t)
	cases := []struct {
		name string
		src  string
		want any
	}{
		{"array", `v = {1, 2, 3}`, []any{1.0, 2.0, 3.0}},
		{"empty", `v = {}`, []any{}},
		{"object", `v = {a = 1, b = "x"}`, map[string]any{"a": 1.0, "b": "x"}},
		{"mixed", `v = {10, x = 20}`, map[any]any{1.0: 10.0, "x": 20.0}},
		{"sparse", `v = {[1] = "a", [3] = "c"}`, map[any]any{1.0: "a", 3.0: "c"}},
		{"bool key", `v = {[true] = 1}`, map[any]any{true: 1.0}},
		{"nested", `v = {{1}, {a = {2}}}`, []any{[]any{1.0}, map[string]any{"a": []any{2.0}}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := L.DoString(c.src); err != nil {
				t.Fatal(err)
			}
			got, err := L.GetGlobal("v")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("got %#v\nwant %#v", got, c.want)
			}
		})
	}
}

func TestMapIntKeys(t *testing.T) {
	L := newState(t)

	L.Push(map[int]string{1: "a", 2: "b"})
	if got, _ := L.Get(-1); !reflect.DeepEqual(got, []any{"a", "b"}) {
		t.Errorf("keys 1..2 should read back as a list, got %#v", got)
	}
	L.Pop(1)

	L.Push(map[int]string{5: "x"})
	if got, _ := L.Get(-1); !reflect.DeepEqual(got, map[any]any{5.0: "x"}) {
		t.Errorf("sparse keys should read back as map[any]any, got %#v", got)
	}
	L.Pop(1)
}

func TestCallReturnsTables(t *testing.T) {
	L := newState(t)
	bc, _ := luau.Compile(`return {1, 2}, {a = 1}`)
	L.Load(bc, "=t")
	res, err := L.Call()
	if err != nil {
		t.Fatal(err)
	}
	want := []any{[]any{1.0, 2.0}, map[string]any{"a": 1.0}}
	if !reflect.DeepEqual(res, want) {
		t.Errorf("got %#v, want %#v", res, want)
	}
}

func TestBytesBecomeString(t *testing.T) {
	L := newState(t)
	L.Push([]byte("hi"))
	if got, _ := L.Get(-1); got != "hi" {
		t.Errorf("got %#v, want \"hi\"", got)
	}
	L.Pop(1)
}

func TestNamedAndSmallNumberTypes(t *testing.T) {
	L := newState(t)
	for _, v := range []any{testID(7), int32(7), uint8(7), float32(7)} {
		if err := L.Push(v); err != nil {
			t.Fatalf("Push(%T): %v", v, err)
		}
		if got, _ := L.Get(-1); got != 7.0 {
			t.Errorf("%T: got %v, want 7", v, got)
		}
		L.Pop(1)
	}
	L.Push([]testID{1, 2})
	if got, _ := L.Get(-1); !reflect.DeepEqual(got, []any{1.0, 2.0}) {
		t.Errorf("got %#v", got)
	}
	L.Pop(1)
}

func TestFailedPushLeavesStackClean(t *testing.T) {
	L := newState(t)
	if err := L.Push([]any{1, "ok", make(chan int)}); err == nil {
		t.Error("a slice holding a channel should fail")
	}
	if err := L.Push(map[string]func(){"f": nil}); err == nil {
		t.Error("a map holding a func should fail")
	}
	if L.Top() != 0 {
		t.Errorf("stack has %d leftover values, want 0", L.Top())
	}
}

func TestDeepAndCyclicValuesFailCleanly(t *testing.T) {
	L := newState(t)

	var deep any = []any{}
	for i := 0; i < 150; i++ {
		deep = []any{deep}
	}
	if err := L.Push(deep); err == nil {
		t.Error("150 levels of nesting should fail")
	}

	cyclic := map[string]any{}
	cyclic["self"] = cyclic
	if err := L.Push(cyclic); err == nil {
		t.Error("a map containing itself should fail")
	}
	if L.Top() != 0 {
		t.Errorf("stack has %d leftover values, want 0", L.Top())
	}

	// And the other direction: a Luau table that contains itself.
	if err := L.DoString(`t = {} t.self = t`); err != nil {
		t.Fatal(err)
	}
	L.PushGlobal("t")
	before := L.Top()
	if _, err := L.Get(-1); err == nil {
		t.Error("a table containing itself should fail")
	}
	if L.Top() != before {
		t.Errorf("stack changed: %d -> %d", before, L.Top())
	}
}

func TestTablesWithNonScalarKeysFail(t *testing.T) {
	L := newState(t)
	if err := L.DoString(`t = {} t[{}] = 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := L.GetGlobal("t"); err == nil {
		t.Error("a table with a table key should fail")
	}
	if L.Top() != 0 {
		t.Errorf("stack not clean: %d", L.Top())
	}
}

func TestRawSetAndRawGet(t *testing.T) {
	L := newState(t)
	L.NewTable(0, 0)

	L.Push(true)
	L.Push("yes")
	if err := L.RawSet(-3); err != nil {
		t.Fatal(err)
	}
	L.Push(2.5)
	L.Push("half")
	if err := L.RawSet(-3); err != nil {
		t.Fatal(err)
	}
	if L.Top() != 1 {
		t.Fatalf("RawSet should pop the key and value, Top() = %d", L.Top())
	}

	for _, c := range []struct{ key, want any }{
		{true, "yes"},
		{2.5, "half"},
		{"missing", nil},
	} {
		L.Push(c.key)
		if err := L.RawGet(-2); err != nil {
			t.Fatal(err)
		}
		if got, _ := L.Get(-1); got != c.want {
			t.Errorf("t[%v] = %v, want %v", c.key, got, c.want)
		}
		L.Pop(1)
	}
	L.Pop(1)
}

func TestRawSetRejectsBadInput(t *testing.T) {
	L := newState(t)
	L.NewTable(0, 0)

	L.Push(nil)
	L.Push(1)
	if err := L.RawSet(-3); err == nil {
		t.Error("a nil key should fail")
	}
	if L.Top() != 1 {
		t.Errorf("Top() = %d after a failed RawSet, want 1", L.Top())
	}

	L.Push(math.NaN())
	L.Push(1)
	if err := L.RawSet(-3); err == nil {
		t.Error("a NaN key should fail")
	}
	if L.Top() != 1 {
		t.Errorf("Top() = %d after a failed RawSet, want 1", L.Top())
	}

	L.DoString(`frozen = table.freeze({})`)
	L.PushGlobal("frozen")
	L.Push("k")
	L.Push(1)
	if err := L.RawSet(-3); err == nil {
		t.Error("writing to a frozen table should fail")
	}
	if L.Top() != 2 {
		t.Errorf("Top() = %d after writing to a frozen table, want 2", L.Top())
	}
	L.Pop(L.Top())

	L.Push(5) // not a table
	L.Push("k")
	L.Push(1)
	if err := L.RawSet(-3); err == nil {
		t.Error("a non-table should fail")
	}
	L.Pop(L.Top())

	L.NewTable(0, 0)
	if err := L.RawSet(-1); err == nil {
		t.Error("missing operands should fail")
	}
}
