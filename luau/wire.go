package luau

/*
#include "bridge.h"
*/
import "C"

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"reflect"
	"unsafe"
)

// The wire format is described in bridge.cpp. Go writes it to push a value,
// and Luau writes it so Go can read a value, each in a single cgo call.
const (
	wNil byte = iota
	wFalse
	wTrue
	wNum
	wStr
	wArr
	wMap
	wBad
	wDeep
)

// ---- Go value -> wire ------------------------------------------------------

type wireWriter struct{ buf []byte }

func (w *wireWriter) tag(b byte) { w.buf = append(w.buf, b) }
func (w *wireWriter) u32(n int)  { w.buf = binary.LittleEndian.AppendUint32(w.buf, uint32(n)) }

func (w *wireWriter) str(s string) {
	w.tag(wStr)
	w.u32(len(s))
	w.buf = append(w.buf, s...)
}

func (w *wireWriter) num(f float64) {
	w.tag(wNum)
	w.buf = binary.LittleEndian.AppendUint64(w.buf, math.Float64bits(f))
}

func (w *wireWriter) patchCount(at, n int) { binary.LittleEndian.PutUint32(w.buf[at:], uint32(n)) }

// value writes rv. depth counts the levels of nesting so far.
func (w *wireWriter) value(rv reflect.Value, depth int) error {
	for rv.Kind() == reflect.Interface || rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			w.tag(wNil)
			return nil
		}
		if rv.Kind() == reflect.Pointer {
			if depth >= maxConvertDepth {
				return errTooDeep
			}
			depth++
		}
		rv = rv.Elem()
	}

	switch rv.Kind() {
	case reflect.Invalid:
		w.tag(wNil)
	case reflect.Bool:
		if rv.Bool() {
			w.tag(wTrue)
		} else {
			w.tag(wFalse)
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		w.num(float64(rv.Int()))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		w.num(float64(rv.Uint()))
	case reflect.Float32, reflect.Float64:
		w.num(rv.Float())
	case reflect.String:
		w.str(rv.String())
	case reflect.Slice, reflect.Array:
		return w.list(rv, depth)
	case reflect.Map:
		return w.mapValue(rv, depth)
	case reflect.Struct:
		return w.structValue(rv, depth)
	default:
		return fmt.Errorf("luau: cannot push %s", rv.Type())
	}
	return nil
}

func (w *wireWriter) list(rv reflect.Value, depth int) error {
	if depth >= maxConvertDepth {
		return errTooDeep
	}
	if rv.Kind() == reflect.Slice && rv.Type().Elem().Kind() == reflect.Uint8 {
		b := rv.Bytes() // a []byte becomes a string
		w.tag(wStr)
		w.u32(len(b))
		w.buf = append(w.buf, b...)
		return nil
	}
	n := rv.Len()
	w.tag(wArr)
	w.u32(n)
	for i := 0; i < n; i++ {
		if err := w.value(rv.Index(i), depth+1); err != nil {
			return err
		}
	}
	return nil
}

func (w *wireWriter) mapValue(rv reflect.Value, depth int) error {
	if depth >= maxConvertDepth {
		return errTooDeep
	}
	w.tag(wMap)
	w.u32(rv.Len())
	iter := rv.MapRange()
	for iter.Next() {
		if err := w.key(iter.Key()); err != nil {
			return err
		}
		if err := w.value(iter.Value(), depth+1); err != nil {
			return err
		}
	}
	return nil
}

// key writes a table key, which can be a string, a number or a boolean.
func (w *wireWriter) key(k reflect.Value) error {
	for k.Kind() == reflect.Interface {
		if k.IsNil() {
			return errors.New("luau: a table key can't be nil")
		}
		k = k.Elem()
	}
	switch k.Kind() {
	case reflect.String:
		w.str(k.String())
	case reflect.Bool:
		if k.Bool() {
			w.tag(wTrue)
		} else {
			w.tag(wFalse)
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		w.num(float64(k.Int()))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		w.num(float64(k.Uint()))
	case reflect.Float32, reflect.Float64:
		f := k.Float()
		if math.IsNaN(f) {
			return errors.New("luau: a table key can't be NaN")
		}
		w.num(f)
	default:
		return fmt.Errorf("luau: cannot use %s as a table key", k.Type())
	}
	return nil
}

func (w *wireWriter) structValue(rv reflect.Value, depth int) error {
	if depth >= maxConvertDepth {
		return errTooDeep
	}
	w.tag(wMap)
	countAt := len(w.buf)
	w.u32(0)
	n := 0
	for _, f := range cachedFields(rv.Type()) {
		fv := rv.Field(f.index)
		if f.omitEmpty && fv.IsZero() {
			continue
		}
		w.str(f.name)
		if err := w.value(fv, depth+1); err != nil {
			return fmt.Errorf("luau: field %s: %w", f.name, err)
		}
		n++
	}
	w.patchCount(countAt, n)
	return nil
}

// Encoded is a Go value that has been flattened, ready to be pushed into any
// State with PushEncoded. Making it needs no State, so it can be done on any
// goroutine. An Encoded is read-only and can be pushed any number of times.
type Encoded struct{ b []byte }

// Encode flattens v, which can be anything Push accepts. It does all the
// reflection and allocation up front, so a goroutine that doesn't own the
// State can do that work, and the goroutine that does own it only has to
// call PushEncoded, which is one cgo call.
func Encode(v any) (*Encoded, error) {
	w := wireWriter{buf: make([]byte, 0, 256)}
	if err := w.value(reflect.ValueOf(v), 0); err != nil {
		return nil, err
	}
	return &Encoded{b: w.buf}, nil
}

// PushEncoded pushes a value made by Encode, in a single cgo call.
func (s *State) PushEncoded(e *Encoded) error {
	if !s.open() {
		return ErrClosed
	}
	if e == nil || len(e.b) == 0 {
		return errors.New("luau: PushEncoded needs a value made by Encode")
	}
	return s.pushWire(e.b)
}

// pushEncoded is Push for a Go value that becomes a table: it flattens it
// into a buffer that the main state keeps for reuse.
func (s *State) pushEncoded(v any) error {
	root := s.root()
	w := wireWriter{buf: root.wire[:0]}
	err := w.value(reflect.ValueOf(v), 0)
	if err == nil {
		err = s.pushWire(w.buf)
	}
	if cap(w.buf) <= 1<<20 {
		root.wire = w.buf[:0]
	} else {
		root.wire = nil // don't keep a big buffer around
	}
	return err
}

func (s *State) pushWire(b []byte) error {
	switch C.bridge_pushencoded(s.l, (*C.char)(unsafe.Pointer(&b[0])), C.size_t(len(b))) {
	case 0:
		return nil
	case -1:
		return s.pushError()
	}
	return errMalformed
}

// ---- wire -> Go value ------------------------------------------------------

const (
	nNil   byte = iota
	nBool       // b
	nNum        // n
	nStr        // s
	nTable      // pairs
	nBad        // a value that can't be converted; s is its Lua type name
	nDeep       // a table nested too deeply
)

// node is a Lua value that has been read from the wire format.
type node struct {
	kind  byte
	b     bool
	n     float64
	s     string
	pairs []pair
}

type pair struct{ key, val node }

// luaType is the Lua type name of the value.
func (n *node) luaType() string {
	switch n.kind {
	case nNil:
		return "nil"
	case nBool:
		return "boolean"
	case nNum:
		return "number"
	case nStr:
		return "string"
	case nBad:
		return n.s
	}
	return "table" // a table, including one that is nested too deeply
}

// toAny converts the value the way Get does.
func (n *node) toAny() (any, error) {
	switch n.kind {
	case nNil:
		return nil, nil
	case nBool:
		return n.b, nil
	case nNum:
		return n.n, nil
	case nStr:
		return n.s, nil
	case nBad:
		return nil, fmt.Errorf("luau: unsupported type %s", n.s)
	case nDeep:
		return nil, errTooDeep
	}
	entries := make([]tableEntry, 0, len(n.pairs))
	for i := range n.pairs {
		p := &n.pairs[i]
		switch p.key.kind {
		case nStr, nNum, nBool:
		default:
			return nil, fmt.Errorf("luau: cannot convert a table that has a %s key", p.key.luaType())
		}
		k, _ := p.key.toAny()
		v, err := p.val.toAny()
		if err != nil {
			return nil, err
		}
		entries = append(entries, tableEntry{k, v})
	}
	return classify(entries), nil
}

// wireReader reads the wire format from a string, so the strings it hands
// out are slices of it and nothing is copied for each one.
type wireReader struct {
	s   string
	pos int
}

func (r *wireReader) u32() (int, bool) {
	if len(r.s)-r.pos < 4 {
		return 0, false
	}
	p := r.s[r.pos:]
	v := uint32(p[0]) | uint32(p[1])<<8 | uint32(p[2])<<16 | uint32(p[3])<<24
	r.pos += 4
	return int(v), true
}

func (r *wireReader) bytes() (string, bool) {
	n, ok := r.u32()
	if !ok || len(r.s)-r.pos < n {
		return "", false
	}
	out := r.s[r.pos : r.pos+n]
	r.pos += n
	return out, true
}

func (r *wireReader) node() (node, bool) {
	if r.pos >= len(r.s) {
		return node{}, false
	}
	tag := r.s[r.pos]
	r.pos++
	switch tag {
	case wNil:
		return node{kind: nNil}, true
	case wFalse:
		return node{kind: nBool}, true
	case wTrue:
		return node{kind: nBool, b: true}, true
	case wNum:
		if len(r.s)-r.pos < 8 {
			return node{}, false
		}
		p := r.s[r.pos:]
		bits := uint64(p[0]) | uint64(p[1])<<8 | uint64(p[2])<<16 | uint64(p[3])<<24 |
			uint64(p[4])<<32 | uint64(p[5])<<40 | uint64(p[6])<<48 | uint64(p[7])<<56
		r.pos += 8
		return node{kind: nNum, n: math.Float64frombits(bits)}, true
	case wStr:
		s, ok := r.bytes()
		return node{kind: nStr, s: s}, ok
	case wBad:
		s, ok := r.bytes()
		return node{kind: nBad, s: s}, ok
	case wDeep:
		return node{kind: nDeep}, true
	case wMap:
		n, ok := r.u32()
		if !ok || n > len(r.s)-r.pos {
			return node{}, false
		}
		out := node{kind: nTable, pairs: make([]pair, 0, n)}
		for i := 0; i < n; i++ {
			k, ok := r.node()
			if !ok {
				return node{}, false
			}
			v, ok := r.node()
			if !ok {
				return node{}, false
			}
			out.pairs = append(out.pairs, pair{k, v})
		}
		return out, true
	}
	return node{}, false
}

func parseWire(s string) (node, error) {
	r := wireReader{s: s}
	n, ok := r.node()
	if !ok || r.pos != len(s) {
		return node{}, errMalformed
	}
	return n, nil
}

// rawEncode asks Luau to flatten the value at idx. The bytes belong to Luau
// and are valid until the next call into it, so read or copy them at once.
func (s *State) rawEncode(idx, depth int) ([]byte, error) {
	var data *C.char
	var n C.size_t
	switch C.bridge_encode(s.l, C.int(idx), C.int(maxConvertDepth-depth), &data, &n) {
	case C.BRIDGE_ENC_OK:
		return unsafe.Slice((*byte)(unsafe.Pointer(data)), int(n)), nil
	case C.BRIDGE_ENC_BADINDEX:
		return nil, fmt.Errorf("luau: stack index %d is not valid", idx)
	case C.BRIDGE_ENC_STACK:
		return nil, ErrStackOverflow
	}
	return nil, ErrMemory
}

func (s *State) readNode(idx, depth int) (node, error) {
	raw, err := s.rawEncode(idx, depth)
	if err != nil {
		return node{}, err
	}
	// One copy. Every string in the result is a slice of it, so a decoded
	// string keeps this whole buffer alive for as long as it lives.
	return parseWire(string(raw))
}

// A Snapshot is a copy of a Lua value that no longer depends on the State it
// came from: it can be read after the State is closed, and from any
// goroutine. Take one with State.Snapshot.
type Snapshot struct{ s string }

// Snapshot copies the value at idx, which can be anything Get reads, tables
// included, in a single cgo call. Functions and the like inside a table are
// kept as markers, and only cause an error if you decode them. It doesn't
// pop the value.
//
// Reading the copy (Decode, Any) is plain Go with no cgo calls, so it can
// happen on another goroutine, away from the one that owns the State. Strings
// decoded from a Snapshot are slices of it, so they keep it alive.
func (s *State) Snapshot(idx int) (*Snapshot, error) {
	if !s.open() {
		return nil, ErrClosed
	}
	raw, err := s.rawEncode(idx, 0)
	if err != nil {
		return nil, err
	}
	return &Snapshot{s: string(raw)}, nil
}

// Kind returns the Lua type name of the value: "string", "table", "number"
// and so on.
func (sn *Snapshot) Kind() string {
	if len(sn.s) == 0 {
		return "no value"
	}
	switch sn.s[0] {
	case wNil:
		return "nil"
	case wFalse, wTrue:
		return "boolean"
	case wNum:
		return "number"
	case wStr:
		return "string"
	case wBad:
		if name, ok := (&wireReader{s: sn.s, pos: 1}).bytes(); ok {
			return name
		}
	}
	return "table"
}

// Decode fills the Go value that v points to, with the rules of State.Decode.
func (sn *Snapshot) Decode(v any) error {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return fmt.Errorf("luau: Decode needs a non-nil pointer, got %T", v)
	}
	n, err := parseWire(sn.s)
	if err != nil {
		return err
	}
	return assign(&n, rv.Elem(), 0, nil)
}

// Any converts the value the way State.Get does.
func (sn *Snapshot) Any() (any, error) {
	n, err := parseWire(sn.s)
	if err != nil {
		return nil, err
	}
	return n.toAny()
}
