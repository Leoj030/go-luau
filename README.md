# go-luau

Go bindings for [Luau](https://luau.org), built with cgo. The binding stays close to Luau's C API and stays out of policy. It has no require, no scheduler, and no opinion on what a script's global API should look like, so you decide what your scripts can see and do.

> **Status: early development.** The API will change. It has only been built and tested on Linux so far. Sandboxing, memory limits and cancellation exist, but they haven't been security-reviewed, so be careful before running untrusted scripts. See [Limitations](#limitations).

## Features

- **Self-contained build.** The Luau sources are vendored inside the module, so go get and go build are all you need. There are no submodules, CMake or system libraries to install.
- **Compile, load and call as separate steps.** Compile once, cache the bytecode, and run it many times with different arguments.
- **Go functions callable from Luau.** Go errors and panics become ordinary Luau errors, and a Go function can yield from a coroutine.
- **Tables and references.** Build and read tables from Go, iterate them, and keep Luau values alive from Go with Ref.
- **Userdata.** Hand Go objects to scripts, give them methods through metatables, and get a Finalize callback when Luau garbage-collects them.
- **Coroutines.** Create threads, resume them, read their status, and give each one its own globals.
- **Sandboxing and limits.** Pick which standard libraries a state opens, cap its memory, lock down its globals with Sandbox, and stop runaway scripts from another goroutine or on a context timeout.
- **Exception-safe.** Luau reports errors with C++ exceptions. Every call into Luau is guarded so an exception never unwinds through Go frames.

## Requirements

- Go 1.21 or newer
- cgo enabled (`CGO_ENABLED=1`)
- A C++17 compiler (`g++` or `clang++`)

## Install

```bash
go get github.com/Leoj030/go-luau
```

The first build compiles around 75 C++ files and takes a while (roughly 15 seconds on a multi-core machine). The Luau sources live in their own package, so Go caches them and later builds only recompile your code.

## Quick start

```go
package main

import (
    "errors"
    "fmt"

    "github.com/Leoj030/go-luau/luau"
)

func main() {
    L, err := luau.NewState()
    if err != nil {
        panic(err)
    }
    defer L.Close()

    // Expose a Go function to scripts.
    L.Register("add", func(c *luau.State) (int, error) {
        a, _ := c.Get(1)
        b, _ := c.Get(2)
        x, ok1 := a.(float64)
        y, ok2 := b.(float64)
        if !ok1 || !ok2 {
            return 0, errors.New("add: expected two numbers")
        }
        c.Push(x + y)
        return 1, nil // number of results pushed
    })

    if err := L.DoString(`print("2 + 3 =", add(2, 3))`); err != nil {
        fmt.Println("error:", err)
    }

    // Compile once, run many times.
    bc, _ := luau.Compile(`local a, b = ... return a * b`)
    for _, n := range []float64{2, 3} {
        L.Load(bc, "=mul")
        res, err := L.Call(n, 10)
        fmt.Println(res, err) // [20] <nil>, then [30] <nil>
    }
}
```

A larger example covering tables, userdata and error handling is in [`examples/basic`](examples/basic/main.go). Run it with `make example`.

## Running scripts with limits

```go
L, err := luau.NewStateWith(luau.Options{
    Libs:        luau.LibBase | luau.LibMath | luau.LibString | luau.LibTable,
    MemoryLimit: 32 << 20, // bytes
})
if err != nil {
    panic(err)
}
defer L.Close()

// Set up everything scripts may use, then lock the globals.
L.SetGlobal("greeting", "hello")
L.Sandbox()

// Stop the script if it runs longer than a second.
ctx, cancel := context.WithTimeout(context.Background(), time.Second)
defer cancel()

stop := L.WatchContext(ctx)
err = L.DoString(src)
stop() // call this before reusing or closing the state

switch {
case errors.Is(err, luau.ErrInterrupted):
    // timed out
case errors.Is(err, luau.ErrMemory):
    // went over MemoryLimit
}
```

## Coroutines

```go
th, _ := L.NewThread()
defer th.Close() // close threads before the state they came from

bc, _ := luau.Compile(`
    local a = ...
    local b = coroutine.yield(a + 1)
    return "done", b`)
th.Load(bc, "=co")

res, done, _ := th.Resume(10)  // [11], done = false
res, done, _ = th.Resume(5)    // ["done", 5], done = true
```

## How the API works

The API mirrors Luau's C API, so it is stack-based. Values are pushed onto and read from a stack, and positions are stack indices:

- Positive indices count from the bottom (`1` is the first value).
- Negative indices count from the top (`-1` is the top value).
- Luau arrays are 1-based.

Inside a function registered from Go, the arguments are at indices `1..Top()`, and you return how many results you pushed.

### Overview

| Area          | API                                                                                     |
|---------------|-----------------------------------------------------------------------------------------|
| Lifecycle     | `NewState`, `Close`, `GC`, `Top`, `Pop`, `TypeName`                                     |
| Running code  | `Compile`, `Load`, `Call`, `DoString`                                                   |
| Values        | `Push`, `Get` (nil, bool, number, string)                                               |
| Globals       | `SetGlobal`, `GetGlobal`, `PushGlobal`, `PopGlobal`                                     |
| Tables        | `NewTable`, `GetField`, `SetField`, `GetIndex`, `SetIndex`, `Len`, `Next`, `ForEach`    |
| References    | `Ref`, `PushRef`, `Unref`                                                               |
| Go functions  | `Register`, `PushFunction`                                                              |
| Userdata      | `PushUserdata`, `ToUserdata`, `SetMetatable`, the `Finalizer` interface                 |
| Configuration | `NewStateWith(Options{Libs, MemoryLimit})`, `Lib*` flags, `MemoryUsed`                  |
| Safety        | `Sandbox`, `Interrupt`, `ClearInterrupt`, `WatchContext`, `ErrMemory`, `ErrInterrupted` |
| Coroutines    | `NewThread`, `Resume`, `ResumeN`, `Status`, `Close`, `SandboxThread`                    |

See the doc comments in [`luau/luau.go`](luau/luau.go) for exact behavior.

### Things worth knowing

- **Numbers are `float64`.** Luau numbers are doubles, so `Get` always returns `float64` for numbers, even when you pushed an `int`.
- **Syntax errors come from `Load`, not `Compile`.** Luau encodes a failed compile as special bytecode, and `Load` reports it with the chunk name.
- **Chunk names.** A name starting with `=` is used as-is (`"=script"` shows up as `script:1: ...`), and `@` marks a file name.
- **`Call` consumes the function** on top of the stack. To call it again, `Load` it again or keep a `Ref`.
- **Table access is raw.** The table functions don't trigger metamethods such as `__index`.
- **Pass data to a script with arguments.** Prefer `Call(args...)` and `local a, b = ...` over globals for per-run data. Use globals for things every script should see.
- **Finalizers run during garbage collection.** `Finalize` must be quick and must not call back into the `State`.
- **`collectgarbage` isn't exposed to scripts** by the vendored Luau version. Use `L.GC()` from Go, or register your own function that calls it.
- **Invalid stack indices return errors**, not crashes. Luau itself is unsafe to query out of range, so the binding checks in Go first.
- **Default libraries.** A zero-value `Options` opens what `luaL_openlibs` opens. `LibAll` additionally enables `class` and `integer`, and `LibNone` opens nothing, not even `print`.
- **Memory limits are in bytes** and count everything the state allocates. Hitting the limit fails the running script with an error wrapping `ErrMemory`, and the state stays usable after a `GC()`.
- **Call `Interrupt` only on a live state.** `WatchContext` handles this for you: call its returned `stop` function before `Close`.

## Limitations

What the binding doesn't do yet:

- **`Sandbox` is state-wide.** It makes the globals table read-only for scripts and for the host, so set everything up before calling it. Per-script environments (`luaL_sandboxthread`) will arrive with the coroutine API.
- **Cancellation is cooperative.** `Interrupt` stops Luau code at safepoints (loops and calls). It can't stop a Go function that blocks.
- **Only nil, bool, number and string convert to Go values.** Reading other types, such as tables, functions, vectors and buffers, with `Get` returns an error. Use the table and reference functions instead.
- **Table keys are limited** to strings and integers.
- **Error values are strings.** There are no tracebacks, and non-string error objects aren't preserved.
- **A `State` is not safe for concurrent use.** Use one goroutine per state, or add your own locking.
- **Methods called after `Close` aren't guarded** yet.
- **Functions registered with `Register` or `PushFunction` live until `Close`.** Registering them in a loop grows memory.
- **Linux only so far.** macOS and Windows haven't been tried.

## License

The Luau sources in `luau/` are distributed under their own licenses, which are included as [`luau/LICENSE-luau.txt`](luau/LICENSE-luau.txt) and [`luau/LICENSE-lua.txt`](luau/LICENSE-lua.txt). Keep them if you redistribute this code.

The binding code is under the license in [`LICENSE`](LICENSE) at the repository root.
