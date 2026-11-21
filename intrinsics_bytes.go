package minigo

import (
	"bytes"

	"github.com/podhmo/minigo/runtime"
)

func (e *Engine) installBytes(h *hostHelpers) {
	e.Bind("bytes", map[string]runtime.Value{
		// `var buf bytes.Buffer` / `new(bytes.Buffer)` box a real
		// *bytes.Buffer so methods (WriteString, String, ...) dispatch
		// on the host value.
		"Buffer":    hostType("bytes.Buffer", func() any { return &bytes.Buffer{} }),
		"Reader":    hostType("bytes.Reader", func() any { return &bytes.Reader{} }),
		"NewBuffer": h.fn("bytes.NewBuffer", func(a []any) (any, error) { return bytes.NewBuffer(byteSlice(a[0])), nil }, bytes.NewBuffer),
		"NewReader": h.fn("bytes.NewReader", func(a []any) (any, error) {
			return &runtime.GoValue{V: bytes.NewReader(byteSlice(a[0]))}, nil
		}, bytes.NewReader),
		"NewBufferString": h.fn("bytes.NewBufferString", func(a []any) (any, error) { return bytes.NewBufferString(str(a[0])), nil }, bytes.NewBufferString),
		"Contains":        h.fn2("bytes.Contains", func(a []any) (any, error) { return bytes.Contains(byteSlice(a[0]), byteSlice(a[1])), nil }, bytes.Contains),
		"Index":           h.fn2("bytes.Index", func(a []any) (any, error) { return bytes.Index(byteSlice(a[0]), byteSlice(a[1])), nil }),
		"LastIndex":       h.fn2("bytes.LastIndex", func(a []any) (any, error) { return bytes.LastIndex(byteSlice(a[0]), byteSlice(a[1])), nil }),
		"Count":           h.fn2("bytes.Count", func(a []any) (any, error) { return bytes.Count(byteSlice(a[0]), byteSlice(a[1])), nil }),
		"Equal":           h.fn2("bytes.Equal", func(a []any) (any, error) { return bytes.Equal(byteSlice(a[0]), byteSlice(a[1])), nil }, bytes.Equal),
		"Compare":         h.fn2("bytes.Compare", func(a []any) (any, error) { return bytes.Compare(byteSlice(a[0]), byteSlice(a[1])), nil }),
		"HasPrefix":       h.fn2("bytes.HasPrefix", func(a []any) (any, error) { return bytes.HasPrefix(byteSlice(a[0]), byteSlice(a[1])), nil }, bytes.HasPrefix),
		"HasSuffix":       h.fn2("bytes.HasSuffix", func(a []any) (any, error) { return bytes.HasSuffix(byteSlice(a[0]), byteSlice(a[1])), nil }, bytes.HasSuffix),
		"Fields":          h.fn("bytes.Fields", func(a []any) (any, error) { return bytesSliceOf(bytes.Fields(byteSlice(a[0]))), nil }),
		"Join":            h.fn2("bytes.Join", func(a []any) (any, error) { return bytes.Join(bytesSlices(a[0]), byteSlice(a[1])), nil }, bytes.Join),
		"Split":           h.fn2("bytes.Split", func(a []any) (any, error) { return bytesSliceOf(bytes.Split(byteSlice(a[0]), byteSlice(a[1]))), nil }),
		"SplitN": h.fn3("bytes.SplitN", func(a []any) (any, error) {
			return bytesSliceOf(bytes.SplitN(byteSlice(a[0]), byteSlice(a[1]), intOf(a[2]))), nil
		}),
		"Cut": h.fn2("bytes.Cut", func(a []any) (any, error) {
			b, af, ok := bytes.Cut(byteSlice(a[0]), byteSlice(a[1]))
			return &runtime.Tuple{Elems: []runtime.Value{scriptVal(b), scriptVal(af), ok}}, nil
		}),
		"CutPrefix": h.fn2("bytes.CutPrefix", func(a []any) (any, error) {
			af, ok := bytes.CutPrefix(byteSlice(a[0]), byteSlice(a[1]))
			return &runtime.Tuple{Elems: []runtime.Value{scriptVal(af), ok}}, nil
		}),
		"CutSuffix": h.fn2("bytes.CutSuffix", func(a []any) (any, error) {
			af, ok := bytes.CutSuffix(byteSlice(a[0]), byteSlice(a[1]))
			return &runtime.Tuple{Elems: []runtime.Value{scriptVal(af), ok}}, nil
		}),
		"Clone":      h.fn("bytes.Clone", func(a []any) (any, error) { return bytes.Clone(byteSlice(a[0])), nil }, bytes.Clone),
		"TrimPrefix": h.fn2("bytes.TrimPrefix", func(a []any) (any, error) { return bytes.TrimPrefix(byteSlice(a[0]), byteSlice(a[1])), nil }, bytes.TrimPrefix),
		"TrimSuffix": h.fn2("bytes.TrimSuffix", func(a []any) (any, error) { return bytes.TrimSuffix(byteSlice(a[0]), byteSlice(a[1])), nil }, bytes.TrimSuffix),
		"TrimLeft":   h.fn2("bytes.TrimLeft", func(a []any) (any, error) { return bytes.TrimLeft(byteSlice(a[0]), str(a[1])), nil }, bytes.TrimLeft),
		"TrimRight":  h.fn2("bytes.TrimRight", func(a []any) (any, error) { return bytes.TrimRight(byteSlice(a[0]), str(a[1])), nil }, bytes.TrimRight),
		"Repeat":     h.fn2("bytes.Repeat", func(a []any) (any, error) { return bytes.Repeat(byteSlice(a[0]), intOf(a[1])), nil }, bytes.Repeat),
		"Trim":       h.fn2("bytes.Trim", func(a []any) (any, error) { return bytes.Trim(byteSlice(a[0]), str(a[1])), nil }, bytes.Trim),
		"TrimSpace":  h.fn("bytes.TrimSpace", func(a []any) (any, error) { return bytes.TrimSpace(byteSlice(a[0])), nil }, bytes.TrimSpace),
		"ToUpper":    h.fn("bytes.ToUpper", func(a []any) (any, error) { return bytes.ToUpper(byteSlice(a[0])), nil }, bytes.ToUpper),
		"ToLower":    h.fn("bytes.ToLower", func(a []any) (any, error) { return bytes.ToLower(byteSlice(a[0])), nil }, bytes.ToLower),
		"ToTitle":    h.fn("bytes.ToTitle", func(a []any) (any, error) { return bytes.ToTitle(byteSlice(a[0])), nil }, bytes.ToTitle),
		"Runes":      h.fn("bytes.Runes", func(a []any) (any, error) { return runeSlice(bytes.Runes(byteSlice(a[0]))), nil }),
		"IndexByte":  h.fn2("bytes.IndexByte", func(a []any) (any, error) { return bytes.IndexByte(byteSlice(a[0]), byte(intOf(a[1]))), nil }),
		"IndexRune":  h.fn2("bytes.IndexRune", func(a []any) (any, error) { return bytes.IndexRune(byteSlice(a[0]), runeOf(a[1])), nil }),
		"IndexAny":   h.fn2("bytes.IndexAny", func(a []any) (any, error) { return bytes.IndexAny(byteSlice(a[0]), str(a[1])), nil }),
		// html/template's escaper folds attribute names, indexes
		// delimiter ends, and scans for comments through these three.
		"EqualFold":   h.fn2("bytes.EqualFold", func(a []any) (any, error) { return bytes.EqualFold(byteSlice(a[0]), byteSlice(a[1])), nil }, bytes.EqualFold),
		"ContainsAny": h.fn2("bytes.ContainsAny", func(a []any) (any, error) { return bytes.ContainsAny(byteSlice(a[0]), str(a[1])), nil }, bytes.ContainsAny),
		"Replace": h.arity("bytes.Replace", 4, func(a []any) (any, error) {
			return bytes.Replace(byteSlice(a[0]), byteSlice(a[1]), byteSlice(a[2]), intOf(a[3])), nil
		}),
		"ReplaceAll": h.fn3("bytes.ReplaceAll", func(a []any) (any, error) {
			return bytes.ReplaceAll(byteSlice(a[0]), byteSlice(a[1]), byteSlice(a[2])), nil
		}),
	})
}

// bytesSlices marshals a script [][]byte (slice of byte-ish values) to
// [][]byte — for bytes.Join/Split inputs.
func bytesSlices(v any) [][]byte {
	var elems []any
	switch s := v.(type) {
	case *runtime.Named:
		return bytesSlices(s.V)
	case *runtime.Slice:
		for _, e := range s.Elems {
			elems = append(elems, goNative(e))
		}
	case []any:
		elems = s
	}
	out := make([][]byte, len(elems))
	for i, e := range elems {
		out[i] = byteSlice(e)
	}
	return out
}

// bytesSliceOf lifts a [][]byte result into []any so scriptVal turns each
// element into a script []byte slice. A nil result keeps the [][]uint8
// spelling so %T matches Go on a nil [][]byte.
func bytesSliceOf(bb [][]byte) any {
	if bb == nil {
		return &runtime.TypedNil{Typ: anonSliceTyp("[]uint8")}
	}
	out := make([]any, len(bb))
	for i, b := range bb {
		out[i] = b
	}
	return out
}
