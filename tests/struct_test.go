package tests

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Leoj030/go-luau/luau"
)

type testPos struct{ X, Y float64 }

type testPlayer struct {
	Name   string
	Score  int      `luau:"score"`
	Tags   []string `luau:"tags,omitempty"`
	Secret string   `luau:"-"`
	hidden int
	Pos    testPos
}

func decodeGlobal(t *testing.T, L *luau.State, name string, v any) error {
	t.Helper()
	if err := L.PushGlobal(name); err != nil {
		t.Fatal(err)
	}
	defer L.Pop(1)
	return L.Decode(-1, v)
}

func TestStructsBecomeTables(t *testing.T) {
	L := newState(t)
	p := testPlayer{Name: "alice", Score: 10, Secret: "s", hidden: 1, Pos: testPos{1, 2}}
	if err := L.SetGlobal("p", p); err != nil {
		t.Fatal(err)
	}
	err := L.DoString(`
		hasSecret = p.Secret ~= nil
		hasHidden = p.hidden ~= nil
		hasTags = p.tags ~= nil
		out = p.Name .. p.score .. p.Pos.X .. p.Pos.Y`)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]any{
		"hasSecret": false, // tagged "-"
		"hasHidden": false, // unexported
		"hasTags":   false, // omitempty and empty
		"out":       "alice1012",
	} {
		if got, _ := L.GetGlobal(name); got != want {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}

	p.Tags = []string{"a", "b"}
	L.SetGlobal("p", p)
	L.DoString(`tag = p.tags[2]`)
	if got, _ := L.GetGlobal("tag"); got != "b" {
		t.Errorf("tags[2] = %v, want b", got)
	}
}

func TestPushPointers(t *testing.T) {
	L := newState(t)

	var np *testPlayer
	L.Push(np)
	if got, _ := L.Get(-1); got != nil {
		t.Errorf("a nil pointer should push nil, got %#v", got)
	}
	L.Pop(1)

	L.SetGlobal("p", &testPlayer{Name: "bob"})
	L.DoString(`n = p.Name`)
	if got, _ := L.GetGlobal("n"); got != "bob" {
		t.Errorf("got %v, want bob", got)
	}
}

func TestNestedStructsMapsAndSlices(t *testing.T) {
	L := newState(t)
	v := map[string]any{
		"players": []testPlayer{{Name: "a", Score: 1}, {Name: "b", Score: 2}},
		"origin":  testPos{3, 4},
	}
	if err := L.SetGlobal("v", v); err != nil {
		t.Fatal(err)
	}
	L.DoString(`out = v.players[2].Name .. v.players[2].score .. v.origin.Y`)
	if got, _ := L.GetGlobal("out"); got != "b24" {
		t.Errorf("got %v, want b24", got)
	}
}

func TestDecodeStruct(t *testing.T) {
	L := newState(t)
	err := L.DoString(`v = {
		Name = "bob", score = 7, tags = {"x", "y"}, Pos = {X = 1.5, Y = 2},
		extra = function() end, other = 99 }`)
	if err != nil {
		t.Fatal(err)
	}

	p := testPlayer{Secret: "keep"}
	if err := decodeGlobal(t, L, "v", &p); err != nil {
		t.Fatal(err)
	}
	want := testPlayer{Name: "bob", Score: 7, Tags: []string{"x", "y"}, Secret: "keep", Pos: testPos{1.5, 2}}
	if !reflect.DeepEqual(p, want) {
		t.Errorf("got %+v\nwant %+v", p, want)
	}
	if L.Top() != 0 {
		t.Errorf("stack not clean: %d", L.Top())
	}
}

func TestDecodeKeepsFieldsTheTableLacks(t *testing.T) {
	L := newState(t)
	cfg := struct {
		Port int
		Host string
	}{Port: 8080, Host: "localhost"}

	L.DoString(`c = {Host = "example.com"}`)
	if err := decodeGlobal(t, L, "c", &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 8080 || cfg.Host != "example.com" {
		t.Errorf("got %+v, want the default port and the new host", cfg)
	}
}

func TestStructRoundTrip(t *testing.T) {
	L := newState(t)
	in := testPlayer{Name: "alice", Score: 10, Tags: []string{"a"}, Pos: testPos{1, 2}}
	if err := L.Push(in); err != nil {
		t.Fatal(err)
	}
	var out testPlayer
	if err := L.Decode(-1, &out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Errorf("got %+v, want %+v", out, in)
	}
}

func TestDecodeSlicesAndMaps(t *testing.T) {
	L := newState(t)
	err := L.DoString(`
		ns = {1, 2, 3}
		words = {"a", "b"}
		counts = {a = 1, b = 2}
		byid = {[10] = "x", [20] = "y"}
		fixed = {5, 6}
		empty = {}`)
	if err != nil {
		t.Fatal(err)
	}

	var ns []int
	var words []string
	var counts map[string]int
	var byid map[int]string
	var fixed [3]int
	var empty []int
	for name, target := range map[string]any{
		"ns": &ns, "words": &words, "counts": &counts, "byid": &byid, "fixed": &fixed, "empty": &empty,
	} {
		if err := decodeGlobal(t, L, name, target); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if !reflect.DeepEqual(ns, []int{1, 2, 3}) ||
		!reflect.DeepEqual(words, []string{"a", "b"}) ||
		!reflect.DeepEqual(counts, map[string]int{"a": 1, "b": 2}) ||
		!reflect.DeepEqual(byid, map[int]string{10: "x", 20: "y"}) ||
		fixed != [3]int{5, 6, 0} ||
		empty == nil || len(empty) != 0 {
		t.Errorf("ns=%v words=%v counts=%v byid=%v fixed=%v empty=%#v", ns, words, counts, byid, fixed, empty)
	}
}

func TestDecodeBytesAnyAndPointers(t *testing.T) {
	L := newState(t)
	L.DoString(`s = "hi" mixed = {1, "x"} w = {N = 5}`)

	var b []byte
	if err := decodeGlobal(t, L, "s", &b); err != nil || string(b) != "hi" {
		t.Errorf("bytes: %q, %v", b, err)
	}

	var a any
	if err := decodeGlobal(t, L, "mixed", &a); err != nil || !reflect.DeepEqual(a, []any{1.0, "x"}) {
		t.Errorf("any: %#v, %v", a, err)
	}

	type withPtr struct {
		N *int
		S *string
	}
	var w withPtr
	if err := decodeGlobal(t, L, "w", &w); err != nil {
		t.Fatal(err)
	}
	if w.N == nil || *w.N != 5 || w.S != nil {
		t.Errorf("got %+v, want N=5 and S=nil", w)
	}
}

func TestDecodeErrors(t *testing.T) {
	L := newState(t)
	err := L.DoString(`
		str = "hi"
		frac = 1.5
		big = 300
		obj = {a = 1}
		long = {1, 2, 3}
		holes = {[1] = "a", [3] = "c"}
		tablekey = {} tablekey[{}] = 1
		badscore = {score = "high"}
		badpos = {Pos = {X = "far"}}`)
	if err != nil {
		t.Fatal(err)
	}

	var i int
	var i8 int8
	var ns []int
	var ss []string
	var two [2]int
	var anyMap map[any]any
	var p testPlayer
	for _, c := range []struct {
		name   string
		global string
		target any
		want   string // text the error must contain
	}{
		{"string into int", "str", &i, "string"},
		{"fraction into int", "frac", &i, "1.5"},
		{"overflow into int8", "big", &i8, "int8"},
		{"object into slice", "obj", &ns, "array"},
		{"too many for array", "long", &two, "3 entries"},
		{"holes into slice", "holes", &ss, "array"},
		{"table key into map", "tablekey", &anyMap, "key"},
		{"wrong field type", "badscore", &p, "score"},
		{"nested path", "badpos", &p, "Pos.X"},
	} {
		err := decodeGlobal(t, L, c.global, c.target)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want an error containing %q", c.name, err, c.want)
		}
	}

	L.Push(1)
	if err := L.Decode(-1, 5); err == nil {
		t.Error("a non-pointer target should fail")
	}
	var np *int
	if err := L.Decode(-1, np); err == nil {
		t.Error("a nil pointer target should fail")
	}
	if err := L.Decode(99, &i); err == nil {
		t.Error("an invalid index should fail")
	}
	L.Pop(1)
	if L.Top() != 0 {
		t.Errorf("stack not clean: %d", L.Top())
	}
}

func TestDecodeCyclicTableFailsCleanly(t *testing.T) {
	L := newState(t)
	type node struct{ Next *node }
	L.DoString(`n = {} n.Next = n`)

	var n node
	if err := decodeGlobal(t, L, "n", &n); err == nil {
		t.Error("a table containing itself should fail")
	}
	if L.Top() != 0 {
		t.Errorf("stack not clean: %d", L.Top())
	}
}
