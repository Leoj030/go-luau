// Package vm is a high-level interface to Luau, built on package luau. It
// hides the stack: you work with ordinary Go values, Go functions and *Value
// handles. Package luau is still there for anything this package doesn't
// cover, and State returns the low-level state.
//
//	v, _ := vm.New()
//	defer v.Close()
//
//	v.Set("add", func(a, b float64) float64 { return a + b })
//	v.Do(`print(add(2, 3))`)
//
// A VM is not safe for concurrent use. Use it from one goroutine.
//
// Conversions follow luau.State.Push and Decode. A Go function becomes a Luau
// function, a map with string keys becomes a table (and may hold functions),
// and a *Value stands for a Luau value that can't be copied into Go, such as
// a function.
package vm

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/Leoj030/go-luau/luau"
)

// Error is the error type scripts fail with: the kind, chunk, line, column
// and traceback. Use errors.As to reach it.
type Error = luau.Error

// The errors that errors.Is can match.
var (
	ErrMemory      = luau.ErrMemory      // a script went over the memory limit
	ErrInterrupted = luau.ErrInterrupted // a script was stopped by a cancelled context
	ErrClosed      = luau.ErrClosed      // the VM has been closed
)

// VM is a Luau state with a high-level interface.
type VM struct {
	s     *luau.State // the main state
	cur   *luau.State // where operations run: s, or the call view while a Go function runs
	depth int         // how many Go functions are running; 0 means we aren't inside one
	fsys  fs.FS       // set by EnableRequire, and used by DoFile
}

// Option configures New.
type Option func(*config)

type config struct {
	libs   luau.Lib
	memory int
}

// WithLibs chooses the standard libraries scripts get, for example
// luau.LibBase|luau.LibMath|luau.LibString. The default is Luau's usual set.
func WithLibs(libs luau.Lib) Option { return func(c *config) { c.libs = libs } }

// WithMemoryLimit caps the memory scripts may use, in bytes. A script that
// goes over it fails with ErrMemory.
func WithMemoryLimit(bytes int) Option { return func(c *config) { c.memory = bytes } }

// New creates a VM.
func New(opts ...Option) (*VM, error) {
	var cfg config
	for _, o := range opts {
		o(&cfg)
	}
	s, err := luau.NewStateWith(luau.Options{Libs: cfg.libs, MemoryLimit: cfg.memory})
	if err != nil {
		return nil, err
	}
	return &VM{s: s, cur: s}, nil
}

// Close frees the VM. Every *Value from it becomes unusable, and its methods
// return ErrClosed.
func (v *VM) Close() { v.s.Close() }

// State returns the underlying low-level state, for anything this package
// doesn't cover. Use it between operations, and mind the stack: leave it as
// you found it.
func (v *VM) State() *luau.State { return v.s }

// GC runs a full garbage collection.
func (v *VM) GC() { v.s.GC() }

// MemoryUsed returns the number of bytes the VM has allocated.
func (v *VM) MemoryUsed() int { return v.s.MemoryUsed() }

// Sandbox locks the globals and the standard libraries against changes by
// scripts. Call it last, after every Set, Module and EnableRequire, because
// it locks the host out too. Module can still be used afterwards.
func (v *VM) Sandbox() { v.s.Sandbox() }

// EnableRequire gives scripts a require function that reads modules from
// fsys, with Luau's rules for relative paths, init files and .luaurc
// aliases. DoFile reads from fsys too. Call it before Sandbox.
func (v *VM) EnableRequire(fsys fs.FS) error {
	if err := v.s.OpenRequire(luau.FSRequirer(fsys)); err != nil {
		return err
	}
	v.fsys = fsys
	return nil
}

// EnableRequireWith is EnableRequire for a custom luau.Requirer, such as one
// that serves modules from a database.
func (v *VM) EnableRequireWith(r luau.Requirer) error { return v.s.OpenRequire(r) }

// Set assigns a Go value to a global variable. A Go function becomes a Lua
// function. See Value for what else can be passed.
func (v *VM) Set(name string, value any) error {
	s := v.cur
	if err := v.push(s, name, value); err != nil {
		return err
	}
	return s.PopGlobal(name)
}

// Get returns a global variable. The *Value is yours: call Release when you
// are done with it, or let Close free it with everything else.
func (v *VM) Get(name string) (*Value, error) {
	s := v.cur
	if err := s.PushGlobal(name); err != nil {
		return nil, err
	}
	defer s.Pop(1)
	return v.valueAt(s, -1)
}

// Module registers members as the module that require(path) returns. The
// path starts with "@", such as "@std/json". Members can be Go functions,
// numbers, strings, nested maps and anything else Set accepts, and the
// module table is frozen. A registered module wins over a file with the
// same path.
func (v *VM) Module(path string, members map[string]any) error {
	s := v.cur
	base := s.Top()
	if err := v.push(s, path, members); err != nil {
		return err
	}
	if err := s.RegisterModuleTable(path); err != nil {
		s.Pop(s.Top() - base)
		return err
	}
	return nil
}

// Do runs Luau source and discards its results.
func (v *VM) Do(src string) error { return v.DoContext(context.Background(), src) }

// DoContext is Do with a context. When ctx is done the script is stopped,
// and the error matches both ctx.Err() and ErrInterrupted.
func (v *VM) DoContext(ctx context.Context, src string) error {
	res, err := v.exec(ctx, "=script", src)
	releaseAll(res)
	return err
}

// Eval runs Luau source and returns what it returns. The values are yours:
// Release them when done.
func (v *VM) Eval(src string) ([]*Value, error) { return v.EvalContext(context.Background(), src) }

// EvalContext is Eval with a context. See DoContext.
func (v *VM) EvalContext(ctx context.Context, src string) ([]*Value, error) {
	return v.exec(ctx, "=script", src)
}

// DoFile runs a script file. If EnableRequire was used, name is a path
// inside that file system (slash-separated, with no leading "./"), so that
// require can find modules next to the script. Otherwise it is a normal
// path on disk.
func (v *VM) DoFile(name string) error { return v.DoFileContext(context.Background(), name) }

// DoFileContext is DoFile with a context. See DoContext.
func (v *VM) DoFileContext(ctx context.Context, name string) error {
	var src []byte
	var err error
	if v.fsys != nil {
		src, err = fs.ReadFile(v.fsys, name)
	} else {
		src, err = os.ReadFile(name)
	}
	if err != nil {
		return fmt.Errorf("vm: cannot read script: %w", err)
	}
	res, err := v.exec(ctx, "@"+name, string(src))
	releaseAll(res)
	return err
}

// exec compiles src and runs it on a thread of its own.
func (v *VM) exec(ctx context.Context, chunk, src string) ([]*Value, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	bc, err := luau.Compile(src)
	if err != nil {
		var le *luau.Error
		if errors.As(err, &le) {
			le.Chunk = strings.TrimLeft(chunk, "=@")
		}
		return nil, err
	}
	th, err := v.cur.NewThread()
	if err != nil {
		return nil, err
	}
	defer th.Close()
	if err := th.Load(bc, chunk); err != nil {
		return nil, err
	}
	return v.run(ctx, th, 0)
}

// run resumes th, which has a function and nargs arguments on its stack, and
// turns the results into Values.
func (v *VM) run(ctx context.Context, th *luau.Thread, nargs int) ([]*Value, error) {
	// Only the outermost call watches the context. A nested one would clear
	// an interrupt that the outer watch had already raised.
	var stop func()
	if v.depth == 0 && ctx.Done() != nil {
		stop = v.s.WatchContext(ctx)
	}
	n, done, err := th.ResumeN(nargs)
	if stop != nil {
		stop()
	}
	if err != nil {
		return nil, wrapContextError(ctx, err)
	}
	if !done {
		return nil, errors.New("vm: the script yielded, and Do, Eval and Call can't wait for it to continue")
	}
	out := make([]*Value, 0, n)
	for i := 1; i <= n; i++ {
		x, verr := v.valueAt(th.State, i)
		if verr != nil {
			releaseAll(out)
			return nil, verr
		}
		out = append(out, x)
	}
	return out, nil
}

// wrapContextError makes an interrupted script's error match the context's
// error too, so errors.Is(err, context.DeadlineExceeded) works.
func wrapContextError(ctx context.Context, err error) error {
	if errors.Is(err, luau.ErrInterrupted) {
		if cerr := ctx.Err(); cerr != nil {
			return fmt.Errorf("%w: %w", cerr, err)
		}
	}
	return err
}

// enter makes s the state that operations use while a Go function runs, and
// returns the function that puts the previous one back.
func (v *VM) enter(c *luau.State) (restore func()) {
	prev := v.cur
	v.cur = c
	v.depth++
	return func() {
		v.cur = prev
		v.depth--
	}
}

// valueAt pins the value at idx of s and wraps it.
func (v *VM) valueAt(s *luau.State, idx int) (*Value, error) {
	typ := s.TypeName(idx)
	ref, err := s.Ref(idx)
	if err != nil {
		return nil, err
	}
	return &Value{vm: v, ref: ref, typ: typ}, nil
}

func releaseAll(vals []*Value) {
	for _, x := range vals {
		x.Release()
	}
}