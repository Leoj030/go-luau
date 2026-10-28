package tests

import (
	"reflect"
	"strings"
	"testing"
)

func TestDecodeErrorPathsAreStillReported(t *testing.T) {
	L := newState(t)
	L.DoString(`a = {list = {1, "x"}} b = {m = {k = {j = true}}}`)

	var a struct {
		List []int `luau:"list"`
	}
	if err := decodeGlobal(t, L, "a", &a); err == nil || !strings.Contains(err.Error(), "list[2]") {
		t.Errorf("got %v, want an error naming list[2]", err)
	}

	var b struct {
		M map[string]map[string]string `luau:"m"`
	}
	if err := decodeGlobal(t, L, "b", &b); err == nil || !strings.Contains(err.Error(), "m.k.j") {
		t.Errorf("got %v, want an error naming m.k.j", err)
	}
}

func TestDecodeStringMaps(t *testing.T) {
	L := newState(t)
	L.DoString(`h = {["content-type"] = "text/plain", x = "y"} bad = {a = "ok", b = 5}`)

	var h map[string]string
	want := map[string]string{"content-type": "text/plain", "x": "y"}
	if err := decodeGlobal(t, L, "h", &h); err != nil || !reflect.DeepEqual(h, want) {
		t.Errorf("got %v, %v", h, err)
	}

	var bad map[string]string
	if err := decodeGlobal(t, L, "bad", &bad); err == nil || !strings.Contains(err.Error(), "decode b") {
		t.Errorf("got %v, want an error about key b", err)
	}
}
