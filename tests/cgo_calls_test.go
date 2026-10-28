package tests

import (
	"reflect"
	"runtime"
	"sync"
	"testing"

	"github.com/Leoj030/go-luau/luau"
)

type fastReq struct {
	Method  string            `luau:"method"`
	Path    string            `luau:"path"`
	Query   map[string]string `luau:"query"`
	Headers map[string]string `luau:"headers"`
	Body    string            `luau:"body"`
}

type fastResp struct {
	Status  int               `luau:"status"`
	Headers map[string]string `luau:"headers"`
	Body    string            `luau:"body"`
}

func atMost(t *testing.T, name string, limit int64, f func()) {
	t.Helper()
	before := runtime.NumCgoCall()
	f()
	if got := runtime.NumCgoCall() - before; got > limit {
		t.Errorf("%s made %d cgo calls, want at most %d", name, got, limit)
	}
}

func TestPrimitivesCostOneCgoCall(t *testing.T) {
	L := newState(t)
	L.NewTable(0, 0)

	atMost(t, "TypeName", 1, func() { L.TypeName(1) })
	atMost(t, "Push(string)", 1, func() { L.Push("v") })
	atMost(t, "SetField", 1, func() { L.SetField(-2, "k") })
	atMost(t, "GetField", 1, func() { L.GetField(-1, "k") })
	atMost(t, "Pop", 1, func() { L.Pop(1) })
}

func TestWholeValuesCostFewCgoCalls(t *testing.T) {
	L := newState(t)
	req := fastReq{
		Method:  "GET",
		Path:    "/",
		Query:   map[string]string{"a": "1", "b": "2", "c": "3"},
		Headers: map[string]string{"host": "x", "user-agent": "y", "accept": "*/*", "z": "1", "w": "2"},
	}
	atMost(t, "Push(struct with maps)", 1, func() { L.Push(req) })
	L.Pop(1)

	enc, _ := luau.Encode(req)
	atMost(t, "Encode", 0, func() { luau.Encode(req) })
	atMost(t, "PushEncoded", 1, func() { L.PushEncoded(enc) })
	L.Pop(1)

	L.DoString(`resp = {status = 200, headers = {a = "1", b = "2", c = "3"}, body = "hello"}`)
	L.PushGlobal("resp")
	var out fastResp
	atMost(t, "Decode", 1, func() { L.Decode(-1, &out) })

	var snap *luau.Snapshot
	atMost(t, "Snapshot", 1, func() { snap, _ = L.Snapshot(-1) })
	atMost(t, "Snapshot.Decode", 0, func() { snap.Decode(&out) })
}

func TestEncodeAndPushEncoded(t *testing.T) {
	req := fastReq{Method: "POST", Path: "/x", Query: map[string]string{"q": "1"}, Headers: map[string]string{"a": "b"}, Body: "hi"}
	enc, err := luau.Encode(req)
	if err != nil {
		t.Fatal(err)
	}
	// The same Encoded can be pushed more than once, and into different states.
	for i := 0; i < 2; i++ {
		L := newState(t)
		for j := 0; j < 2; j++ {
			if err := L.PushEncoded(enc); err != nil {
				t.Fatal(err)
			}
			var got fastReq
			if err := L.Decode(-1, &got); err != nil || !reflect.DeepEqual(got, req) {
				t.Fatalf("got %+v, %v; want %+v", got, err, req)
			}
			L.Pop(1)
		}
	}
	if _, err := luau.Encode(make(chan int)); err == nil {
		t.Error("a channel can't be encoded")
	}
	if err := newState(t).PushEncoded(nil); err == nil {
		t.Error("PushEncoded(nil) should fail")
	}
}

func TestSnapshotOutlivesTheStateAndWorksFromAnyGoroutine(t *testing.T) {
	L, err := luau.NewState()
	if err != nil {
		t.Fatal(err)
	}
	L.DoString(`
		resp = {status = 201, headers = {a = "1"}, body = "made", extra = function() end}
		name = "plain"`)
	L.PushGlobal("resp")
	tbl, err := L.Snapshot(-1)
	if err != nil {
		t.Fatal(err)
	}
	L.Pop(1)
	L.PushGlobal("name")
	str, _ := L.Snapshot(-1)
	L.Pop(1)
	L.Close()

	if tbl.Kind() != "table" || str.Kind() != "string" {
		t.Errorf("kinds: %q, %q", tbl.Kind(), str.Kind())
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var out fastResp
			if err := tbl.Decode(&out); err != nil || out.Status != 201 || out.Body != "made" || out.Headers["a"] != "1" {
				t.Errorf("got %+v, %v", out, err)
			}
			if got, err := str.Any(); err != nil || got != "plain" {
				t.Errorf("got %v, %v", got, err)
			}
		}()
	}
	wg.Wait()

	// The function inside the table is only an error if you decode it.
	if _, err := tbl.Any(); err == nil {
		t.Error("Any should fail on a table that holds a function")
	}
}

func TestFieldKeysMayContainNULBytes(t *testing.T) {
	L := newState(t)
	L.NewTable(0, 0)
	L.Push(1)
	if err := L.SetField(-2, "a\x00b"); err != nil {
		t.Fatal(err)
	}
	L.GetField(-1, "a\x00b")
	if v, _ := L.Get(-1); v != 1.0 {
		t.Errorf("t[\"a\\0b\"] = %v, want 1", v)
	}
	L.Pop(1)
	L.GetField(-1, "a")
	if v, _ := L.Get(-1); v != nil {
		t.Errorf("t[\"a\"] = %v, want nil (the key must not be cut at the NUL)", v)
	}
}

func TestNilMapAndSliceBecomeEmptyTables(t *testing.T) {
	L := newState(t)
	L.Push(map[string]string(nil))
	L.Push([]int(nil))
	for i := 0; i < 2; i++ {
		if v, _ := L.Get(-1 - i); !reflect.DeepEqual(v, []any{}) {
			t.Errorf("got %#v, want an empty table", v)
		}
	}
}
