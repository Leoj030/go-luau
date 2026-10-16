# go-luau

Go bindings for [Luau](https://luau.org), built with cgo. The binding stays close to Luau's C API and stays out of policy. It has no require, no scheduler, and no opinion on what a script's global API should look like, so you decide what your scripts can see and do.

> **Status: early development.** The API will change. It has only been built and tested on Linux so far, and it does **not** yet provide sandboxing, memory limits or timeouts, so don't run untrusted scripts with it yet. See [Limitations](#limitations).

## Features

- **Self-contained build.** The Luau sources are vendored inside the module, so `go get` and `go build` are all you need. There are no submodules, CMake or system libraries to install.
- **Compile, load and call as separate steps.** Compile once, cache the bytecode, and run it many times with different arguments.
- **Go functions callable from Luau.** Go errors and panics become ordinary Luau errors.
- **Tables and references.** Build and read tables from Go, iterate them, and keep Luau values alive from Go with `Ref`.
- **Userdata.** Hand Go objects to scripts, give them methods through metatables, and get a `Finalize` callback when Luau garbage-collects them.
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

## How the API works

The API mirrors Luau's C API, so it is stack-based. Values are pushed onto and read from a stack, and positions are stack indices:

- Positive indices count from the bottom (`1` is the first value).
- Negative indices count from the top (`-1` is the top value).
- Luau arrays are 1-based.

Inside a function registered from Go, the arguments are at indices `1..Top()`, and you return how many results you pushed.

### Overview

| Area         | API                                                                                  |
|--------------|--------------------------------------------------------------------------------------|
| Lifecycle    | `NewState`, `Close`, `GC`, `Top`, `Pop`, `TypeName`                                  |
| Running code | `Compile`, `Load`, `Call`, `DoString`                                                |
| Values       | `Push`, `Get` (nil, bool, number, string)                                            |
| Globals      | `SetGlobal`, `GetGlobal`, `PushGlobal`, `PopGlobal`                                  |
| Tables       | `NewTable`, `GetField`, `SetField`, `GetIndex`, `SetIndex`, `Len`, `Next`, `ForEach` |
| References   | `Ref`, `PushRef`, `Unref`                                                            |
| Go functions | `Register`, `PushFunction`                                                           |
| Userdata     | `PushUserdata`, `ToUserdata`, `SetMetatable`, the `Finalizer` interface              |

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

## Limitations

What the binding doesn't do yet:

- **No sandboxing.** `luaL_sandbox` isn't wrapped, and `NewState` opens every standard library. Don't run untrusted scripts.
- **No memory limit and no timeouts or cancellation.**
- **No coroutine API** (create, resume, yield).
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
