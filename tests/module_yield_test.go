package tests

import (
	"errors"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Leoj030/go-luau/luau"
)

type moduleDone struct {
	waiter *luau.Thread
	err    error
}

// yieldingRequirer is FSRequirer plus a ModuleFinished that just records
// the event, so a test can play the part of the scheduler.
type yieldingRequirer struct {
	luau.Requirer
	done chan moduleDone
}

func (y *yieldingRequirer) ModuleFinished(w *luau.Thread, err error) {
	y.done <- moduleDone{w, err}
}

type moduleEnv struct {
	L      *luau.State
	files  map[string]string
	req    *yieldingRequirer
	paused chan *luau.Thread // handles of coroutines suspended inside pause()
}

func newModuleEnv(t *testing.T, files map[string]string) *moduleEnv {
	t.Helper()
	fsys := fstest.MapFS{}
	for name, src := range files {
		fsys[name] = &fstest.MapFile{Data: []byte(src)}
	}
	e := &moduleEnv{
		L:      newState(t),
		files:  files,
		req:    &yieldingRequirer{Requirer: luau.FSRequirer(fsys), done: make(chan moduleDone, 4)},
		paused: make(chan *luau.Thread, 4),
	}
	if err := e.L.OpenRequire(e.req); err != nil {
		t.Fatal(err)
	}
	// pause() suspends whoever calls it until the test resumes its handle.
	e.L.Register("pause", func(c *luau.State) (int, error) {
		th, err := c.Thread()
		if err != nil {
			return 0, err
		}
		e.paused <- th
		return c.Yield(0)
	})
	return e
}

// startMain runs main.luau in a coroutine until it first yields.
func (e *moduleEnv) startMain(t *testing.T) (*luau.Thread, int, bool, error) {
	t.Helper()
	th, err := e.L.NewThread()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(th.Close)
	bc, err := luau.Compile(e.files["main.luau"])
	if err != nil {
		t.Fatal(err)
	}
	if err := th.Load(bc, "@main.luau"); err != nil {
		t.Fatal(err)
	}
	n, done, err := th.ResumeN(0)
	return th, n, done, err
}

func recvEvent[T any](t *testing.T, ch chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for an event")
		var zero T
		return zero
	}
}

func TestModuleCanYieldWhileLoading(t *testing.T) {
	e := newModuleEnv(t, map[string]string{
		"main.luau": `local m = require("./m") return m.value, m.greet()`,
		"m.luau":    `pause() return {value = 42, greet = function() return "hi" end}`,
	})

	_, n, done, err := e.startMain(t)
	if err != nil || done || n != 0 {
		t.Fatalf("main should be suspended inside require: n=%d done=%v err=%v", n, done, err)
	}

	mod := recvEvent(t, e.paused) // the module is paused inside pause()
	defer mod.Close()
	if n, done, err := mod.ResumeN(0); err != nil || !done || n != 1 {
		t.Fatalf("module: n=%d done=%v err=%v; want it to finish with one result", n, done, err)
	}

	fin := recvEvent(t, e.req.done)
	defer fin.waiter.Close()
	if fin.err != nil {
		t.Fatal(fin.err)
	}
	// The module's result, a table holding a function, was handed over on the
	// stack, so it didn't need to be convertible to Go.
	n, done, err = fin.waiter.ResumeN(1)
	if err != nil || !done || n != 2 {
		t.Fatalf("main after require: n=%d done=%v err=%v", n, done, err)
	}
	v1, _ := fin.waiter.Get(1)
	v2, _ := fin.waiter.Get(2)
	if v1 != 42.0 || v2 != "hi" {
		t.Errorf("got %v, %v; want 42, hi", v1, v2)
	}
}

func TestModuleCanYieldMoreThanOnce(t *testing.T) {
	e := newModuleEnv(t, map[string]string{
		"main.luau": `return require("./m")`,
		"m.luau":    `pause() pause() return "done"`,
	})
	if _, n, done, err := e.startMain(t); err != nil || done || n != 0 {
		t.Fatalf("main: n=%d done=%v err=%v", n, done, err)
	}

	first := recvEvent(t, e.paused)
	defer first.Close()
	if _, done, err := first.ResumeN(0); err != nil || done {
		t.Fatalf("first resume: done=%v err=%v; want it to yield again", done, err)
	}
	if len(e.req.done) != 0 {
		t.Fatal("the requirer was woken before the module finished")
	}

	second := recvEvent(t, e.paused)
	defer second.Close()
	if _, done, err := second.ResumeN(0); err != nil || !done {
		t.Fatalf("second resume: done=%v err=%v", done, err)
	}

	fin := recvEvent(t, e.req.done)
	defer fin.waiter.Close()
	if n, done, err := fin.waiter.ResumeN(1); err != nil || !done || n != 1 {
		t.Fatalf("main: n=%d done=%v err=%v", n, done, err)
	}
	if v, _ := fin.waiter.Get(1); v != "done" {
		t.Errorf("got %v, want done", v)
	}
}

func TestModuleErrorAfterYieldReachesPcall(t *testing.T) {
	e := newModuleEnv(t, map[string]string{
		"main.luau": `local ok, err = pcall(function() return require("./m") end) return ok, err`,
		"m.luau":    `pause() error("late boom")`,
	})
	if _, n, done, err := e.startMain(t); err != nil || done || n != 0 {
		t.Fatalf("main: n=%d done=%v err=%v", n, done, err)
	}

	mod := recvEvent(t, e.paused)
	defer mod.Close()
	if _, _, err := mod.ResumeN(0); err == nil || !strings.Contains(err.Error(), "late boom") {
		t.Fatalf("module: got %v, want its error", err)
	}

	fin := recvEvent(t, e.req.done)
	defer fin.waiter.Close()
	if fin.err == nil || !strings.Contains(fin.err.Error(), "late boom") {
		t.Fatalf("ModuleFinished got %v, want the module's error", fin.err)
	}

	// The error is raised where require was called, so the script's pcall catches it.
	n, done, err := fin.waiter.ResumeError(fin.err)
	if err != nil || !done || n != 2 {
		t.Fatalf("main after the error: n=%d done=%v err=%v", n, done, err)
	}
	ok, _ := fin.waiter.Get(1)
	msg, _ := fin.waiter.Get(2)
	if ok != false || !strings.Contains(msg.(string), "late boom") {
		t.Errorf("got %v, %v; want false and the module's error", ok, msg)
	}
}

func TestModuleErrorAfterYieldFailsTheRequirer(t *testing.T) {
	e := newModuleEnv(t, map[string]string{
		"main.luau": `return require("./m")`,
		"m.luau":    `pause() error("late boom")`,
	})
	th, _, _, _ := e.startMain(t)

	mod := recvEvent(t, e.paused)
	defer mod.Close()
	mod.ResumeN(0)
	fin := recvEvent(t, e.req.done)
	defer fin.waiter.Close()

	if _, _, err := fin.waiter.ResumeError(fin.err); err == nil || !strings.Contains(err.Error(), "late boom") {
		t.Errorf("got %v, want the module's error", err)
	}
	if th.Status() != luau.ThreadFailed {
		t.Errorf("status = %v, want failed", th.Status())
	}
}

func TestYieldingModuleNeedsACoroutineRequirer(t *testing.T) {
	e := newModuleEnv(t, map[string]string{"m.luau": `pause() return 1`})

	// DoString runs on the main thread, which can't yield.
	err := e.L.DoString(`require("./m")`)
	if err == nil || !strings.Contains(err.Error(), "yield") {
		t.Errorf("got %v, want a yield error", err)
	}
	select {
	case th := <-e.paused:
		th.Close()
	default:
	}
}

func TestResumeErrorMisuse(t *testing.T) {
	L := newState(t)
	th, _ := L.NewThread()
	defer th.Close()

	if _, _, err := th.ResumeError(errors.New("x")); err == nil {
		t.Error("a thread with nothing loaded can't be resumed with an error")
	}
	bc, _ := luau.Compile(`return 1`)
	th.Load(bc, "=x")
	if _, _, err := th.ResumeError(errors.New("x")); err == nil {
		t.Error("a thread that hasn't started can't be resumed with an error")
	}
	th.ResumeN(0) // runs to the end
	if _, _, err := th.ResumeError(errors.New("x")); err == nil {
		t.Error("a finished thread can't be resumed with an error")
	}
}
