package stdjson

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/podhmo/minigo/internal/stdlib"
	"github.com/podhmo/minigo/runtime"
)

func jsonScalar(c runtime.VMCaller, dec any, td *runtime.TypeDef, prior runtime.Value, ectx *jsonErrCtx) runtime.Value {
	// UseNumber's decoder emits json.Number for every number literal;
	// a typed target still converts the literal like gc's direct decode.
	// Integer literals keep their exact int64/uint64 so >2^53 values
	// decode without float64 rounding, and integer targets take only
	// integer spellings: gc hands the literal to ParseInt/ParseUint, so
	// "1e2"/"1.0" fail here rather than rounding through float64.
	if n, ok := dec.(json.Number); ok && td != nil {
		un := jsonUnderlyingName(td)
		if un == "" {
			un = td.Name
		}
		switch {
		case td.Name == "encoding/json.Number" || un == "json.Number":
			dec = n.String()
		case un == "float64" || un == "float32":
			if f, err := n.Float64(); err == nil {
				// gc's floatType path: a float32 destination rejects
				// when the ROUNDED value overflows (3.4028235e38
				// rounds into range, 3.5e38 does not), and either
				// float reports the literal when strconv refuses it
				// (1e309, etc).
				if un == "float32" && math.IsInf(float64(float32(f)), 0) {
					ectx.fail("number "+n.String(), td, nil)
					return priorOr(prior, c, td)
				}
				dec = f
			} else {
				ectx.fail("number "+n.String(), td, nil)
				return priorOr(prior, c, td)
			}
		case jsonIntName(un):
			lit := n.String()
			if i, err := strconv.ParseInt(lit, 10, 64); err == nil {
				dec = i
			} else if u, uerr := strconv.ParseUint(lit, 10, 64); uerr == nil {
				dec = u
			} else {
				ectx.fail("number "+lit, td, nil)
				return priorOr(prior, c, td)
			}
		}
	}
	fail := func() runtime.Value {
		ectx.fail(jsonValueName(dec), td, nil)
		return priorOr(prior, c, td)
	}
	conv := func(x any) runtime.Value {
		v, err := c.Convert(td, stdlib.ScriptVal(x))
		if err != nil {
			return fail()
		}
		return v
	}
	switch td.Name {
	case "string":
		s, ok := dec.(string)
		if !ok {
			return fail()
		}
		return s
	case "bool":
		b, ok := dec.(bool)
		if !ok {
			return fail()
		}
		return b
	case "float64", "float32":
		f, ok := dec.(float64)
		if !ok {
			return fail()
		}
		return conv(f)
	case "encoding/json.Number":
		// Number is `type Number string` and keeps the literal text —
		// the check is the number grammar, not the float64 range.
		switch x := dec.(type) {
		case float64:
			return conv(jsonNumLit(x))
		case string:
			if !jsonIsValidNumber(x) {
				// gc reports this one without field context.
				ectx.failErr(fmt.Errorf("json: cannot unmarshal string %s into Go value of type json.Number: invalid syntax", strconv.Quote(x)))
				return priorOr(prior, c, td)
			}
			return conv(x)
		}
		return fail()
	}
	if jsonIntName(td.Name) {
		if i, ok := dec.(int64); ok {
			if i2, ok := jsonIntOfInt64(i, td.Name); ok {
				return conv(i2)
			}
			ectx.fail("number "+strconv.FormatInt(i, 10), td, nil)
			return priorOr(prior, c, td)
		}
		if u, ok := dec.(uint64); ok {
			if u2, ok := jsonUintOfInt(u, td.Name); ok {
				return conv(u2)
			}
			ectx.fail("number "+strconv.FormatUint(u, 10), td, nil)
			return priorOr(prior, c, td)
		}
		f, ok := dec.(float64)
		if !ok {
			return fail()
		}
		i, ok := jsonIntOf(f, td.Name)
		if !ok {
			ectx.fail("number "+jsonNumLit(f), td, nil)
			return priorOr(prior, c, td)
		}
		return conv(i)
	}
	// Named basics (`type MBool bool`, `type MyInt int`) coerce by their
	// underlying zero's Go kind — Convert applies the declaration's
	// conversion rules and tags the result.
	switch runtime.Unwrap(c.Zero(td)).(type) {
	case bool:
		b, ok := dec.(bool)
		if !ok {
			return fail()
		}
		return conv(b)
	case string:
		s, ok := dec.(string)
		if !ok {
			return fail()
		}
		return conv(s)
	case float64:
		f, ok := dec.(float64)
		if !ok {
			return fail()
		}
		return conv(f)
	case int64:
		un := jsonUnderlyingName(td)
		if i, ok := dec.(int64); ok {
			if i2, ok := jsonIntOfInt64(i, un); ok {
				return conv(i2)
			}
			ectx.fail("number "+strconv.FormatInt(i, 10), td, nil)
			return priorOr(prior, c, td)
		}
		if u, ok := dec.(uint64); ok {
			if u2, ok := jsonUintOfInt(u, un); ok {
				return conv(u2)
			}
			ectx.fail("number "+strconv.FormatUint(u, 10), td, nil)
			return priorOr(prior, c, td)
		}
		f, ok := dec.(float64)
		if !ok {
			return fail()
		}
		i, ok := jsonIntOf(f, un)
		if !ok {
			ectx.fail("number "+jsonNumLit(f), td, nil)
			return priorOr(prior, c, td)
		}
		return conv(i)
	case uint64:
		un := jsonUnderlyingName(td)
		if i, ok := dec.(int64); ok {
			if i2, ok := jsonIntOfInt64(i, un); ok {
				return conv(i2)
			}
			ectx.fail("number "+strconv.FormatInt(i, 10), td, nil)
			return priorOr(prior, c, td)
		}
		if u, ok := dec.(uint64); ok {
			if u2, ok := jsonUintOfInt(u, un); ok {
				return conv(u2)
			}
			ectx.fail("number "+strconv.FormatUint(u, 10), td, nil)
			return priorOr(prior, c, td)
		}
		f, ok := dec.(float64)
		if !ok {
			return fail()
		}
		i, ok := jsonIntOf(f, un)
		if !ok {
			ectx.fail("number "+jsonNumLit(f), td, nil)
			return priorOr(prior, c, td)
		}
		return conv(i)
	}
	return fail()
}

// jsonStringOpt decodes a `,string`-tagged field: the JSON value must
// be a string whose inner text is the literal for the field's kind —
// `"42"` for ints, `"\"x\""` for strings, `"true"` for bools. A
// non-string value is a type error even when it would otherwise fit
// (gc reports the value's JSON kind) and null keeps normal semantics.
// Composite and interface kinds ignore the option entirely — the
// string then decodes as it normally would (into an interface the
// string itself lands, into a struct it fails like any string).
// Pointers apply the option to the pointee.
func jsonStringOpt(c runtime.VMCaller, node *jsonDom, td *runtime.TypeDef, prior runtime.Value, ectx *jsonErrCtx) runtime.Value {
	dec := domVal(node)
	if dec == nil {
		return jsonShape(c, node, td, prior, ectx)
	}
	s, sok := dec.(string)
	if td == nil {
		if sok {
			return stdlib.ScriptVal(s)
		}
		return stdlib.ScriptVal(jsonDomDeep(node, ectx))
	}
	// A quoted "null" is the null literal, not the word: it follows
	// normal null semantics (nils nilable kinds, leaves the rest).
	if sok && s == "null" {
		return jsonShape(c, &jsonDom{raw: []byte("null"), pos: -1}, td, prior, ectx)
	}
	if td.Kind == runtime.KindPointer {
		var ep runtime.Value
		if pc, ok := prior.(*runtime.Cell); ok {
			ep = pc.Elem
		}
		return &runtime.Cell{Elem: jsonStringOpt(c, node, c.TypeOf(c.ElemZero(td)), ep, ectx)}
	}
	conv := func(x any) runtime.Value {
		v, err := c.Convert(td, stdlib.ScriptVal(x))
		if err != nil {
			ectx.fail("string", td, nil)
			return priorOr(prior, c, td)
		}
		return v
	}
	numFail := func() runtime.Value {
		ectx.fail("number "+s, td, nil)
		return priorOr(prior, c, td)
	}
	isNumber := td.Name == "encoding/json.Number"
	kind, bits, signed := "", 64, true
	if !isNumber {
		switch u := jsonUnderlyingName(td); {
		case u == "string":
			kind = "string"
		case u == "bool":
			kind = "bool"
		case u == "float64", u == "float32":
			kind = "float"
			if u == "float32" {
				bits = 32
			}
		case jsonIntName(u):
			bits, signed = jsonIntWidth(u)
			if signed {
				kind = "int"
			} else {
				kind = "uint"
			}
		}
	}
	// gc's ,string applies only to bool/int/float/string kinds (and
	// json.Number's literal keep): composite and interface kinds ignore
	// the option entirely and the value decodes as it normally would —
	// an array into a `,string` slice field is not an error.
	if !isNumber && kind == "" {
		return jsonShape(c, node, td, prior, ectx)
	}
	if !sok {
		ectx.fail(jsonValueName(dec), td, nil)
		return priorOr(prior, c, td)
	}
	// a named type offering UnmarshalText receives the string's inner
	// text verbatim — ,string hands the method the literal bytes
	// without re-parsing them as JSON.
	if !isNumber && td.Name != "" {
		recv, cell := unmarshalRecv(prior, c, td)
		if m, ok := runtime.IfaceMember(c, recv, "UnmarshalText"); ok && jsonUnmarshalShape(m) {
			if !jsonCallMethod(c, m, []byte(s), ectx) {
				return prior
			}
			if cell != nil {
				return cell.Elem
			}
			return prior
		}
	}
	inner := s
	if isNumber {
		if !jsonIsValidNumber(inner) {
			// gc reports this one without field context.
			ectx.failErr(fmt.Errorf("json: cannot unmarshal string %s into Go value of type json.Number: invalid syntax", strconv.Quote(inner)))
			return priorOr(prior, c, td)
		}
		return conv(inner)
	}
	switch kind {
	case "string":
		var sv string
		if err := json.Unmarshal([]byte(inner), &sv); err != nil {
			// The inner text is re-parsed expecting a quoted string
			// token: a leading non-quote char reports jsontext's
			// object-key error, a bare empty text reports EOF.
			var cause error
			if inner == "" {
				cause = errors.New("unexpected end of JSON input")
			} else if inner[0] != '"' {
				cause = fmt.Errorf("invalid character %q looking for beginning of object key string", rune(inner[0]))
			} else {
				cause = err
			}
			ectx.fail("string", td, cause)
			return priorOr(prior, c, td)
		}
		return conv(sv)
	case "bool":
		switch inner {
		case "true":
			return conv(true)
		case "false":
			return conv(false)
		}
		ectx.fail("string "+strconv.Quote(inner), td, strconv.ErrSyntax)
		return priorOr(prior, c, td)
	case "int":
		n, err := strconv.ParseInt(inner, 10, bits)
		if err != nil {
			return numFail()
		}
		return conv(n)
	case "uint":
		u64, err := strconv.ParseUint(inner, 10, bits)
		if err != nil {
			return numFail()
		}
		return conv(u64)
	case "float":
		f, err := strconv.ParseFloat(inner, bits)
		if err != nil {
			return numFail()
		}
		return conv(f)
	}
	return priorOr(prior, c, td)
}

// jsonIsValidNumber mirrors encoding/json's isValidNumber — the RFC
// 7159 number grammar. json.Number keeps the literal text, so range is
// not a constraint ("1e1000" is valid even though it overflows float64).
func jsonIsValidNumber(s string) bool {
	if s == "" {
		return false
	}
	if s[0] == '-' {
		s = s[1:]
		if s == "" {
			return false
		}
	}
	switch {
	case s[0] == '0':
		s = s[1:]
	case '1' <= s[0] && s[0] <= '9':
		s = s[1:]
		for len(s) > 0 && '0' <= s[0] && s[0] <= '9' {
			s = s[1:]
		}
	default:
		return false
	}
	if len(s) >= 2 && s[0] == '.' && '0' <= s[1] && s[1] <= '9' {
		s = s[2:]
		for len(s) > 0 && '0' <= s[0] && s[0] <= '9' {
			s = s[1:]
		}
	}
	if len(s) >= 2 && (s[0] == 'e' || s[0] == 'E') {
		s = s[1:]
		if s[0] == '+' || s[0] == '-' {
			s = s[1:]
			if s == "" {
				return false
			}
		}
		for len(s) > 0 && '0' <= s[0] && s[0] <= '9' {
			s = s[1:]
		}
	}
	return s == ""
}

// jsonStringLit renders a scalar field's `,string` marshal literal —
// the JSON encoding of the value as it should appear inside the
// outer quotes: ints become "42", strings are re-quoted ("\"x\""),
// json.Number keeps its raw literal text. ok=false for kinds gc
// leaves alone (composites, interfaces, nil).
func jsonStringLit(v runtime.Value) (string, bool) {
	// Named must be checked before runtime.Unwrap peels it — json.Number
	// is `type Number string` and only the wrapper carries the typedef.
	if n, ok := v.(*runtime.Named); ok {
		if n.Typ != nil && n.Typ.Name == "encoding/json.Number" {
			if s, ok := runtime.Unwrap(n.V).(string); ok {
				return s, true
			}
			return "", false
		}
		return jsonStringLit(n.V)
	}
	switch x := runtime.Unwrap(v).(type) {
	case *runtime.Cell:
		return jsonStringLit(x.Elem)
	case bool:
		return strconv.FormatBool(x), true
	case int64:
		return strconv.FormatInt(x, 10), true
	case float64:
		b, err := json.Marshal(x)
		if err != nil {
			return "", false
		}
		return string(b), true
	case string:
		b, err := json.Marshal(x)
		if err != nil {
			return "", false
		}
		return string(b), true
	}
	return "", false
}

// jsonIntName reports whether name is a builtin integer spelling.
func jsonIntName(name string) bool {
	switch name {
	case "int", "int8", "int16", "int32", "int64",
		"uint", "uint8", "uint16", "uint32", "uint64",
		"byte", "rune", "uintptr":
		return true
	}
	return false
}



// jsonUnderlyingName peels a named typedef to its underlying spelling's
// bare identifier ("int" for `type MyInt int`) for width checks.
func jsonUnderlyingName(td *runtime.TypeDef) string {
	s := runtime.TypUnderlyingSpelling(td)
	if i := strings.LastIndex(s, "."); i >= 0 {
		s = s[i+1:]
	}
	return s
}

// jsonIntOf converts an integral float64 to int64 like encoding/json's
// ParseInt on the literal: non-integral and out-of-range numbers fail.
func jsonIntOf(f float64, name string) (int64, bool) {
	i := int64(f)
	if float64(i) != f {
		return 0, false
	}
	return jsonIntOfInt64(i, name)
}

// jsonIntOfInt64 range-checks an exact int64 against the spelled
// integer type — the float64-free path UseNumber's literal keeps.
func jsonIntOfInt64(i int64, name string) (int64, bool) {
	bits, signed := jsonIntWidth(name)
	if bits >= 64 {
		if !signed && i < 0 {
			return 0, false
		}
		return i, true
	}
	if signed {
		max := int64(1)<<(bits-1) - 1
		if i < -max-1 || i > max {
			return 0, false
		}
	} else if i < 0 || i > int64(1)<<bits-1 {
		return 0, false
	}
	return i, true
}

// jsonUintOfInt range-checks an exact uint64 literal against the
// spelled integer type — the >2^63 path UseNumber's literal keeps.
func jsonUintOfInt(u uint64, name string) (uint64, bool) {
	bits, signed := jsonIntWidth(name)
	if bits >= 64 {
		if signed && u > math.MaxInt64 {
			return 0, false
		}
		return u, true
	}
	if signed {
		if u > uint64(1)<<(bits-1)-1 {
			return 0, false
		}
	} else if u > uint64(1)<<bits-1 {
		return 0, false
	}
	return u, true
}

// jsonIntWidth returns the bit width and signedness of a spelled integer
// type; unknown spellings default to signed 64.
func jsonIntWidth(name string) (bits int, signed bool) {
	switch name {
	case "int8", "uint8", "byte":
		return 8, name == "int8"
	case "int16", "uint16":
		return 16, name == "int16"
	case "int32", "rune", "uint32":
		return 32, name == "int32" || name == "rune"
	}
	return 64, name == "int" || name == "int64"
}

// jsonDeep rewrites the tree encoding/json produces — map[string]any keys —
// into the map[any]any shape scriptVal already handles. keepNum leaves
// a UseNumber decoder's json.Number on `any` targets; without it the
// literal flattens to float64 like gc's default decode.

func jsonDeep(v any, keepNum bool) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[any]any, len(x))
		for k, e := range x {
			out[k] = jsonDeep(e, keepNum)
		}
		return out
	case []any:
		for i, e := range x {
			x[i] = jsonDeep(e, keepNum)
		}
		return x
	case json.Number:
		if keepNum {
			return x
		}
		if f, err := x.Float64(); err == nil {
			return f
		}
		return x.String()
	default:
		return v
	}
}
