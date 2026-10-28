package luau

import (
	"fmt"
	"math"
	"reflect"
	"strconv"
)

var stringMapType = reflect.TypeOf(map[string]string(nil))

// Decode reads the value at stack index idx into the Go value that v points
// to, which can be a struct, map, slice, array, pointer, string, bool or
// any number type. It doesn't pop the value.
//
// Structs are filled from a table: each string key sets the field with
// that name (see Push for how names and `luau` tags work). Names are
// matched exactly. Fields the table doesn't have keep the value they had,
// so you can set defaults before decoding, and table keys that match no
// field are ignored without being converted. Fields that Push skips are
// skipped here too.
//
// Slices and arrays need a table whose keys are exactly 1..n, and an array
// can't be shorter than the table. Maps are replaced with a new map and
// accept string, number and boolean keys. A number becomes an integer only
// if it is whole and fits. A string can fill a []byte, and a pointer is
// allocated when the value isn't nil. A value of nil sets the target to its
// zero value. An `any` target gets what Get would return.
//
// The value is read raw and nested up to 100 levels deep. Errors name the
// path to the value that failed, such as "Pos.X" or "tags[2]".
//
// The whole value is read in a single cgo call. To do the filling on another
// goroutine, take a Snapshot and decode that.
func (s *State) Decode(idx int, v any) error {
	if !s.open() {
		return ErrClosed
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return fmt.Errorf("luau: Decode needs a non-nil pointer, got %T", v)
	}
	n, err := s.readNode(idx, 0)
	if err != nil {
		return err
	}
	return assign(&n, rv.Elem(), 0, nil)
}

// paths says where a value sits inside the value being decoded. It is only
// turned into text when an error needs it, so decoding a good value never
// formats a string.
type paths struct {
	parent *paths
	field  string        // a struct field
	index  int           // an array position (1-based), used when there is no field and no key
	key    reflect.Value // a map key, used when it is valid
}

func (p *paths) String() string {
	if p == nil {
		return ""
	}
	base := p.parent.String()
	switch {
	case p.key.IsValid():
		return joinPath(base, fmt.Sprint(p.key.Interface()))
	case p.field != "":
		return joinPath(base, p.field)
	}
	return base + "[" + strconv.Itoa(p.index) + "]"
}

// assign fills dst from n. at is where dst sits inside the value being
// decoded, for error messages.
func assign(n *node, dst reflect.Value, depth int, at *paths) error {
	if depth > maxConvertDepth {
		return errTooDeep
	}

	// An empty interface takes whatever Get would produce.
	if dst.Kind() == reflect.Interface && dst.NumMethod() == 0 {
		v, err := n.toAny()
		if err != nil {
			return decodeErr(at, err)
		}
		if v == nil {
			dst.Set(reflect.Zero(dst.Type()))
		} else {
			dst.Set(reflect.ValueOf(v))
		}
		return nil
	}

	switch n.kind {
	case nNil:
		dst.Set(reflect.Zero(dst.Type()))
		return nil
	case nDeep:
		return decodeErr(at, errTooDeep)
	}

	if dst.Kind() == reflect.Pointer {
		if dst.IsNil() {
			dst.Set(reflect.New(dst.Type().Elem()))
		}
		return assign(n, dst.Elem(), depth+1, at)
	}

	switch dst.Kind() {
	case reflect.Bool:
		if n.kind != nBool {
			return mismatch(at, n.luaType(), dst)
		}
		dst.SetBool(n.b)

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if n.kind != nNum {
			return mismatch(at, n.luaType(), dst)
		}
		i, ok := floatToInt(n.n)
		if !ok || dst.OverflowInt(i) {
			return decodeErr(at, fmt.Errorf("%v isn't a whole number that fits in %s", n.n, dst.Type()))
		}
		dst.SetInt(i)

	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if n.kind != nNum {
			return mismatch(at, n.luaType(), dst)
		}
		u, ok := floatToUint(n.n)
		if !ok || dst.OverflowUint(u) {
			return decodeErr(at, fmt.Errorf("%v isn't a whole number that fits in %s", n.n, dst.Type()))
		}
		dst.SetUint(u)

	case reflect.Float32, reflect.Float64:
		if n.kind != nNum {
			return mismatch(at, n.luaType(), dst)
		}
		if dst.OverflowFloat(n.n) {
			return decodeErr(at, fmt.Errorf("%v doesn't fit in %s", n.n, dst.Type()))
		}
		dst.SetFloat(n.n)

	case reflect.String:
		if n.kind != nStr {
			return mismatch(at, n.luaType(), dst)
		}
		dst.SetString(n.s)

	case reflect.Slice:
		if dst.Type().Elem().Kind() == reflect.Uint8 && n.kind == nStr {
			dst.SetBytes([]byte(n.s))
			return nil
		}
		return assignList(n, dst, depth, at)

	case reflect.Array:
		return assignList(n, dst, depth, at)

	case reflect.Map:
		return assignMap(n, dst, depth, at)

	case reflect.Struct:
		return assignStruct(n, dst, depth, at)

	default:
		return decodeErr(at, fmt.Errorf("can't decode into %s", dst.Type()))
	}
	return nil
}

func assignList(n *node, dst reflect.Value, depth int, at *paths) error {
	if n.kind != nTable {
		return mismatch(at, n.luaType(), dst)
	}
	count := len(n.pairs)
	var out reflect.Value
	if dst.Kind() == reflect.Slice {
		out = reflect.MakeSlice(dst.Type(), count, count)
	} else {
		if count > dst.Len() {
			return decodeErr(at, fmt.Errorf("the table has %d entries, but %s holds %d", count, dst.Type(), dst.Len()))
		}
		out = reflect.New(dst.Type()).Elem()
	}

	// The keys must be exactly 1..count.
	seen := make([]bool, count)
	for i := range n.pairs {
		p := &n.pairs[i]
		if p.key.kind != nNum {
			return decodeErr(at, fmt.Errorf("the table isn't an array: it has a %s key", p.key.luaType()))
		}
		f := p.key.n
		if f != math.Trunc(f) || f < 1 || f > float64(count) || seen[int(f)-1] {
			return decodeErr(at, fmt.Errorf("the table isn't an array: key %v is out of place", f))
		}
		seen[int(f)-1] = true
		child := paths{parent: at, index: int(f)}
		if err := assign(&p.val, out.Index(int(f)-1), depth+1, &child); err != nil {
			return err
		}
	}
	dst.Set(out)
	return nil
}

func assignMap(n *node, dst reflect.Value, depth int, at *paths) error {
	if n.kind != nTable {
		return mismatch(at, n.luaType(), dst)
	}

	// The common case, strings to strings (headers, query parameters), needs
	// no reflection per entry.
	if dst.Type() == stringMapType {
		if m, ok := stringMap(n); ok {
			dst.Set(reflect.ValueOf(m))
			return nil
		}
		// Anything else is decoded below, which also explains what is wrong.
	}

	kt, vt := dst.Type().Key(), dst.Type().Elem()
	out := reflect.MakeMapWithSize(dst.Type(), len(n.pairs))
	key := reflect.New(kt).Elem() // reused for every entry: SetMapIndex copies them
	val := reflect.New(vt).Elem()
	for i := range n.pairs {
		p := &n.pairs[i]
		switch p.key.kind {
		case nStr, nNum, nBool:
		default:
			return decodeErr(at, fmt.Errorf("can't use a %s key in a Go map", p.key.luaType()))
		}
		key.SetZero()
		if err := assign(&p.key, key, depth+1, at); err != nil {
			return err
		}
		val.SetZero()
		entry := paths{parent: at, key: key}
		if err := assign(&p.val, val, depth+1, &entry); err != nil {
			return err
		}
		out.SetMapIndex(key, val)
	}
	dst.Set(out)
	return nil
}

// stringMap reads a table whose keys and values are all strings straight
// into a map. It reports false if the table is anything else.
func stringMap(n *node) (map[string]string, bool) {
	m := make(map[string]string, len(n.pairs))
	for i := range n.pairs {
		p := &n.pairs[i]
		if p.key.kind != nStr || p.val.kind != nStr {
			return nil, false
		}
		m[p.key.s] = p.val.s
	}
	return m, true
}

func assignStruct(n *node, dst reflect.Value, depth int, at *paths) error {
	if n.kind != nTable {
		return mismatch(at, n.luaType(), dst)
	}
	fields := fieldMap(dst.Type())
	for i := range n.pairs {
		p := &n.pairs[i]
		if p.key.kind != nStr {
			continue // only a string key can name a field
		}
		f, ok := fields[p.key.s]
		if !ok {
			continue // unknown keys are ignored, and never converted
		}
		child := paths{parent: at, field: p.key.s}
		if err := assign(&p.val, dst.Field(f.index), depth+1, &child); err != nil {
			return err
		}
	}
	return nil
}

func floatToInt(f float64) (int64, bool) {
	if f != math.Trunc(f) || f < -9223372036854775808 || f >= 9223372036854775808 {
		return 0, false
	}
	return int64(f), true
}

func floatToUint(f float64) (uint64, bool) {
	if f != math.Trunc(f) || f < 0 || f >= 18446744073709551616 {
		return 0, false
	}
	return uint64(f), true
}

func joinPath(base, key string) string {
	if base == "" {
		return key
	}
	return base + "." + key
}

func mismatch(at *paths, tn string, dst reflect.Value) error {
	return decodeErr(at, fmt.Errorf("can't decode a Luau %s into %s", tn, dst.Type()))
}

func decodeErr(at *paths, err error) error {
	if p := at.String(); p != "" {
		return fmt.Errorf("luau: decode %s: %w", p, err)
	}
	return fmt.Errorf("luau: decode: %w", err)
}
