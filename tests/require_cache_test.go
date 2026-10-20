package tests

import (
	"testing"

	"github.com/Leoj030/go-luau/luau"
)

func TestClearRequireCache(t *testing.T) {
	e := newReqEnv(t, map[string]string{"m.luau": `tick() return {}`})
	loads := 0
	e.L.Register("tick", func(c *luau.State) (int, error) { loads++; return 0, nil })

	require := func() {
		t.Helper()
		if err := e.L.DoString(`require("./m")`); err != nil {
			t.Fatal(err)
		}
	}

	require()
	require()
	if loads != 1 {
		t.Fatalf("the module ran %d times for two requires, want 1", loads)
	}

	if err := e.L.ClearRequireCache(); err != nil {
		t.Fatal(err)
	}
	require()
	if loads != 2 {
		t.Errorf("after clearing everything the module ran %d times, want 2", loads)
	}

	if err := e.L.ClearRequireCacheEntry("nope.luau"); err != nil {
		t.Fatal(err)
	}
	require()
	if loads != 2 {
		t.Errorf("clearing an unknown key reloaded the module (%d runs)", loads)
	}

	if err := e.L.ClearRequireCacheEntry("m.luau"); err != nil {
		t.Fatal(err)
	}
	require()
	if loads != 3 {
		t.Errorf("after clearing its entry the module ran %d times, want 3", loads)
	}
}

func TestClearRequireCacheKeepsRegisteredModules(t *testing.T) {
	L := newRequireState(t)
	err := L.RegisterModule("@std/x", map[string]luau.Func{
		"hi": func(c *luau.State) (int, error) { return 1, c.Push("hi") },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := L.DoString(`a = require("@std/x").hi()`); err != nil {
		t.Fatal(err)
	}
	if err := L.ClearRequireCache(); err != nil {
		t.Fatal(err)
	}
	if err := L.DoString(`b = require("@std/x").hi()`); err != nil {
		t.Fatalf("the registered module is gone after clearing the cache: %v", err)
	}
}

func TestClearRequireCacheEntryRejectsNUL(t *testing.T) {
	L := newRequireState(t)
	if err := L.ClearRequireCacheEntry("bad\x00key"); err == nil {
		t.Error("a key with a NUL byte should fail")
	}
}
