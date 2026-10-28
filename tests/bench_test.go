package tests

import (
	"testing"

	"github.com/Leoj030/go-luau/luau"
)

func benchState(b *testing.B) *luau.State {
	b.Helper()
	L, err := luau.NewState()
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(L.Close)
	return L
}

var benchRequest = fastReq{
	Method:  "GET",
	Path:    "/",
	Query:   map[string]string{"a": "1", "b": "2"},
	Headers: map[string]string{"host": "localhost", "user-agent": "wrk", "accept": "*/*"},
}

func BenchmarkPushRequest(b *testing.B) {
	L := benchState(b)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		L.Push(benchRequest)
		L.Pop(1)
	}
}

func BenchmarkPushEncodedRequest(b *testing.B) {
	L := benchState(b)
	enc, _ := luau.Encode(benchRequest)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		L.PushEncoded(enc)
		L.Pop(1)
	}
}

func BenchmarkDecodeResponse(b *testing.B) {
	L := benchState(b)
	L.DoString(`resp = {status = 200, headers = {["content-type"] = "text/plain", x = "y"}, body = "hello"}`)
	L.PushGlobal("resp")
	var out fastResp
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		L.Decode(-1, &out)
	}
}

func BenchmarkSnapshotResponse(b *testing.B) {
	L := benchState(b)
	L.DoString(`resp = {status = 200, headers = {["content-type"] = "text/plain", x = "y"}, body = "hello"}`)
	L.PushGlobal("resp")
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		L.Snapshot(-1)
	}
}
