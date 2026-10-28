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
	"sync"
)

// maxConvertDepth bounds how deeply nested a value can be when it is
// converted between Go and Luau. It also stops cyclic structures, which
// would otherwise recurse forever.
const maxConvertDepth = 100

var errTooDeep = fmt.Errorf("luau: value is nested more than %d levels deep (is it cyclic?)", maxConvertDepth)

// restore cuts the stack back to base values.
func (s *State) restore(base int) { C.bridge_settop(s.l, C.int(base)) }

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

var (
	fieldsCache   sync.Map // reflect.Type -> []structField
	fieldMapCache sync.Map // reflect.Type -> map[string]structField
)

// cachedFields is structFields, computed once per type.
func cachedFields(t reflect.Type) []structField {
	if v, ok := fieldsCache.Load(t); ok {
		return v.([]structField)
	}
	f := structFields(t)
	fieldsCache.Store(t, f)
	return f
}

// fieldMap returns the fields of t by their Luau name, computed once per type.
func fieldMap(t reflect.Type) map[string]structField {
	if v, ok := fieldMapCache.Load(t); ok {
		return v.(map[string]structField)
	}
	m := map[string]structField{}
	for _, f := range cachedFields(t) {
		m[f.name] = f
	}
	fieldMapCache.Store(t, m)
	return m
}

type tableEntry struct{ key, val any }

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
