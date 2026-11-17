package minigo

import (
	"fmt"
	"slices"
	"strings"

	"github.com/podhmo/minigo/runtime"
)

func (e *Engine) installStrings(h *hostHelpers) {
	e.Bind("strings", map[string]runtime.Value{
		"Contains":    h.fn2("strings.Contains", func(a []any) (any, error) { return strings.Contains(str(a[0]), str(a[1])), nil }, strings.Contains),
		"ContainsAny": h.fn2("strings.ContainsAny", func(a []any) (any, error) { return strings.ContainsAny(str(a[0]), str(a[1])), nil }, strings.ContainsAny),
		"Compare":     h.fn2("strings.Compare", func(a []any) (any, error) { return strings.Compare(str(a[0]), str(a[1])), nil }),
		"Replace":     h.fn3("strings.Replace", func(a []any) (any, error) { return strings.Replace(str(a[0]), str(a[1]), str(a[2]), intOf(a[3])), nil }, strings.Replace),
		"Cut": h.fn2("strings.Cut", func(a []any) (any, error) {
			b, af, ok := strings.Cut(str(a[0]), str(a[1]))
			return &runtime.Tuple{Elems: []runtime.Value{b, af, ok}}, nil
		}),
		"CutPrefix": h.fn2("strings.CutPrefix", func(a []any) (any, error) {
			af, ok := strings.CutPrefix(str(a[0]), str(a[1]))
			return &runtime.Tuple{Elems: []runtime.Value{af, ok}}, nil
		}),
		"CutSuffix": h.fn2("strings.CutSuffix", func(a []any) (any, error) {
			bf, ok := strings.CutSuffix(str(a[0]), str(a[1]))
			return &runtime.Tuple{Elems: []runtime.Value{bf, ok}}, nil
		}),
		"HasPrefix":  h.fn2("strings.HasPrefix", func(a []any) (any, error) { return strings.HasPrefix(str(a[0]), str(a[1])), nil }, strings.HasPrefix),
		"HasSuffix":  h.fn2("strings.HasSuffix", func(a []any) (any, error) { return strings.HasSuffix(str(a[0]), str(a[1])), nil }, strings.HasSuffix),
		"Index":      h.fn2("strings.Index", func(a []any) (any, error) { return strings.Index(str(a[0]), str(a[1])), nil }),
		"Join":       h.fn2("strings.Join", func(a []any) (any, error) { return strings.Join(strSlice(a[0]), str(a[1])), nil }, strings.Join),
		"Split":      h.fn2("strings.Split", func(a []any) (any, error) { return strsSlice(strings.Split(str(a[0]), str(a[1]))), nil }),
		"ToUpper":    h.fn("strings.ToUpper", func(a []any) (any, error) { return strings.ToUpper(str(a[0])), nil }, strings.ToUpper),
		"ToLower":    h.fn("strings.ToLower", func(a []any) (any, error) { return strings.ToLower(str(a[0])), nil }, strings.ToLower),
		"TrimSpace":  h.fn("strings.TrimSpace", func(a []any) (any, error) { return strings.TrimSpace(str(a[0])), nil }, strings.TrimSpace),
		"ReplaceAll": h.fn3("strings.ReplaceAll", func(a []any) (any, error) { return strings.ReplaceAll(str(a[0]), str(a[1]), str(a[2])), nil }, strings.ReplaceAll),
		"Repeat":     h.fn2("strings.Repeat", func(a []any) (any, error) { return strings.Repeat(str(a[0]), intOf(a[1])), nil }, strings.Repeat),
		// a Builder's zero is the host *strings.Builder so Write*/String
		// methods dispatch through reflection like sync.Mutex's.
		"Builder": hostType("strings.Builder", func() any { return &strings.Builder{} }),
		// Reader likewise: `strings.Reader{}` and `(*strings.Reader)(nil)`
		// resolve to the named host type (Elem of *T reads strings.Reader).
		"Reader":      hostType("strings.Reader", func() any { return &strings.Reader{} }),
		"NewReplacer": h.fn("strings.NewReplacer", func(a []any) (any, error) { return strings.NewReplacer(strArgs(a)...), nil }, strings.NewReplacer),
		"Fields":      h.fn("strings.Fields", func(a []any) (any, error) { return strsSlice(strings.Fields(str(a[0]))), nil }),
		"EqualFold":   h.fn2("strings.EqualFold", func(a []any) (any, error) { return strings.EqualFold(str(a[0]), str(a[1])), nil }, strings.EqualFold),
		"Count":       h.fn2("strings.Count", func(a []any) (any, error) { return strings.Count(str(a[0]), str(a[1])), nil }),
		"SplitN":      h.fn3("strings.SplitN", func(a []any) (any, error) { return strsSlice(strings.SplitN(str(a[0]), str(a[1]), intOf(a[2]))), nil }),
		"SplitAfter":  h.fn2("strings.SplitAfter", func(a []any) (any, error) { return strsSlice(strings.SplitAfter(str(a[0]), str(a[1]))), nil }),
		"SplitAfterN": h.fn3("strings.SplitAfterN", func(a []any) (any, error) {
			return strsSlice(strings.SplitAfterN(str(a[0]), str(a[1]), intOf(a[2]))), nil
		}),
		"Trim":          h.fn2("strings.Trim", func(a []any) (any, error) { return strings.Trim(str(a[0]), str(a[1])), nil }, strings.Trim),
		"TrimPrefix":    h.fn2("strings.TrimPrefix", func(a []any) (any, error) { return strings.TrimPrefix(str(a[0]), str(a[1])), nil }, strings.TrimPrefix),
		"TrimSuffix":    h.fn2("strings.TrimSuffix", func(a []any) (any, error) { return strings.TrimSuffix(str(a[0]), str(a[1])), nil }, strings.TrimSuffix),
		"TrimLeft":      h.fn2("strings.TrimLeft", func(a []any) (any, error) { return strings.TrimLeft(str(a[0]), str(a[1])), nil }, strings.TrimLeft),
		"TrimRight":     h.fn2("strings.TrimRight", func(a []any) (any, error) { return strings.TrimRight(str(a[0]), str(a[1])), nil }, strings.TrimRight),
		"LastIndex":     h.fn2("strings.LastIndex", func(a []any) (any, error) { return strings.LastIndex(str(a[0]), str(a[1])), nil }),
		"LastIndexAny":  h.fn2("strings.LastIndexAny", func(a []any) (any, error) { return strings.LastIndexAny(str(a[0]), str(a[1])), nil }),
		"LastIndexByte": h.fn2("strings.LastIndexByte", func(a []any) (any, error) { return strings.LastIndexByte(str(a[0]), byte(intOf(a[1]))), nil }),
		"Lines": &runtime.BuiltinFunc{Name: "strings.Lines", Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("strings.Lines needs 1 arg")
			}
			return e.typedSeq(seqOf("strings.Lines", strPieces(slices.Collect(strings.Lines(str(args[0]))))), runtime.BasicTypedef("string")), nil
		}},
		"SplitSeq": &runtime.BuiltinFunc{Name: "strings.SplitSeq", Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("strings.SplitSeq needs 2 args")
			}
			return e.typedSeq(seqOf("strings.SplitSeq", strPieces(slices.Collect(strings.SplitSeq(str(args[0]), str(args[1]))))), runtime.BasicTypedef("string")), nil
		}},
		"SplitAfterSeq": &runtime.BuiltinFunc{Name: "strings.SplitAfterSeq", Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("strings.SplitAfterSeq needs 2 args")
			}
			return e.typedSeq(seqOf("strings.SplitAfterSeq", strPieces(slices.Collect(strings.SplitAfterSeq(str(args[0]), str(args[1]))))), runtime.BasicTypedef("string")), nil
		}},
		"FieldsSeq": &runtime.BuiltinFunc{Name: "strings.FieldsSeq", Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("strings.FieldsSeq needs 1 arg")
			}
			return e.typedSeq(seqOf("strings.FieldsSeq", strPieces(slices.Collect(strings.FieldsSeq(str(args[0]))))), runtime.BasicTypedef("string")), nil
		}},
		"FieldsFuncSeq": &runtime.BuiltinFunc{Name: "strings.FieldsFuncSeq", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("strings.FieldsFuncSeq needs 2 args")
			}
			return e.typedSeq(seqOf("strings.FieldsFuncSeq", strPieces(strings.FieldsFunc(str(args[0]), runePred(v, args[1])))), runtime.BasicTypedef("string")), nil
		}},
		"IndexAny":     h.fn2("strings.IndexAny", func(a []any) (any, error) { return strings.IndexAny(str(a[0]), str(a[1])), nil }),
		"IndexByte":    h.fn2("strings.IndexByte", func(a []any) (any, error) { return strings.IndexByte(str(a[0]), byte(intOf(a[1]))), nil }),
		"IndexRune":    h.fn2("strings.IndexRune", func(a []any) (any, error) { return strings.IndexRune(str(a[0]), runeOf(a[1])), nil }),
		"ContainsRune": h.fn2("strings.ContainsRune", func(a []any) (any, error) { return strings.ContainsRune(str(a[0]), runeOf(a[1])), nil }, strings.ContainsRune),
		"ToTitle":      h.fn("strings.ToTitle", func(a []any) (any, error) { return strings.ToTitle(str(a[0])), nil }, strings.ToTitle),
		//lint:ignore SA1019 mirrors the deprecated stdlib symbol for script parity
		"Title":     h.fn("strings.Title", func(a []any) (any, error) { return strings.Title(str(a[0])), nil }, strings.Title),
		"NewReader": h.fn("strings.NewReader", func(a []any) (any, error) { return strings.NewReader(str(a[0])), nil }, strings.NewReader),
		// func-taking variants call the script callback back through the VM.
		"TrimFunc": &runtime.BuiltinFunc{Name: "strings.TrimFunc", SyncCallbacks: true, Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("strings.TrimFunc needs 2 args")
			}
			out := strings.TrimFunc(str(args[0]), runePred(v, args[1]))
			return out, nil
		}},
		"TrimLeftFunc": &runtime.BuiltinFunc{Name: "strings.TrimLeftFunc", SyncCallbacks: true, Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("strings.TrimLeftFunc needs 2 args")
			}
			out := strings.TrimLeftFunc(str(args[0]), runePred(v, args[1]))
			return out, nil
		}},
		"TrimRightFunc": &runtime.BuiltinFunc{Name: "strings.TrimRightFunc", SyncCallbacks: true, Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("strings.TrimRightFunc needs 2 args")
			}
			out := strings.TrimRightFunc(str(args[0]), runePred(v, args[1]))
			return out, nil
		}},
		"IndexFunc": &runtime.BuiltinFunc{Name: "strings.IndexFunc", SyncCallbacks: true, Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("strings.IndexFunc needs 2 args")
			}
			out := strings.IndexFunc(str(args[0]), runePred(v, args[1]))
			return int64(out), nil
		}},
		"ContainsFunc": &runtime.BuiltinFunc{Name: "strings.ContainsFunc", SyncCallbacks: true, Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("strings.ContainsFunc needs 2 args")
			}
			return strings.ContainsFunc(str(args[0]), runePred(v, args[1])), nil
		}},
		"LastIndexFunc": &runtime.BuiltinFunc{Name: "strings.LastIndexFunc", SyncCallbacks: true, Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("strings.LastIndexFunc needs 2 args")
			}
			out := strings.LastIndexFunc(str(args[0]), runePred(v, args[1]))
			return int64(out), nil
		}},
		"FieldsFunc": &runtime.BuiltinFunc{Name: "strings.FieldsFunc", SyncCallbacks: true, Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("strings.FieldsFunc needs 2 args")
			}
			out := strings.FieldsFunc(str(args[0]), runePred(v, args[1]))
			return strsSlice(out), nil
		}},
		"Map": &runtime.BuiltinFunc{Name: "strings.Map", SyncCallbacks: true, Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("strings.Map needs 2 args")
			}
			mapping := func(r rune) rune {
				res := callOrPanic(v, args[0], []runtime.Value{int64(r)})
				// a negative result drops the rune in Go
				return rune(int64Of(goNative(res)))
			}
			out := strings.Map(mapping, str(args[1]))
			return out, nil
		}},
	})
}
