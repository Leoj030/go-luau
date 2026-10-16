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
