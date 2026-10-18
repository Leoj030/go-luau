package luau

/*
#include "bridge.h"
*/
import "C"

import (
	"fmt"
	"math"
	"reflect"
	"strings"
)

// maxConvertDepth bounds how deeply nested a value can be when it is
// converted between Go and Luau. It also stops cyclic structures, which
// would otherwise recurse forever.
const maxConvertDepth = 100

var errTooDeep = fmt.Errorf("luau: value is nested more than %d levels deep (is it cyclic?)", maxConvertDepth)

// restore cuts the stack back to base values.
func (s *State) restore(base int) { C.bridge_settop(s.l, C.int(base)) }

// pushComposite pushes what pushValue doesn't handle directly: slices,
// arrays and maps become tables, and numbers, booleans and strings of other
// types (int32, float32, a named type like `type ID int`) are converted by
// their kind.
func (s *State) pushComposite(v any, depth int) error {
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Bool:
		return s.pushValue(rv.Bool(), depth)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return s.pushValue(float64(rv.Int()), depth)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return s.pushValue(float64(rv.Uint()), depth)
	case reflect.Float32, reflect.Float64:
		return s.pushValue(rv.Float(), depth)
	case reflect.String:
		return s.pushValue(rv.String(), depth)
	case reflect.Slice, reflect.Array:
		return s.pushList(rv, depth)
	case reflect.Map:
		return s.pushMap(rv, depth)
	case reflect.Struct:
		return s.pushStruct(rv, depth)
	case reflect.Pointer:
		if rv.IsNil() {
			return s.pushValue(nil, depth)
		}
		if depth >= maxConvertDepth {
			return errTooDeep
		}
		return s.pushValue(rv.Elem().Interface(), depth+1)
	}
	return fmt.Errorf("luau: cannot push %T", v)
}

// structField describes one exported field of a struct and how it is named
// in Luau.
type structField struct {
	index     int    // position in the struct
	name      string // key in the Luau table
	omitEmpty bool   // skip the field when it holds its zero value
}

// structFields lists the fields of t that are converted: the exported ones,
// minus those tagged `luau:"-"`. The `luau` tag renames a field
// (`luau:"score"`), and `luau:"score,omitempty"` also skips it when it
// holds its zero value.
func structFields(t reflect.Type) []structField {
	var fields []structField
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("luau")
		if tag == "-" {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		if name == "" {
			name = f.Name
		}
		fields = append(fields, structField{index: i, name: name, omitEmpty: opts == "omitempty"})
	}
	return fields
}

// pushStruct pushes a struct as a table keyed by field name. On failure the
// caller restores the stack.
func (s *State) pushStruct(rv reflect.Value, depth int) error {
	if depth >= maxConvertDepth {
		return errTooDeep
	}
	fields := structFields(rv.Type())
	if err := s.NewTable(0, len(fields)); err != nil {
		return err
	}
	for _, f := range fields {
		fv := rv.Field(f.index)
		if f.omitEmpty && fv.IsZero() {
			continue
		}
		if err := s.pushValue(f.name, depth+1); err != nil {
			return err
		}
		if err := s.pushValue(fv.Interface(), depth+1); err != nil {
			return fmt.Errorf("luau: field %s: %w", f.name, err)
		}
		if err := s.RawSet(-3); err != nil {
			return err
		}
	}
	return nil
}

// pushList pushes a slice or array as an array table. A []byte becomes a
// string instead. On failure the caller restores the stack.
func (s *State) pushList(rv reflect.Value, depth int) error {
	if depth >= maxConvertDepth {
		return errTooDeep
	}
	if rv.Kind() == reflect.Slice && rv.Type().Elem().Kind() == reflect.Uint8 {
		return s.pushValue(string(rv.Bytes()), depth)
	}
	n := rv.Len()
	if err := s.NewTable(n, 0); err != nil {
		return err
	}
	for i := 0; i < n; i++ {
		if err := s.pushValue(rv.Index(i).Interface(), depth+1); err != nil {
			return err
		}
		if err := s.SetIndex(-2, i+1); err != nil {
			return err
		}
	}
	return nil
}

// pushMap pushes a map as a table. Keys can be strings, numbers or
// booleans. On failure the caller restores the stack.
func (s *State) pushMap(rv reflect.Value, depth int) error {
	if depth >= maxConvertDepth {
		return errTooDeep
	}
	if err := s.NewTable(0, rv.Len()); err != nil {
		return err
	}
	iter := rv.MapRange()
	for iter.Next() {
		if err := s.pushValue(iter.Key().Interface(), depth+1); err != nil {
			return err
		}
		if err := s.pushValue(iter.Value().Interface(), depth+1); err != nil {
			return err
		}
		if err := s.RawSet(-3); err != nil {
			return err
		}
	}
	return nil
}

type tableEntry struct{ key, val any }

// tableToGo converts the table at idx. See Get for the rules.
func (s *State) tableToGo(idx, depth int) (any, error) {
	if depth >= maxConvertDepth {
		return nil, errTooDeep
	}
	var entries []tableEntry
	err := s.ForEach(idx, func() error {
		if kt := s.TypeName(-2); kt != "string" && kt != "number" && kt != "boolean" {
			return fmt.Errorf("luau: cannot convert a table that has a %s key", kt)
		}
		k, err := s.get(-2, depth+1)
		if err != nil {
			return err
		}
		v, err := s.get(-1, depth+1)
		if err != nil {
			return err
		}
		entries = append(entries, tableEntry{k, v})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return classify(entries), nil
}

// classify picks the Go shape for a table's entries: a list, a map with
// string keys, or a map with any keys.
func classify(entries []tableEntry) any {
	n := len(entries)
	if n == 0 {
		return []any{}
	}

	// A sequence: the keys are exactly 1..n.
	list := make([]any, n)
	isList := true
	for _, e := range entries {
		f, ok := e.key.(float64)
		if !ok || f < 1 || f > float64(n) || f != math.Trunc(f) {
			isList = false
			break
		}
		list[int(f)-1] = e.val
	}
	if isList {
		return list
	}

	// Only string keys.
	strs := make(map[string]any, n)
	for _, e := range entries {
		k, ok := e.key.(string)
		if !ok {
			strs = nil
			break
		}
		strs[k] = e.val
	}
	if strs != nil {
		return strs
	}

	all := make(map[any]any, n)
	for _, e := range entries {
		all[e.key] = e.val
	}
	return all
}
