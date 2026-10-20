# go-luau [![CI](https://github.com/Leoj030/go-luau/actions/workflows/ci.yml/badge.svg)](https://github.com/Leoj030/go-luau/actions/workflows/ci.yml)

Go bindings for [Luau](https://luau.org), built with cgo. The binding stays close to Luau's C API and stays out of policy. It has no scheduler, and no opinion on what a script's global API should look like, so you decide what your scripts can see and do.

> **Status: early development.** The API will change. It builds and passes its tests on Linux, macOS and Windows. Sandboxing, memory limits and cancellation exist, but they haven't been security-reviewed, so be careful before running untrusted scripts. See [Limitations](#limitations).

## Features

- **Compile, load and call as separate steps.** Compile once, cache the bytecode, and run it many times with different arguments.
- **Go functions callable from Luau.** Go errors and panics become ordinary Luau errors, and a Go function can yield from a coroutine.
- **Tables and references.** Build and read tables from Go, iterate them, and keep Luau values alive from Go with `Ref`.
- **Userdata.** Hand Go objects to scripts, give them methods through metatables, and get a `Finalize` callback when Luau garbage-collects them.
- **Coroutines.** Create threads, resume them, read their status, and give each one its own globals.
- **Sandboxing and limits.** Pick which standard libraries a state opens, cap its memory, lock down its globals with `Sandbox`, and stop runaway scripts from another goroutine or on a `context` timeout.
- **Useful errors.** Failures come back as a typed `*luau.Error` with the kind, chunk, line, a column for syntax errors, a traceback, and the raw error value.
- **Exception-safe.** Luau reports errors with C++ exceptions. Every call into Luau is guarded so an exception never unwinds through Go frames.
- **Structs and tables.** Push Go structs, slices and maps as tables, and `Decode` tables back into Go structs, with luau field tags.
- **require**. `OpenRequire` gives scripts Luau’s own `require`, with relative paths, `init` files and `.luaurc` aliases, served from any `fs.FS` or from your own `Requirer`. `RegisterModule` and `RegisterModuleTable` expose Go code as require-able modules, with any value in them.

## Requirements

- Go 1.21 or newer
- cgo enabled (`CGO_ENABLED=1`)
- A C++17 compiler (`g++` or `clang++`)

## Install

```bash
go get github.com/Leoj030/go-luau
```

The first build compiles around 75 C++ files and takes a while (roughly 15 seconds on a multi-core machine or longer). The Luau sources live in their own package, so Go caches them and later builds only recompile your code.

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

| Area | API |
| --- | --- |
| Lifecycle | `NewState`, `NewStateWith`, `Close`, `GC`, `Top`, `Pop`, `TypeName` |
| Configuration | `Options{Libs, MemoryLimit}`, the `Lib*` flags, `MemoryUsed` |
| Running code | `Compile`, `Load`, `Call`, `DoString` |
| Values | `Push`, `Get` (nil, bool, number, string), `Decode` (structs, slices, maps) |
| Globals | `SetGlobal`, `GetGlobal`, `PushGlobal`, `PopGlobal` |
| Tables | `NewTable`, `GetField`, `SetField`, `GetIndex`, `SetIndex`, `Len`, `Next`, `ForEach` |
| References | `Ref`, `PushRef`, `Unref` |
| Go functions | `Register`, `PushFunction`, `Yield` |
| Userdata | `PushUserdata`, `ToUserdata`, `SetMetatable`, the `Finalizer` interface |
| Coroutines | `NewThread`, `Thread.Resume`, `Thread.ResumeN`, `Thread.Status`, `Thread.SandboxThread`, `Thread.Close` |
| Safety | `Sandbox`, `Interrupt`, `ClearInterrupt`, `WatchContext`, `ErrMemory`, `ErrInterrupted` |
| Errors | `*Error`, `ErrorKind` (`KindSyntax`, `KindRuntime`, `KindMemory`, `KindInterrupted`) |
| Require | `OpenRequire`, `RegisterModule`, `Requirer`, `FSRequirer` |

A `Thread` embeds `*State`, so `Load`, `Push`, `Get`, `SetGlobal` and the rest work on the thread's own stack.

See the doc comments in [`luau/luau.go`](luau/luau.go) for exact behavior.

### Errors

Script failures come back as `*luau.Error`, reachable with `errors.As`. `errors.Is(err, luau.ErrMemory)` and `errors.Is(err, luau.ErrInterrupted)` work too.

| Field | Meaning |
| --- | --- |
| `Kind` | `KindSyntax`, `KindRuntime`, `KindMemory` or `KindInterrupted` |
| `Message` | the error text without the `chunk:line:` prefix |
| `Chunk`, `Line`, `Column` | where it happened (0 means unknown) |
| `Traceback` | one stack frame per line, innermost first |
| `Value` | the raw error value, when it was a string, number or boolean |
| `Ref`, `HasRef` | for errors that are tables, functions or userdata, a reference you can read with `PushRef` |

### Things worth knowing

- **Numbers are `float64`.** Luau numbers are doubles, so `Get` always returns `float64` for numbers, even when you pushed an `int`.
- **Syntax errors come from `Compile`.** It returns an `*Error` with the line and column. `Load` also recognizes the encoded error that older cached bytecode might contain, but that form has a line and no column.
- **Chunk names.** A name starting with `=` is used as-is (`"=script"` shows up as `script:1: ...`), and `@` marks a file name. `Compile` doesn't know the chunk name, so its errors have an empty `Chunk`.
- **`Call` consumes the function** on top of the stack. To call it again, `Load` it again or keep a `Ref`.
- **Table access is raw.** The table functions don't trigger metamethods such as `__index`.
- **Pass data to a script with arguments.** Prefer `Call(args...)` and `local a, b = ...` over globals for per-run data. Use globals for things every script should see.
- **Finalizers run during garbage collection.** `Finalize` must be quick and must not call back into the `State`.
- **`collectgarbage` isn't exposed to scripts** by the vendored Luau version. Use `L.GC()` from Go, or register your own function that calls it.
- **Invalid stack indices return errors**, not crashes. Luau itself is unsafe to query out of range, so the binding checks in Go first.
- **Default libraries.** A zero-value `Options` opens what `luaL_openlibs` opens (`LibDefault`). `LibAll` additionally enables `class` and `integer`, and `LibNone` opens nothing, not even `print`.
- **Memory limits are in bytes** and count everything the state allocates. Hitting the limit fails the running script with an error wrapping `ErrMemory`, and the state stays usable after a `GC()`.
- **`Sandbox` comes last.** It makes the globals table read-only for scripts *and* for the host, so call `SetGlobal`, `Register`, `RegisterModule` and the like first.
- **Call `Interrupt` only on a live state.** `WatchContext` handles this for you: call its returned `stop` function before `Close`.
- **Resuming a thread needs a clean stack.** For the first `Resume`, the thread's stack must hold exactly the loaded function. `Resume` converts yielded values like `Get` does, so for tables and other unsupported types use `ResumeN` and read the thread's stack yourself.
- **Per-thread globals.** For isolated scripts, call `Sandbox` on the main state, then `SandboxThread` on each thread *before* `Load`. Writes then stay private to that thread.
- **Yielding needs a coroutine.** A Go function that calls `Yield` outside `Thread.Resume` makes the script fail with Luau's yield error. It doesn't crash.
- **After `Close`, methods return `ErrClosed`.** The ones with no error return give a zero value instead. A `Thread` whose main state has closed behaves the same way.
- **The stack grows safely.** `Push` and the other functions that add values reserve space first and return `ErrStackOverflow` if the stack is full. `CheckStack(n)` lets you check once before a large batch of pushes.
- **Structs convert their exported fields.** A `luau:"name"` tag renames a field, `luau:"-"` skips it, and `luau:"name,omitempty"` skips zero values.
- **`Decode` fills structs, maps and slices from tables.** Fields the table lacks keep their old values, so you can set defaults first, and unknown table keys are ignored.
- **Converted tables are copies.** To let scripts modify a Go object, use userdata.
- Call `OpenRequire` before Sandbox.
- `FSRequirer` resolves a script’s `require` relative to its chunk name: `@dir/x.luau` must exist in the file system, and a `=name` chunk resolves from the root.
- `os.DirFS` follows symbolic links, so a link can point outside the directory.
- A registered module wins over a file with the same path, so a `.luau` stub can describe it for type checkers.
- Registered module tables are frozen. Tables inside them are not, so call `Freeze` on those if scripts shouldn’t modify them.
- Module paths are case-insensitive.

## Limitations

What the binding doesn't do yet:

- **Security review.** The sandbox and limits are tested, but nobody has tried to break them yet.
- **Cancellation is cooperative.** `Interrupt` stops Luau code at safepoints (loops and calls). It can't stop a Go function that blocks.
- **The memory limit covers Luau's allocations only.** Go-side memory (the function registry, userdata handles, large values built in callbacks) isn't counted.
- **Only nil, bool, number and string convert to Go values.** Reading other types, such as tables, functions, vectors and buffers, with `Get` returns an error. Use the table and reference functions instead.
- **Table keys are limited** to strings and integers.
- **No column for runtime errors.** This is a limit of Luau's bytecode, not of the binding.
- **No debugger support.** Breakpoints, `lua_resumeerror`, and the debug hooks aren't wrapped.
- **A `State` is not safe for concurrent use,** apart from `Interrupt`. Use one goroutine per state, or add your own locking.
- **Functions registered with `Register` or `PushFunction` live until `Close`.** Registering them in a loop grows memory.
- **Embedded structs aren’t flattened,** and there are no custom marshalers.
- **require doesn’t yet support** clearing the cache or cyclic requires with export.

## License

The Luau sources in `luau/` are distributed under their own licenses, which are included as [`luau/LICENSE-luau.txt`](luau/LICENSE-luau.txt) and [`luau/LICENSE-lua.txt`](luau/LICENSE-lua.txt). Keep them if you redistribute this code.

The binding code is under the license in [`LICENSE`](LICENSE) at the repository root.
