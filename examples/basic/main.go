package main

import (
	"errors"
	"fmt"

	"github.com/Leoj030/go-luau/luau"
)

type Counter struct{ n int }

// Finalize runs when Luau garbage-collects the userdata (or at Close).
func (c *Counter) Finalize() { fmt.Println("counter finalized, n =", c.n) }

func check(err error) {
	if err != nil {
		panic(err)
	}
}

// counterMeta builds a shared metatable once: { __index = { inc, value } }.
func counterMeta(L *luau.State) luau.Ref {
	check(L.NewTable(0, 1)) // metatable
	check(L.NewTable(0, 2)) // methods

	check(L.PushFunction("inc", func(s *luau.State) (int, error) {
		v, ok := s.ToUserdata(1)
		if !ok {
			return 0, errors.New("inc: expected a counter")
		}
		v.(*Counter).n++
		return 0, nil
	}))
	check(L.SetField(-2, "inc"))

	check(L.PushFunction("value", func(s *luau.State) (int, error) {
		v, ok := s.ToUserdata(1)
		if !ok {
			return 0, errors.New("value: expected a counter")
		}
		s.Push(v.(*Counter).n)
		return 1, nil
	}))
	check(L.SetField(-2, "value"))

	check(L.SetField(-2, "__index"))
	ref, err := L.Ref(-1)
	check(err)
	check(L.Pop(1))
	return ref
}

func main() {
	L, err := luau.NewState()
	check(err)
	defer L.Close()

	// 1. Expose a Go function to scripts.
	check(L.Register("add", func(c *luau.State) (int, error) {
		if c.Top() != 3 {
			return 0, fmt.Errorf("add: expected 3 arguments, got %d", c.Top())
		}
		a, _ := c.Get(1)
		b, _ := c.Get(2)
		d, _ := c.Get(3)
		x, ok1 := a.(float64)
		y, ok2 := b.(float64)
		z, ok3 := d.(float64)
		if !ok1 || !ok2 || !ok3 {
			return 0, errors.New("add: expected three numbers")
		}
		c.Push(x + y + z)
		return 1, nil
	}))
	check(L.DoString(`print("2 + 3 + 4 =", add(2, 3, 4))`))

	// 2. Globals set from Go.
	check(L.SetGlobal("greeting", "hello from Go"))
	check(L.DoString(`print(greeting)`))

	// 3. Compile once, run many times with different arguments.
	bc, err := luau.Compile(`local a, b = ... return a * b, a + b`)
	check(err)
	for _, p := range [][2]float64{{2, 3}, {4, 5}} {
		check(L.Load(bc, "=math"))
		res, err := L.Call(p[0], p[1])
		check(err)
		fmt.Printf("%v * %v and %v + %v -> %v\n", p[0], p[1], p[0], p[1], res)
	}

	// 4. Build a table in Go and hand it to scripts.
	check(L.NewTable(0, 2))
	check(L.Push("Luau"))
	check(L.SetField(-2, "name"))
	check(L.Push(1))
	check(L.SetField(-2, "version"))
	check(L.PopGlobal("config"))
	check(L.DoString(`print(config.name, config.version)`))

	// 5. Userdata: a Go object with methods, owned by Luau's GC.
	meta := counterMeta(L)
	check(L.PushUserdata(&Counter{}))
	check(L.PushRef(meta))
	check(L.SetMetatable(-2))
	check(L.PopGlobal("c"))
	check(L.DoString(`c:inc() c:inc() c:inc() print("count:", c:value())`))

	// 6. Errors come back as ordinary Go errors, and the state stays usable.
	fmt.Println("caught:", L.DoString(`error("something went wrong")`))
	fmt.Println("caught:", L.DoString(`local = =`))
	check(L.DoString(`print("still works")`))
}
