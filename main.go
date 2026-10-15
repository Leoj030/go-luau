package main

import (
	"fmt"

	"github.com/Leoj030/go-luau/luau"
)

func main() {
	L, err := luau.NewState()
	if err != nil {
		panic(err)
	}
	defer L.Close()

	fmt.Println("ok:", L.DoString(`print("hello from Luau", 1 + 2)`))
	fmt.Println("runtime error:", L.DoString(`error("boom")`))
	fmt.Println("syntax error:", L.DoString(`local = =`))
}