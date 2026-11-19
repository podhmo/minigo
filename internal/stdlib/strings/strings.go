package strings

import (
	"fmt"
	"slices"
	"strings"

	"github.com/podhmo/minigo/internal/hostbridge"
	"github.com/podhmo/minigo/internal/stdlib"
	"github.com/podhmo/minigo/runtime"
)

func Install(bind stdlib.Bind, h stdlib.Helpers, typedSeq func(*runtime.BuiltinFunc, ...*runtime.TypeDef) runtime.Value) {
	bind("strings", map[string]runtime.Value{
		"Contains":    h.Fn2("strings.Contains", func(a []any) (any, error) { return strings.Contains(stdlib.Str(a[0]), stdlib.Str(a[1])), nil }, strings.Contains),
		"ContainsAny": h.Fn2("strings.ContainsAny", func(a []any) (any, error) { return strings.ContainsAny(stdlib.Str(a[0]), stdlib.Str(a[1])), nil }, strings.ContainsAny),
		"Compare":     h.Fn2("strings.Compare", func(a []any) (any, error) { return strings.Compare(stdlib.Str(a[0]), stdlib.Str(a[1])), nil }),
		"Replace": h.Fn3("strings.Replace", func(a []any) (any, error) {
			return strings.Replace(stdlib.Str(a[0]), stdlib.Str(a[1]), stdlib.Str(a[2]), stdlib.IntOf(a[3])), nil
		}, strings.Replace),
		"Cut": h.Fn2("strings.Cut", func(a []any) (any, error) {
			b, af, ok := strings.Cut(stdlib.Str(a[0]), stdlib.Str(a[1]))
			return &runtime.Tuple{Elems: []runtime.Value{b, af, ok}}, nil
		}),
		"CutPrefix": h.Fn2("strings.CutPrefix", func(a []any) (any, error) {
			af, ok := strings.CutPrefix(stdlib.Str(a[0]), stdlib.Str(a[1]))
			return &runtime.Tuple{Elems: []runtime.Value{af, ok}}, nil
		}),
		"CutSuffix": h.Fn2("strings.CutSuffix", func(a []any) (any, error) {
			bf, ok := strings.CutSuffix(stdlib.Str(a[0]), stdlib.Str(a[1]))
			return &runtime.Tuple{Elems: []runtime.Value{bf, ok}}, nil
		}),
		"HasPrefix": h.Fn2("strings.HasPrefix", func(a []any) (any, error) { return strings.HasPrefix(stdlib.Str(a[0]), stdlib.Str(a[1])), nil }, strings.HasPrefix),
		"HasSuffix": h.Fn2("strings.HasSuffix", func(a []any) (any, error) { return strings.HasSuffix(stdlib.Str(a[0]), stdlib.Str(a[1])), nil }, strings.HasSuffix),
		"Index":     h.Fn2("strings.Index", func(a []any) (any, error) { return strings.Index(stdlib.Str(a[0]), stdlib.Str(a[1])), nil }),
		"Join":      h.Fn2("strings.Join", func(a []any) (any, error) { return strings.Join(stdlib.StrSlice(a[0]), stdlib.Str(a[1])), nil }, strings.Join),
		"Split": h.Fn2("strings.Split", func(a []any) (any, error) {
			return stdlib.StrsSlice(strings.Split(stdlib.Str(a[0]), stdlib.Str(a[1]))), nil
		}),
		"ToUpper":   h.Fn("strings.ToUpper", func(a []any) (any, error) { return strings.ToUpper(stdlib.Str(a[0])), nil }, strings.ToUpper),
		"ToLower":   h.Fn("strings.ToLower", func(a []any) (any, error) { return strings.ToLower(stdlib.Str(a[0])), nil }, strings.ToLower),
		"TrimSpace": h.Fn("strings.TrimSpace", func(a []any) (any, error) { return strings.TrimSpace(stdlib.Str(a[0])), nil }, strings.TrimSpace),
		"ReplaceAll": h.Fn3("strings.ReplaceAll", func(a []any) (any, error) {
			return strings.ReplaceAll(stdlib.Str(a[0]), stdlib.Str(a[1]), stdlib.Str(a[2])), nil
		}, strings.ReplaceAll),
		"Repeat": h.Fn2("strings.Repeat", func(a []any) (any, error) { return strings.Repeat(stdlib.Str(a[0]), stdlib.IntOf(a[1])), nil }, strings.Repeat),
		// a Builder's zero is the host *strings.Builder so Write*/String
		// methods dispatch through reflection like sync.Mutex's.
		"Builder": stdlib.HostType("strings.Builder", func() any { return &strings.Builder{} }),
		// Reader likewise: `strings.Reader{}` and `(*strings.Reader)(nil)`
		// resolve to the named host type (Elem of *T reads strings.Reader).
		"Reader":      stdlib.HostType("strings.Reader", func() any { return &strings.Reader{} }),
		"NewReplacer": h.Fn("strings.NewReplacer", func(a []any) (any, error) { return strings.NewReplacer(stdlib.StrArgs(a)...), nil }, strings.NewReplacer),
		"Fields":      h.Fn("strings.Fields", func(a []any) (any, error) { return stdlib.StrsSlice(strings.Fields(stdlib.Str(a[0]))), nil }),
		"EqualFold":   h.Fn2("strings.EqualFold", func(a []any) (any, error) { return strings.EqualFold(stdlib.Str(a[0]), stdlib.Str(a[1])), nil }, strings.EqualFold),
		"Count":       h.Fn2("strings.Count", func(a []any) (any, error) { return strings.Count(stdlib.Str(a[0]), stdlib.Str(a[1])), nil }),
		"SplitN": h.Fn3("strings.SplitN", func(a []any) (any, error) {
			return stdlib.StrsSlice(strings.SplitN(stdlib.Str(a[0]), stdlib.Str(a[1]), stdlib.IntOf(a[2]))), nil
		}),
		"SplitAfter": h.Fn2("strings.SplitAfter", func(a []any) (any, error) {
			return stdlib.StrsSlice(strings.SplitAfter(stdlib.Str(a[0]), stdlib.Str(a[1]))), nil
		}),
		"SplitAfterN": h.Fn3("strings.SplitAfterN", func(a []any) (any, error) {
			return stdlib.StrsSlice(strings.SplitAfterN(stdlib.Str(a[0]), stdlib.Str(a[1]), stdlib.IntOf(a[2]))), nil
		}),
		"Trim":         h.Fn2("strings.Trim", func(a []any) (any, error) { return strings.Trim(stdlib.Str(a[0]), stdlib.Str(a[1])), nil }, strings.Trim),
		"TrimPrefix":   h.Fn2("strings.TrimPrefix", func(a []any) (any, error) { return strings.TrimPrefix(stdlib.Str(a[0]), stdlib.Str(a[1])), nil }, strings.TrimPrefix),
		"TrimSuffix":   h.Fn2("strings.TrimSuffix", func(a []any) (any, error) { return strings.TrimSuffix(stdlib.Str(a[0]), stdlib.Str(a[1])), nil }, strings.TrimSuffix),
		"TrimLeft":     h.Fn2("strings.TrimLeft", func(a []any) (any, error) { return strings.TrimLeft(stdlib.Str(a[0]), stdlib.Str(a[1])), nil }, strings.TrimLeft),
		"TrimRight":    h.Fn2("strings.TrimRight", func(a []any) (any, error) { return strings.TrimRight(stdlib.Str(a[0]), stdlib.Str(a[1])), nil }, strings.TrimRight),
		"LastIndex":    h.Fn2("strings.LastIndex", func(a []any) (any, error) { return strings.LastIndex(stdlib.Str(a[0]), stdlib.Str(a[1])), nil }),
		"LastIndexAny": h.Fn2("strings.LastIndexAny", func(a []any) (any, error) { return strings.LastIndexAny(stdlib.Str(a[0]), stdlib.Str(a[1])), nil }),
		"LastIndexByte": h.Fn2("strings.LastIndexByte", func(a []any) (any, error) {
			return strings.LastIndexByte(stdlib.Str(a[0]), byte(stdlib.IntOf(a[1]))), nil
		}),
		"Lines": &runtime.BuiltinFunc{Name: "strings.Lines", Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("strings.Lines needs 1 arg")
			}
			return typedSeq(stdlib.SeqOf("strings.Lines", stdlib.StrPieces(slices.Collect(strings.Lines(stdlib.Str(args[0]))))), runtime.BasicTypedef("string")), nil
		}},
		"SplitSeq": &runtime.BuiltinFunc{Name: "strings.SplitSeq", Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("strings.SplitSeq needs 2 args")
			}
			return typedSeq(stdlib.SeqOf("strings.SplitSeq", stdlib.StrPieces(slices.Collect(strings.SplitSeq(stdlib.Str(args[0]), stdlib.Str(args[1]))))), runtime.BasicTypedef("string")), nil
		}},
		"SplitAfterSeq": &runtime.BuiltinFunc{Name: "strings.SplitAfterSeq", Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("strings.SplitAfterSeq needs 2 args")
			}
			return typedSeq(stdlib.SeqOf("strings.SplitAfterSeq", stdlib.StrPieces(slices.Collect(strings.SplitAfterSeq(stdlib.Str(args[0]), stdlib.Str(args[1]))))), runtime.BasicTypedef("string")), nil
		}},
		"FieldsSeq": &runtime.BuiltinFunc{Name: "strings.FieldsSeq", Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("strings.FieldsSeq needs 1 arg")
			}
			return typedSeq(stdlib.SeqOf("strings.FieldsSeq", stdlib.StrPieces(slices.Collect(strings.FieldsSeq(stdlib.Str(args[0]))))), runtime.BasicTypedef("string")), nil
		}},
		"FieldsFuncSeq": &runtime.BuiltinFunc{Name: "strings.FieldsFuncSeq", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("strings.FieldsFuncSeq needs 2 args")
			}
			return typedSeq(stdlib.SeqOf("strings.FieldsFuncSeq", stdlib.StrPieces(strings.FieldsFunc(stdlib.Str(args[0]), stdlib.RunePred(v, args[1])))), runtime.BasicTypedef("string")), nil
		}},
		"IndexAny":     h.Fn2("strings.IndexAny", func(a []any) (any, error) { return strings.IndexAny(stdlib.Str(a[0]), stdlib.Str(a[1])), nil }),
		"IndexByte":    h.Fn2("strings.IndexByte", func(a []any) (any, error) { return strings.IndexByte(stdlib.Str(a[0]), byte(stdlib.IntOf(a[1]))), nil }),
		"IndexRune":    h.Fn2("strings.IndexRune", func(a []any) (any, error) { return strings.IndexRune(stdlib.Str(a[0]), stdlib.RuneOf(a[1])), nil }),
		"ContainsRune": h.Fn2("strings.ContainsRune", func(a []any) (any, error) { return strings.ContainsRune(stdlib.Str(a[0]), stdlib.RuneOf(a[1])), nil }, strings.ContainsRune),
		"ToTitle":      h.Fn("strings.ToTitle", func(a []any) (any, error) { return strings.ToTitle(stdlib.Str(a[0])), nil }, strings.ToTitle),
		//lint:ignore SA1019 mirrors the deprecated stdlib symbol for script parity
		"Title":     h.Fn("strings.Title", func(a []any) (any, error) { return strings.Title(stdlib.Str(a[0])), nil }, strings.Title),
		"NewReader": h.Fn("strings.NewReader", func(a []any) (any, error) { return strings.NewReader(stdlib.Str(a[0])), nil }, strings.NewReader),
		// func-taking variants call the script callback back through the VM.
		"TrimFunc": &runtime.BuiltinFunc{Name: "strings.TrimFunc", SyncCallbacks: true, Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("strings.TrimFunc needs 2 args")
			}
			out := strings.TrimFunc(stdlib.Str(args[0]), stdlib.RunePred(v, args[1]))
			return out, nil
		}},
		"TrimLeftFunc": &runtime.BuiltinFunc{Name: "strings.TrimLeftFunc", SyncCallbacks: true, Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("strings.TrimLeftFunc needs 2 args")
			}
			out := strings.TrimLeftFunc(stdlib.Str(args[0]), stdlib.RunePred(v, args[1]))
			return out, nil
		}},
		"TrimRightFunc": &runtime.BuiltinFunc{Name: "strings.TrimRightFunc", SyncCallbacks: true, Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("strings.TrimRightFunc needs 2 args")
			}
			out := strings.TrimRightFunc(stdlib.Str(args[0]), stdlib.RunePred(v, args[1]))
			return out, nil
		}},
		"IndexFunc": &runtime.BuiltinFunc{Name: "strings.IndexFunc", SyncCallbacks: true, Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("strings.IndexFunc needs 2 args")
			}
			out := strings.IndexFunc(stdlib.Str(args[0]), stdlib.RunePred(v, args[1]))
			return int64(out), nil
		}},
		"ContainsFunc": &runtime.BuiltinFunc{Name: "strings.ContainsFunc", SyncCallbacks: true, Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("strings.ContainsFunc needs 2 args")
			}
			return strings.ContainsFunc(stdlib.Str(args[0]), stdlib.RunePred(v, args[1])), nil
		}},
		"LastIndexFunc": &runtime.BuiltinFunc{Name: "strings.LastIndexFunc", SyncCallbacks: true, Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("strings.LastIndexFunc needs 2 args")
			}
			out := strings.LastIndexFunc(stdlib.Str(args[0]), stdlib.RunePred(v, args[1]))
			return int64(out), nil
		}},
		"FieldsFunc": &runtime.BuiltinFunc{Name: "strings.FieldsFunc", SyncCallbacks: true, Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("strings.FieldsFunc needs 2 args")
			}
			out := strings.FieldsFunc(stdlib.Str(args[0]), stdlib.RunePred(v, args[1]))
			return stdlib.StrsSlice(out), nil
		}},
		"Map": &runtime.BuiltinFunc{Name: "strings.Map", SyncCallbacks: true, Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("strings.Map needs 2 args")
			}
			mapping := func(r rune) rune {
				res := hostbridge.CallOrPanic(v, args[0], []runtime.Value{int64(r)})
				// a negative result drops the rune in Go
				return rune(stdlib.Int64Of(hostbridge.GoNative(res)))
			}
			out := strings.Map(mapping, stdlib.Str(args[1]))
			return out, nil
		}},
	})
}
