package tests

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Leoj030/go-luau/luau"
)

type wakeUp struct {
	th    *luau.Thread
	value string
}

func TestThreadHandleWakesCoroutineFromAnotherGoroutine(t *testing.T) {
	L := newState(t)
	events := make(chan wakeUp, 1)

	// An "asynchronous" function: the work happens on another goroutine and
	// the coroutine stays suspended until the result comes back.
	L.Register("later", func(c *luau.State) (int, error) {
		th, err := c.Thread()
		if err != nil {
			return 0, err
		}
		arg, err := c.Get(1)
		if err != nil {
			th.Close()
			return 0, err
		}
		word, _ := arg.(string)
		go func() { // never touch the State from here
			time.Sleep(10 * time.Millisecond)
			events <- wakeUp{th, word + "!"}
		}()
		return c.Yield(0)
	})

	first, err := L.NewThread()
	if err != nil {
		t.Fatal(err)
	}
	bc, _ := luau.Compile(`local got = later("hello") return "finished", got`)
	if err := first.Load(bc, "=co"); err != nil {
		t.Fatal(err)
	}
	res, done, err := first.Resume()
	if err != nil || done || len(res) != 0 {
		t.Fatalf("first resume: %v, %v, %v; want it to yield nothing", res, done, err)
	}

	// Drop the original handle, so that only the one from Thread() keeps the
	// coroutine alive across the collections.
	first.Close()
	L.GC()
	L.GC()

	// The runtime's event loop: wait for the result, then resume on this
	// goroutine, which owns the State.
	ev := <-events
	defer ev.th.Close()
	if ev.th.Status() != luau.ThreadSuspended {
		t.Errorf("status = %v, want suspended", ev.th.Status())
	}
	res, done, err = ev.th.Resume(ev.value)
	if err != nil || !done || !sameValues(res, "finished", "hello!") {
		t.Errorf("got %v, %v, %v; want [finished hello!]", res, done, err)
	}
}

func TestThreadNeedsACoroutine(t *testing.T) {
	L := newState(t)
	L.Register("who", func(c *luau.State) (int, error) {
		_, err := c.Thread()
		return 0, err
	})
	if err := L.DoString(`who()`); err == nil || !strings.Contains(err.Error(), "coroutine") {
		t.Errorf("got %v, want an error about needing a coroutine", err)
	}
	if _, err := L.Thread(); err == nil {
		t.Error("Thread outside a Go function should fail")
	}
}

func TestThreadHandleAfterStateClosed(t *testing.T) {
	L, err := luau.NewState()
	if err != nil {
		t.Fatal(err)
	}
	var handle *luau.Thread
	L.Register("grab", func(c *luau.State) (int, error) {
		h, err := c.Thread()
		if err != nil {
			return 0, err
		}
		handle = h
		return c.Yield(0)
	})

	th, _ := L.NewThread()
	bc, _ := luau.Compile(`grab()`)
	th.Load(bc, "=co")
	if _, done, err := th.Resume(); err != nil || done {
		t.Fatalf("resume: %v, %v", done, err)
	}
	L.Close()

	if _, _, err := handle.Resume(); !errors.Is(err, luau.ErrClosed) {
		t.Errorf("got %v, want ErrClosed", err)
	}
	handle.Close() // must not touch the freed state
}
