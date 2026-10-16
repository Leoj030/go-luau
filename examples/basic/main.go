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
		return 1, nil
	})

	if err := L.DoString(`print("2 + 3 =", add(2, 3))`); err != nil {
		fmt.Println("error:", err)
	}
}
