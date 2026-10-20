package tests

import (
	"errors"
	"testing"
	"testing/fstest"

	"github.com/Leoj030/go-luau/luau"
)

func TestUseAfterCloseReturnsErrClosed(t *testing.T) {
	L, err := luau.NewState()
	if err != nil {
		t.Fatal(err)
	}
	L.Close()
	L.Close() // closing twice is fine

	fn := func(*luau.State) (int, error) { return 0, nil }
	calls := map[string]func() error{
		"Push":                func() error { return L.Push(1) },
		"Pop":                 func() error { return L.Pop(0) },
		"Get":                 func() error { _, err := L.Get(1); return err },
		"CheckStack":          func() error { return L.CheckStack(1) },
		"SetGlobal":           func() error { return L.SetGlobal("x", 1) },
		"GetGlobal":           func() error { _, err := L.GetGlobal("x"); return err },
		"PushGlobal":          func() error { return L.PushGlobal("x") },
		"PopGlobal":           func() error { return L.PopGlobal("x") },
		"NewTable":            func() error { return L.NewTable(0, 0) },
		"GetField":            func() error { return L.GetField(-1, "x") },
		"SetField":            func() error { return L.SetField(-2, "x") },
		"GetIndex":            func() error { return L.GetIndex(1, 1) },
		"SetIndex":            func() error { return L.SetIndex(1, 1) },
		"Len":                 func() error { _, err := L.Len(1); return err },
		"Next":                func() error { _, err := L.Next(1); return err },
		"ForEach":             func() error { return L.ForEach(1, func() error { return nil }) },
		"Ref":                 func() error { _, err := L.Ref(1); return err },
		"PushRef":             func() error { return L.PushRef(1) },
		"PushFunction":        func() error { return L.PushFunction("f", fn) },
		"Register":            func() error { return L.Register("f", fn) },
		"PushUserdata":        func() error { return L.PushUserdata(1) },
		"SetMetatable":        func() error { return L.SetMetatable(-1) },
		"Load":                func() error { return L.Load([]byte{1}, "=x") },
		"Call":                func() error { _, err := L.Call(); return err },
		"DoString":            func() error { return L.DoString("x = 1") },
		"NewThread":           func() error { _, err := L.NewThread(); return err },
		"RawGet":              func() error { return L.RawGet(-2) },
		"RawSet":              func() error { return L.RawSet(-3) },
		"Decode":              func() error { var x int; return L.Decode(1, &x) },
		"OpenRequire":         func() error { return L.OpenRequire(luau.FSRequirer(fstest.MapFS{})) },
		"Freeze":              func() error { return L.Freeze(-1) },
		"RegisterModuleTable": func() error { return L.RegisterModuleTable("@x") },
	}
	for name, call := range calls {
		if err := call(); !errors.Is(err, luau.ErrClosed) {
			t.Errorf("%s: got %v, want ErrClosed", name, err)
		}
	}

	// These return no error, but they must not crash either.
	L.GC()
	L.Sandbox()
	L.Interrupt()
	L.ClearInterrupt()
	L.Unref(1)
	if L.Top() != 0 || L.MemoryUsed() != 0 || L.TypeName(1) != "no value" {
		t.Error("a closed state should report an empty stack and no memory")
	}
	if _, ok := L.ToUserdata(1); ok {
		t.Error("ToUserdata on a closed state should report false")
	}
}

func TestThreadAfterMainStateClosed(t *testing.T) {
	L, err := luau.NewState()
	if err != nil {
		t.Fatal(err)
	}
	th, err := L.NewThread()
	if err != nil {
		t.Fatal(err)
	}
	bc, _ := luau.Compile(`return 1`)
	th.Load(bc, "=co")
	L.Close()

	if _, _, err := th.Resume(); !errors.Is(err, luau.ErrClosed) {
		t.Errorf("Resume: got %v, want ErrClosed", err)
	}
	if _, _, err := th.ResumeN(0); !errors.Is(err, luau.ErrClosed) {
		t.Errorf("ResumeN: got %v, want ErrClosed", err)
	}
	if err := th.Push(1); !errors.Is(err, luau.ErrClosed) {
		t.Errorf("Push: got %v, want ErrClosed", err)
	}
	if th.Status() != luau.ThreadFinished || th.Top() != 0 {
		t.Error("a thread of a closed state should look finished and empty")
	}
	th.SandboxThread()
	th.Close() // must not touch the freed state
}

func TestGetInvalidIndexFailsCleanly(t *testing.T) {
	L := newState(t)
	for _, idx := range []int{0, 1, -1, 99, -99} {
		if v, err := L.Get(idx); err == nil {
			t.Errorf("Get(%d) = %v, want an error on an empty stack", idx, v)
		}
	}
}

func TestCheckStack(t *testing.T) {
	L := newState(t)
	if err := L.CheckStack(100); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		if err := L.Push(i); err != nil {
			t.Fatalf("push %d after CheckStack(100): %v", i, err)
		}
	}
	if err := L.CheckStack(100_000_000); !errors.Is(err, luau.ErrStackOverflow) {
		t.Errorf("got %v, want ErrStackOverflow", err)
	}
	if err := L.CheckStack(-1); err == nil {
		t.Error("a negative count should fail")
	}
}

func TestPushPastTheStackLimitFailsCleanly(t *testing.T) {
	L := newState(t)
	var err error
	pushed := 0
	for pushed < 5_000_000 {
		if err = L.Push(pushed); err != nil {
			break
		}
		pushed++
	}
	if !errors.Is(err, luau.ErrStackOverflow) {
		t.Fatalf("after %d pushes got %v, want ErrStackOverflow", pushed, err)
	}
	if L.Top() != pushed {
		t.Errorf("Top() = %d, want %d", L.Top(), pushed)
	}
	L.Pop(L.Top())
	if err := L.DoString(`x = 1`); err != nil {
		t.Errorf("state unusable after hitting the stack limit: %v", err)
	}
}

func TestGoFunctionCanPushManyResults(t *testing.T) {
	L := newState(t)
	L.Register("many", func(c *luau.State) (int, error) {
		for i := 1; i <= 200; i++ {
			if err := c.Push(i); err != nil {
				return 0, err
			}
		}
		return 200, nil
	})
	if err := L.DoString(`local t = {many()} count = #t`); err != nil {
		t.Fatal(err)
	}
	if got, _ := L.GetGlobal("count"); got != 200.0 {
		t.Errorf("got %v results, want 200", got)
	}
}

func TestPushFunctionRejectsNil(t *testing.T) {
	L := newState(t)
	if err := L.PushFunction("f", nil); err == nil {
		t.Error("a nil function should be rejected")
	}
}
