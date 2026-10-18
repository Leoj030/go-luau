package luau

/*
#include "bridge.h"
*/
import "C"

import (
	"fmt"
	"math"
	"reflect"
)

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
func (s *State) Decode(idx int, v any) error {
	if !s.open() {
		return ErrClosed
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return fmt.Errorf("luau: Decode needs a non-nil pointer, got %T", v)
	}
	if !s.valid(idx) {
		return fmt.Errorf("luau: stack index %d is not valid", idx)
	}
	return s.decode(s.abs(idx), rv.Elem(), 0, "")
}

// decode fills dst from the stack slot at the absolute index idx. path is
// where dst sits inside the value being decoded, for error messages.
func (s *State) decode(idx int, dst reflect.Value, depth int, path string) error {
	if depth > maxConvertDepth {
		return errTooDeep
	}
	tn := s.TypeName(idx)

	// An empty interface takes whatever Get would produce.
	if dst.Kind() == reflect.Interface && dst.NumMethod() == 0 {
		v, err := s.get(idx, depth)
		if err != nil {
			return decodeErr(path, err)
		}
		if v == nil {
			dst.Set(reflect.Zero(dst.Type()))
		} else {
			dst.Set(reflect.ValueOf(v))
		}
		return nil
	}

	if tn == "nil" {
		dst.Set(reflect.Zero(dst.Type()))
		return nil
	}

	if dst.Kind() == reflect.Pointer {
		if dst.IsNil() {
			dst.Set(reflect.New(dst.Type().Elem()))
		}
		return s.decode(idx, dst.Elem(), depth+1, path)
	}

	switch dst.Kind() {
	case reflect.Bool:
		if tn != "boolean" {
			return mismatch(path, tn, dst)
		}
		dst.SetBool(C.bridge_toboolean(s.l, C.int(idx)) != 0)
		return nil

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		f, err := s.numberAt(idx, tn, path, dst)
		if err != nil {
			return err
		}
		n, ok := floatToInt(f)
		if !ok || dst.OverflowInt(n) {
			return decodeErr(path, fmt.Errorf("%v isn't a whole number that fits in %s", f, dst.Type()))
		}
		dst.SetInt(n)
		return nil

	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		f, err := s.numberAt(idx, tn, path, dst)
		if err != nil {
			return err
		}
		n, ok := floatToUint(f)
		if !ok || dst.OverflowUint(n) {
			return decodeErr(path, fmt.Errorf("%v isn't a whole number that fits in %s", f, dst.Type()))
		}
		dst.SetUint(n)
		return nil

	case reflect.Float32, reflect.Float64:
		f, err := s.numberAt(idx, tn, path, dst)
		if err != nil {
			return err
		}
		if dst.OverflowFloat(f) {
			return decodeErr(path, fmt.Errorf("%v doesn't fit in %s", f, dst.Type()))
		}
		dst.SetFloat(f)
		return nil

	case reflect.String:
		if tn != "string" {
			return mismatch(path, tn, dst)
		}
		dst.SetString(s.stringAt(idx))
		return nil

	case reflect.Slice:
		if dst.Type().Elem().Kind() == reflect.Uint8 && tn == "string" {
			dst.SetBytes([]byte(s.stringAt(idx)))
			return nil
		}
		return s.decodeList(idx, dst, tn, depth, path)

	case reflect.Array:
		return s.decodeList(idx, dst, tn, depth, path)

	case reflect.Map:
		return s.decodeMap(idx, dst, tn, depth, path)

	case reflect.Struct:
		return s.decodeStruct(idx, dst, tn, depth, path)
	}
	return decodeErr(path, fmt.Errorf("can't decode into %s", dst.Type()))
}

func (s *State) decodeList(idx int, dst reflect.Value, tn string, depth int, path string) error {
	if tn != "table" {
		return mismatch(path, tn, dst)
	}

	// First pass: count the entries, so the slice can be sized.
	count := 0
	if err := s.ForEach(idx, func() error { count++; return nil }); err != nil {
		return decodeErr(path, err)
	}
	var out reflect.Value
	if dst.Kind() == reflect.Slice {
		out = reflect.MakeSlice(dst.Type(), count, count)
	} else {
		if count > dst.Len() {
			return decodeErr(path, fmt.Errorf("the table has %d entries, but %s holds %d", count, dst.Type(), dst.Len()))
		}
		out = reflect.New(dst.Type()).Elem()
	}

	// Second pass: the keys must be exactly 1..count.
	seen := make([]bool, count)
	err := s.ForEach(idx, func() error {
		if kt := s.TypeName(-2); kt != "number" {
			return decodeErr(path, fmt.Errorf("the table isn't an array: it has a %s key", kt))
		}
		var ok C.int
		f := float64(C.bridge_tonumber(s.l, -2, &ok))
		if f != math.Trunc(f) || f < 1 || f > float64(count) || seen[int(f)-1] {
			return decodeErr(path, fmt.Errorf("the table isn't an array: key %v is out of place", f))
		}
		seen[int(f)-1] = true
		return s.decode(s.Top(), out.Index(int(f)-1), depth+1, fmt.Sprintf("%s[%d]", path, int(f)))
	})
	if err != nil {
		return err
	}
	dst.Set(out)
	return nil
}

func (s *State) decodeMap(idx int, dst reflect.Value, tn string, depth int, path string) error {
	if tn != "table" {
		return mismatch(path, tn, dst)
	}
	kt, vt := dst.Type().Key(), dst.Type().Elem()
	out := reflect.MakeMap(dst.Type())
	err := s.ForEach(idx, func() error {
		if kn := s.TypeName(-2); kn != "string" && kn != "number" && kn != "boolean" {
			return decodeErr(path, fmt.Errorf("can't use a %s key in a Go map", kn))
		}
		key := reflect.New(kt).Elem()
		if err := s.decode(s.Top()-1, key, depth+1, path); err != nil {
			return err
		}
		val := reflect.New(vt).Elem()
		if err := s.decode(s.Top(), val, depth+1, joinPath(path, fmt.Sprint(key.Interface()))); err != nil {
			return err
		}
		out.SetMapIndex(key, val)
		return nil
	})
	if err != nil {
		return err
	}
	dst.Set(out)
	return nil
}

func (s *State) decodeStruct(idx int, dst reflect.Value, tn string, depth int, path string) error {
	if tn != "table" {
		return mismatch(path, tn, dst)
	}
	fields := map[string]structField{}
	for _, f := range structFields(dst.Type()) {
		fields[f.name] = f
	}
	return s.ForEach(idx, func() error {
		if s.TypeName(-2) != "string" {
			return nil // only a string key can name a field
		}
		name := s.stringAt(s.Top() - 1)
		f, ok := fields[name]
		if !ok {
			return nil // unknown keys are ignored, and never converted
		}
		return s.decode(s.Top(), dst.Field(f.index), depth+1, joinPath(path, name))
	})
}

// numberAt reads the number at idx, or explains why it isn't one.
func (s *State) numberAt(idx int, tn, path string, dst reflect.Value) (float64, error) {
	if tn != "number" {
		return 0, mismatch(path, tn, dst)
	}
	var ok C.int
	return float64(C.bridge_tonumber(s.l, C.int(idx), &ok)), nil
}

// stringAt copies the string at idx, which must be a string.
func (s *State) stringAt(idx int) string {
	var n C.size_t
	p := C.bridge_tostring(s.l, C.int(idx), &n)
	return C.GoStringN(p, C.int(n))
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

func joinPath(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

func mismatch(path, tn string, dst reflect.Value) error {
	return decodeErr(path, fmt.Errorf("can't decode a Luau %s into %s", tn, dst.Type()))
}

func decodeErr(path string, err error) error {
	if path == "" {
		return fmt.Errorf("luau: decode: %w", err)
	}
	return fmt.Errorf("luau: decode %s: %w", path, err)
}
