package tests

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Leoj030/go-luau/luau"
)

func newRequireState(t *testing.T) *luau.State {
	t.Helper()
	L := newState(t)
	if err := L.OpenRequire(luau.FSRequirer(fstest.MapFS{})); err != nil {
		t.Fatal(err)
	}
	return L
}

// buildProcessModule leaves a table on the stack with a string, a table and
// a function in it.
func buildProcessModule(t *testing.T, L *luau.State) {
	t.Helper()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(L.NewTable(0, 3))
	must(L.Push("linux"))
	must(L.SetField(-2, "platform"))
	must(L.Push(map[string]string{"DEV": "1"}))
	must(L.SetField(-2, "env"))
	must(L.PushFunction("cwd", func(c *luau.State) (int, error) {
		return 1, c.Push("/work")
	}))
	must(L.SetField(-2, "cwd"))
}

func TestRegisterModuleTableHoldsAnyValue(t *testing.T) {
	L := newRequireState(t)
	buildProcessModule(t, L)
	if err := L.RegisterModuleTable("@std/process"); err != nil {
		t.Fatal(err)
	}
	if L.Top() != 0 {
		t.Errorf("the table should be consumed, Top() = %d", L.Top())
	}

	err := L.DoString(`
		local p = require("@std/process")
		result = p.platform .. p.env.DEV .. p.cwd()`)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := L.GetGlobal("result"); got != "linux1/work" {
		t.Errorf("got %v, want linux1/work", got)
	}
}

func TestRegisteredModulesAreReadOnly(t *testing.T) {
	L := newRequireState(t)
	buildProcessModule(t, L)
	if err := L.RegisterModuleTable("@std/process"); err != nil {
		t.Fatal(err)
	}
	err := L.RegisterModule("@std/json", map[string]luau.Func{
		"encode": func(*luau.State) (int, error) { return 0, nil },
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, src := range []string{
		`require("@std/process").platform = "windows"`,
		`require("@std/process").cwd = nil`,
		`require("@std/process").extra = 1`,
		`require("@std/json").encode = nil`,
	} {
		err := L.DoString(src)
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "readonly") {
			t.Errorf("%q: got %v, want a read-only error", src, err)
		}
	}

	// Tables inside a module are left to the runtime: env is still writable.
	if err := L.DoString(`require("@std/process").env.DEV = "2"`); err != nil {
		t.Fatal(err)
	}
	L.DoString(`seen = require("@std/process").env.DEV`)
	if got, _ := L.GetGlobal("seen"); got != "2" {
		t.Errorf("env.DEV = %v, want 2", got)
	}
}

func TestRegisterModuleTableValidation(t *testing.T) {
	L := newRequireState(t)

	if err := L.RegisterModuleTable("@x"); err == nil {
		t.Error("an empty stack should fail")
	}

	L.Push(5)
	if err := L.RegisterModuleTable("@x"); err == nil {
		t.Error("a non-table should fail")
	}
	if L.Top() != 1 {
		t.Errorf("Top() = %d after a rejected registration, want 1", L.Top())
	}
	L.Pop(1)

	L.NewTable(0, 0)
	for _, path := range []string{"no-at-sign", "@bad\x00name", ""} {
		if err := L.RegisterModuleTable(path); err == nil {
			t.Errorf("path %q should fail", path)
		}
		if L.Top() != 1 {
			t.Errorf("a rejected path should leave the table on the stack, Top() = %d", L.Top())
		}
	}
	L.Pop(1)
}

func TestFreeze(t *testing.T) {
	L := newState(t)
	L.NewTable(0, 0)
	if err := L.Freeze(-1); err != nil {
		t.Fatal(err)
	}

	L.Push("k")
	L.Push(1)
	if err := L.RawSet(-3); err == nil {
		t.Error("a frozen table should reject RawSet")
	}
	L.PopGlobal("frozen") // RawSet popped the key and value, so the table is on top
	if err := L.DoString(`frozen.x = 1`); err == nil || !strings.Contains(strings.ToLower(err.Error()), "readonly") {
		t.Errorf("got %v, want a read-only error", err)
	}

	L.Push(1)
	if err := L.Freeze(-1); err == nil {
		t.Error("Freeze of a non-table should fail")
	}
	if err := L.Freeze(99); err == nil {
		t.Error("Freeze of an invalid index should fail")
	}
}
