// Package fixture builds display-oriented values from Go types. Native
// stories use these generic functions directly; the storybook previewer
// binds the same import path into minigo with script-aware implementations.
package fixture

import (
	"math"
	"reflect"
	"strings"
)

type mode int

const (
	modeEmpty mode = iota
	modeMinimal
	modeMaximal
	modeLongText
)

// Empty returns T's zero value: empty strings, nil pointers, slices and maps.
func Empty[T any]() T { var z T; return z }

// Minimal returns a small non-empty value of T: scalars are non-zero,
// pointers are allocated, and slices and maps receive one generated element.
func Minimal[T any]() T { return build[T](modeMinimal) }

// Maximal returns a large value of T: numeric fields use their maximum,
// strings are long, and slices and maps receive several generated elements.
func Maximal[T any]() T { return build[T](modeMaximal) }

// LongText is Minimal except strings use a long mixed-script, hard-to-wrap text.
func LongText[T any]() T { return build[T](modeLongText) }

func build[T any](m mode) T {
	var z T
	out := buildValue(reflect.TypeOf(z), m, 0, map[reflect.Type]bool{})
	if !out.IsValid() {
		return z
	}
	if v, ok := out.Interface().(T); ok {
		return v
	}
	return z
}

func buildValue(t reflect.Type, m mode, depth int, seen map[reflect.Type]bool) reflect.Value {
	if t == nil || depth > 5 {
		return reflect.Value{}
	}
	if m == modeEmpty {
		return reflect.Zero(t)
	}
	if seen[t] {
		return reflect.Zero(t)
	}
	seen[t] = true
	defer delete(seen, t)
	switch t.Kind() {
	case reflect.Pointer:
		if seen[t.Elem()] {
			return reflect.Zero(t)
		}
		p := reflect.New(t.Elem())
		if v := buildValue(t.Elem(), m, depth+1, seen); v.IsValid() {
			p.Elem().Set(v)
		}
		if p.Type() != t {
			return p.Convert(t)
		}
		return p
	case reflect.Struct:
		v := reflect.New(t).Elem()
		for i := range t.NumField() {
			f := v.Field(i)
			if !f.CanSet() {
				continue
			}
			if fv := buildValue(t.Field(i).Type, m, depth+1, seen); fv.IsValid() {
				f.Set(fv)
			}
		}
		return v
	case reflect.Slice:
		n := countFor(t, m)
		v := reflect.MakeSlice(t, n, n)
		for i := range n {
			if ev := buildValue(t.Elem(), m, depth+1, seen); ev.IsValid() {
				v.Index(i).Set(ev)
			}
		}
		return v
	case reflect.Array:
		v := reflect.New(t).Elem()
		for i := range t.Len() {
			if ev := buildValue(t.Elem(), m, depth+1, seen); ev.IsValid() {
				v.Index(i).Set(ev)
			}
		}
		return v
	case reflect.Map:
		n := countFor(t, m)
		v := reflect.MakeMapWithSize(t, n)
		for i := range n {
			kv := buildValue(t.Key(), m, depth+1, seen)
			ev := buildValue(t.Elem(), m, depth+1, seen)
			if kv.IsValid() && ev.IsValid() {
				kv = varyKey(kv, i)
				v.SetMapIndex(kv, ev)
			}
		}
		return v
	case reflect.String:
		return reflect.ValueOf(textFor(m)).Convert(t)
	case reflect.Bool:
		return reflect.ValueOf(true).Convert(t)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return integerValue(t, m, int64(1)<<(t.Bits()-1)-1, 0)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return integerValue(t, m, 0, ^uint64(0)>>(64-t.Bits()))
	case reflect.Float32:
		return floatValue(t, m, math.MaxFloat32)
	case reflect.Float64:
		return floatValue(t, m, math.MaxFloat64)
	}
	return reflect.Zero(t)
}

func countFor(t reflect.Type, m mode) int {
	if t.Kind() == reflect.Array {
		return t.Len()
	}
	if m == modeMaximal {
		return 3
	}
	return 1
}

func integerValue(t reflect.Type, m mode, maxInt int64, maxUint uint64) reflect.Value {
	out := reflect.New(t).Elem()
	if t.Kind() >= reflect.Uint && t.Kind() <= reflect.Uintptr {
		v := uint64(1)
		if m == modeMaximal {
			v = maxUint
		}
		out.SetUint(v)
		return out
	}
	v := int64(1)
	if m == modeMaximal {
		v = maxInt
	}
	out.SetInt(v)
	return out
}

func floatValue(t reflect.Type, m mode, max float64) reflect.Value {
	v := 1.25
	if m == modeMaximal {
		v = max
	}
	out := reflect.New(t).Elem()
	out.SetFloat(v)
	return out
}

func textFor(m mode) string {
	switch m {
	case modeMaximal:
		return strings.Repeat("x", 64)
	case modeLongText:
		return "supercalifragilisticexpialidocious-and-very-long-japanese-text-これはとても長い表示確認用の文章です"
	default:
		return "x"
	}
}

func varyKey(v reflect.Value, i int) reflect.Value {
	if i == 0 {
		return v
	}
	if v.Kind() == reflect.String {
		out := reflect.New(v.Type()).Elem()
		out.SetString(v.String() + string(rune('a'+i)))
		return out
	}
	if v.Kind() >= reflect.Int && v.Kind() <= reflect.Int64 {
		out := reflect.New(v.Type()).Elem()
		out.SetInt(v.Int() + int64(i))
		return out
	}
	if v.Kind() >= reflect.Uint && v.Kind() <= reflect.Uintptr {
		out := reflect.New(v.Type()).Elem()
		out.SetUint(v.Uint() + uint64(i))
		return out
	}
	return v
}
