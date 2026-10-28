package tests

import (
	"strings"
	"testing"

	"github.com/Leoj030/go-luau/luau"
)

func TestThreadResetAllowsReuse(t *testing.T) {
	L := newState(t)
	th, err := L.NewThread()
	if err != nil {
		t.Fatal(err)
	}
	defer th.Close()

	for i, c := range []struct {
		src      string
		wantDone bool
		wantErr  bool
	}{
		{`return 1`, true, false},
		{`error("boom")`, false, true},
		{`coroutine.yield(2)`, false, false}, // left suspended, then abandoned
		{`return "again"`, true, false},
	} {
		bc, err := luau.Compile(c.src)
		if err != nil {
			t.Fatal(err)
		}
		if err := th.Load(bc, "=reuse"); err != nil {
			t.Fatalf("case %d: Load: %v", i, err)
		}
		res, done, err := th.Resume()
		if done != c.wantDone || (err != nil) != c.wantErr {
			t.Fatalf("case %d: done=%v err=%v res=%v", i, done, err, res)
		}
		if err := th.Reset(); err != nil {
			t.Fatalf("case %d: Reset: %v", i, err)
		}
		if th.Status() != luau.ThreadFinished || th.Top() != 0 {
			t.Errorf("case %d: after Reset the status is %v and the stack holds %d, want finished and empty",
				i, th.Status(), th.Top())
		}
	}
}

func TestResetOfARunningThreadFails(t *testing.T) {
	L := newState(t)
	L.Register("selfreset", func(c *luau.State) (int, error) {
		th, err := c.Thread()
		if err != nil {
			return 0, err
		}
		defer th.Close()
		return 0, th.Reset()
	})

	th, _ := L.NewThread()
	defer th.Close()
	bc, _ := luau.Compile(`selfreset()`)
	th.Load(bc, "=x")
	if _, _, err := th.Resume(); err == nil || !strings.Contains(err.Error(), "running") {
		t.Errorf("got %v, want an error about a running thread", err)
	}
}
