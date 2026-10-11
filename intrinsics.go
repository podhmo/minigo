// Stdlib intrinsics: a small native surface bound into every engine so
// interpreted code doesn't have to parse GOROOT sources for the common
// cases. Script values are marshalled to Go natives at the boundary
// (goNative) so the real fmt/strconv/... implementations do the work.
// Errors return Go-style (value, err) tuples where the real API has one.
//
// Deliberately small — anything absent falls through to lazy GOROOT source
// interpretation, which stays the documented path for full fidelity.
package minigo

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"html"
	"io"
	"io/fs"
	"maps"
	"math"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	goruntime "runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"text/template"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/podhmo/minigo/minireflect"
	"github.com/podhmo/minigo/runtime"
	"github.com/podhmo/minigo/vm"
)

var (
	memStatsOnce     sync.Once
	memStatsSnapshot goruntime.MemStats
)

// installStdlib binds the intrinsic packages onto the engine's import-path
// table; Bound packages win over source resolution (loadPath checks pkgs).
func (e *Engine) installStdlib() {
	h := &hostHelpers{e: e}
	e.Bind("fmt", map[string]runtime.Value{
		// fmt's interfaces are needed as types by interpreted sources that
		// reflect on them or assert (e.g. template's fmt.Stringer probes,
		// math/big's compile-time fmt.Scanner assertion).
		// Stringer/GoStringer carry their requirement as a real interface
		// AST (same pattern as `error`) so the facade's signature
		// machinery can report NumMethod/Method/MethodByName and check
		// Implements against `String() string`.
		"Stringer": &runtime.TypeDef{
			Name: "fmt.Stringer", Kind: runtime.KindInterface, MReqs: []string{"String"},
			Anon: &ast.InterfaceType{Methods: &ast.FieldList{List: []*ast.Field{{
				Names: []*ast.Ident{{Name: "String"}},
				Type: &ast.FuncType{
					Params:  &ast.FieldList{},
					Results: &ast.FieldList{List: []*ast.Field{{Type: ast.NewIdent("string")}}},
				},
			}}}},
		},
		"GoStringer": &runtime.TypeDef{
			Name: "fmt.GoStringer", Kind: runtime.KindInterface, MReqs: []string{"GoString"},
			Anon: &ast.InterfaceType{Methods: &ast.FieldList{List: []*ast.Field{{
				Names: []*ast.Ident{{Name: "GoString"}},
				Type: &ast.FuncType{
					Params:  &ast.FieldList{},
					Results: &ast.FieldList{List: []*ast.Field{{Type: ast.NewIdent("string")}}},
				},
			}}}},
		},
		"Formatter": &runtime.TypeDef{Name: "fmt.Formatter", Kind: runtime.KindInterface, MReqs: []string{"Format"}},
		"Scanner":   &runtime.TypeDef{Name: "fmt.Scanner", Kind: runtime.KindInterface, MReqs: []string{"Scan"}},
		"State":     &runtime.TypeDef{Name: "fmt.State", Kind: runtime.KindInterface, MReqs: []string{"Write", "Width", "Precision", "Flag"}},
		// Print/Fprint/Sprint need the flat script args: gc's operand
		// spacing reads the reflect Kind, so a named type over string
		// (json.Number) suppresses the space — the host-shaped args hide
		// that inside fmtValue.
		"Print": h.pffn("fmt.Print", 0, func(v runtime.VMCaller, a []any, flat []runtime.Value) (any, error) {
			return retErr(fprintOperands(h.out(), a, flat))
		}, fmt.Print),
		"Println": h.ffn("fmt.Println", -1, 0, func(a []any) (any, error) { return retErr(fmt.Fprintln(h.out(), a...)) }, fmt.Println),
		"Printf": h.ffn("fmt.Printf", 0, 1, func(a []any) (any, error) {
			return retErr(fmt.Fprintf(h.out(), str(a[0]), a[1:]...))
		}),
		// Fprint* take an explicit writer — os.Stdout/os.Stderr arrive as
		// GoValue (unwrapped by fmtArg to the native *os.File).
		"Fprint": h.pffn("fmt.Fprint", 1, func(v runtime.VMCaller, a []any, flat []runtime.Value) (any, error) {
			w, err := asWriterVM(v, a[0])
			if err != nil {
				return nil, err
			}
			return retErr(fprintOperands(w, a[1:], flat[1:]))
		}),
		"Fprintf": h.vffn("fmt.Fprintf", 1, 2, func(v runtime.VMCaller, a []any) (any, error) {
			w, err := asWriterVM(v, a[0])
			if err != nil {
				return nil, err
			}
			return retErr(fmt.Fprintf(w, str(a[1]), a[2:]...))
		}),
		"Fprintln": h.vffn("fmt.Fprintln", -1, 1, func(v runtime.VMCaller, a []any) (any, error) {
			w, err := asWriterVM(v, a[0])
			if err != nil {
				return nil, err
			}
			return retErr(fmt.Fprintln(w, a[1:]...))
		}),
		"Sprint": h.pffn("fmt.Sprint", 0, func(v runtime.VMCaller, a []any, flat []runtime.Value) (any, error) {
			var buf bytes.Buffer
			if _, err := fprintOperands(&buf, a, flat); err != nil {
				return nil, err
			}
			return buf.String(), nil
		}, fmt.Sprint),
		"Sprintln": h.ffn("fmt.Sprintln", -1, 0, func(a []any) (any, error) { return fmt.Sprintln(a...), nil }, fmt.Sprintln),
		"Sprintf": h.ffn("fmt.Sprintf", 0, 1, func(a []any) (any, error) {
			return fmt.Sprintf(str(a[0]), a[1:]...), nil
		}, fmt.Sprintf),
		// The Append family formats like Sprint/Sprintf/Sprintln onto a
		// caller's byte slice. The buffer arg is read from flat (the raw
		// script value): a[i] holds fmtValue boxes for composite args,
		// which byteSlice cannot see through. Source-interpreted packages
		// reach these through e.g. log's output(), which formats via
		// fmt.Appendln.
		"Append": h.pffn("fmt.Append", 1, func(v runtime.VMCaller, a []any, flat []runtime.Value) (any, error) {
			var buf bytes.Buffer
			if _, err := fprintOperands(&buf, a[1:], flat[1:]); err != nil {
				return nil, err
			}
			return append(byteSlice(flat[0]), buf.Bytes()...), nil
		}, fmt.Append),
		"Appendf": h.wffn("fmt.Appendf", 1, 2, func(v runtime.VMCaller, a []any, flat []runtime.Value) (any, error) {
			return fmt.Appendf(byteSlice(flat[0]), str(a[1]), a[2:]...), nil
		}, fmt.Appendf),
		"Appendln": h.pffn("fmt.Appendln", 1, func(v runtime.VMCaller, a []any, flat []runtime.Value) (any, error) {
			return fmt.Appendln(byteSlice(flat[0]), a[1:]...), nil
		}, fmt.Appendln),
		// The Scan family's out-params arrive as script refs; scanFn
		// mirrors each into a host var of the pointee's type, scans, and
		// writes results back through the ref (parse/node.go's
		// complex-literal path needs Sscan).
		"Sscan":   h.scanFn("fmt.Sscan", 1, nil, fmt.Sscan, func(head, ptrs []any) (int, error) { return fmt.Sscan(str(head[0]), ptrs...) }),
		"Sscanf":  h.scanFn("fmt.Sscanf", 2, nil, fmt.Sscanf, func(head, ptrs []any) (int, error) { return fmt.Sscanf(str(head[0]), str(head[1]), ptrs...) }),
		"Sscanln": h.scanFn("fmt.Sscanln", 1, nil, fmt.Sscanln, func(head, ptrs []any) (int, error) { return fmt.Sscanln(str(head[0]), ptrs...) }),
		"Scan":    h.scanFn("fmt.Scan", 0, nil, fmt.Scan, func(_, ptrs []any) (int, error) { return fmt.Scan(ptrs...) }),
		"Scanf":   h.scanFn("fmt.Scanf", 1, nil, fmt.Scanf, func(head, ptrs []any) (int, error) { return fmt.Scanf(str(head[0]), ptrs...) }),
		"Scanln":  h.scanFn("fmt.Scanln", 0, nil, fmt.Scanln, func(_, ptrs []any) (int, error) { return fmt.Scanln(ptrs...) }),
		"Fscan":   h.scanFn("fmt.Fscan", 1, h.scanReader, fmt.Fscan, func(head, ptrs []any) (int, error) { return fmt.Fscan(head[0].(io.Reader), ptrs...) }),
		"Fscanf":  h.scanFn("fmt.Fscanf", 2, h.scanReader, fmt.Fscanf, func(head, ptrs []any) (int, error) { return fmt.Fscanf(head[0].(io.Reader), str(head[1]), ptrs...) }),
		"Fscanln": h.scanFn("fmt.Fscanln", 1, h.scanReader, fmt.Fscanln, func(head, ptrs []any) (int, error) { return fmt.Fscanln(head[0].(io.Reader), ptrs...) }),
		// Errorf is hand-bound: %w verbs wrap the cause like Go's
		// fmt.wrapError so errors.Unwrap/Is/As see the chain.
		"Errorf": &runtime.BuiltinFunc{Name: "fmt.Errorf", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) < 1 {
				return nil, fmt.Errorf("fmt.Errorf needs 1+ args")
			}
			a, flat := fmtArgs(v, args)
			spec := str(a[0])
			spec, tail := rewriteTypeVerbs(spec, a, flat, 0, v)
			a = append(a[:1], tail...)
			spec, wrapPos := rewriteWrapVerbs(spec)
			msg := fmt.Sprintf(spec, a[1:]...)
			if wrapPos >= 0 && wrapPos < len(args)-1 {
				return errVal(&wrapError{msg: msg, err: hostErrOf(v, args[wrapPos+1])}), nil
			}
			return errVal(errors.New(msg)), nil
		}},
	})
	e.Bind("errors", map[string]runtime.Value{
		"New": h.fn("errors.New", func(a []any) (any, error) { return errors.New(str(a[0])), nil }, errors.New),
		// sentinel vars ride GoValue boxes so `err == errors.ErrUnsupported`
		// and errors.Is against the chain see the real objects.
		"ErrUnsupported": &runtime.GoValue{V: errors.ErrUnsupported},
		"Join": &runtime.BuiltinFunc{Name: "errors.Join", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			var errs []error
			for _, x := range args {
				if err := hostErrOf(v, x); err != nil {
					errs = append(errs, err)
				}
			}
			return errVal(errors.Join(errs...)), nil
		}},
		// Is/Unwrap/As take raw runtime args: h.fn's goNative would flatten
		// a script error value to its printed string, losing the type the
		// chain walk needs. hostErrOf keeps the script value inside a
		// scriptError wrapper instead.
		"Is": &runtime.BuiltinFunc{Name: "errors.Is", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("errors.Is needs 2 args")
			}
			e0, e1 := hostErrOf(v, args[0]), hostErrOf(v, args[1])
			if e0 == nil || e1 == nil {
				return e0 == e1, nil
			}
			if s0, ok := e0.(*scriptError); ok {
				// Go's `err == target` fast path: two scriptError boxes
				// around the same script value are one error.
				if s1, ok := e1.(*scriptError); ok && s0.v == s1.v {
					return true, nil
				}
			}
			// script-side walk — host errors.Is's `==` compares error
			// pointers and only walks Unwrap() error, so it misses a
			// scriptError's canonical equality and its []error kids.
			return errIs(e0, e1), nil
		}},
		"Unwrap": &runtime.BuiltinFunc{Name: "errors.Unwrap", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("errors.Unwrap needs 1 arg")
			}
			return errVal(errors.Unwrap(hostErrOf(v, args[0]))), nil
		}},
		// As walks the error tree the way gc's errors.As does — each
		// node's dynamic type, its own As(any) bool, then Unwrap() error
		// and Unwrap() []error children depth-first — and assigns the
		// first cause satisfying the target cell's declared element type.
		"As": &runtime.BuiltinFunc{Name: "errors.As", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("errors.As needs 2 args")
			}
			want := cellElemTyp(args[1])
			sv, ok := findAsTarget(v, hostErrOf(v, args[0]), want, e.ifaceReqSet(want), args[1])
			if ok && runtime.SetRef(args[1], sv) {
				return true, nil
			}
			return false, nil
		}},
		// AsType is the generic form of As: GenFn makes the bind
		// instantiable (`errors.AsType[*E](err)` → a plain builtin that
		// returns the first matching chain element with its bool). The
		// walk shares As's matching rule: an interface E accepts any
		// error, a concrete E matches the element's declared typedef.
		"AsType": &runtime.BuiltinFunc{Name: "errors.AsType", GenFn: func(v runtime.VMCaller, targs []runtime.Value, args []runtime.Value) (runtime.Value, error) {
			if len(targs) != 1 {
				return nil, fmt.Errorf("errors.AsType needs 1 type argument")
			}
			if len(args) != 1 {
				return nil, fmt.Errorf("errors.AsType needs 1 arg")
			}
			want, _ := targs[0].(*runtime.TypeDef)
			// gc returns E's zero on a miss — a typed nil so a
			// pointer-receiver Error() on the result still runs.
			var zero runtime.Value = runtime.NIL
			if want != nil {
				zero = v.Zero(want)
			}
			// target stands in for errors.As's user cell: a node's own
			// As method writes its answer through this pointer.
			target := &runtime.Cell{Elem: zero}
			sv, ok := findAsTarget(v, hostErrOf(v, args[0]), want, e.ifaceReqSet(want), target)
			if ok {
				return &runtime.Tuple{Elems: []runtime.Value{sv, true}}, nil
			}
			return &runtime.Tuple{Elems: []runtime.Value{zero, false}}, nil
		}},
	})
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
			return seqOf("strings.Lines", strPieces(slices.Collect(strings.Lines(str(args[0]))))), nil
		}},
		"SplitSeq": &runtime.BuiltinFunc{Name: "strings.SplitSeq", Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("strings.SplitSeq needs 2 args")
			}
			return seqOf("strings.SplitSeq", strPieces(slices.Collect(strings.SplitSeq(str(args[0]), str(args[1]))))), nil
		}},
		"SplitAfterSeq": &runtime.BuiltinFunc{Name: "strings.SplitAfterSeq", Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("strings.SplitAfterSeq needs 2 args")
			}
			return seqOf("strings.SplitAfterSeq", strPieces(slices.Collect(strings.SplitAfterSeq(str(args[0]), str(args[1]))))), nil
		}},
		"FieldsSeq": &runtime.BuiltinFunc{Name: "strings.FieldsSeq", Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("strings.FieldsSeq needs 1 arg")
			}
			return seqOf("strings.FieldsSeq", strPieces(slices.Collect(strings.FieldsSeq(str(args[0]))))), nil
		}},
		"FieldsFuncSeq": &runtime.BuiltinFunc{Name: "strings.FieldsFuncSeq", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("strings.FieldsFuncSeq needs 2 args")
			}
			return seqOf("strings.FieldsFuncSeq", strPieces(strings.FieldsFunc(str(args[0]), runePred(v, args[1])))), nil
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
	e.Bind("strconv", map[string]runtime.Value{
		"Atoi":    h.fn("strconv.Atoi", func(a []any) (any, error) { return retErr2(strconv.Atoi(str(a[0]))) }),
		"Itoa":    h.fn("strconv.Itoa", func(a []any) (any, error) { return strconv.Itoa(intOf(a[0])), nil }, strconv.Itoa),
		"Quote":   h.fn("strconv.Quote", func(a []any) (any, error) { return strconv.Quote(str(a[0])), nil }, strconv.Quote),
		"Unquote": h.fn("strconv.Unquote", func(a []any) (any, error) { return retErr2(strconv.Unquote(str(a[0]))) }),
		"UnquoteChar": h.fn2("strconv.UnquoteChar", func(a []any) (any, error) {
			r, multibyte, tail, err := strconv.UnquoteChar(str(a[0]), byte(int64Of(a[1])))
			return &runtime.Tuple{Elems: []runtime.Value{namedSized(r, int64(r)), multibyte, tail, errVal(err)}}, nil
		}),
		"ParseUint": h.fn3("strconv.ParseUint", func(a []any) (any, error) {
			return retErr2(strconv.ParseUint(str(a[0]), intOf(a[1]), intOf(a[2])))
		}),
		"FormatFloat": h.fn3("strconv.FormatFloat", func(a []any) (any, error) {
			f, _ := a[0].(float64)
			return strconv.FormatFloat(f, byte(intOf(a[1])), intOf(a[2]), 64), nil
		}),
		"FormatBool": h.fn("strconv.FormatBool", func(a []any) (any, error) {
			b, _ := a[0].(bool)
			return strconv.FormatBool(b), nil
		}),
		"ParseInt": h.fn3("strconv.ParseInt", func(a []any) (any, error) { return retErr2(strconv.ParseInt(str(a[0]), intOf(a[1]), intOf(a[2]))) }),
		"ParseFloat": h.fn2("strconv.ParseFloat", func(a []any) (any, error) {
			return retErr2(strconv.ParseFloat(str(a[0]), intOf(a[1])))
		}),
		"ParseBool": h.fn("strconv.ParseBool", func(a []any) (any, error) { return retErr2(strconv.ParseBool(str(a[0]))) }),
		"FormatInt": h.fn2("strconv.FormatInt", func(a []any) (any, error) { return strconv.FormatInt(int64Of(a[0]), intOf(a[1])), nil }, strconv.FormatInt),
		"FormatUint": h.fn2("strconv.FormatUint", func(a []any) (any, error) {
			return strconv.FormatUint(uint64(int64Of(a[0])), intOf(a[1])), nil
		}),
		"QuoteToASCII": h.fn("strconv.QuoteToASCII", func(a []any) (any, error) { return strconv.QuoteToASCII(str(a[0])), nil }, strconv.QuoteToASCII),
		"QuoteRune":    h.fn("strconv.QuoteRune", func(a []any) (any, error) { return strconv.QuoteRune(runeOf(a[0])), nil }, strconv.QuoteRune),
		"IsPrint":      h.fn("strconv.IsPrint", func(a []any) (any, error) { return strconv.IsPrint(runeOf(a[0])), nil }, strconv.IsPrint),
		"IsGraphic":    h.fn("strconv.IsGraphic", func(a []any) (any, error) { return strconv.IsGraphic(runeOf(a[0])), nil }, strconv.IsGraphic),
		"CanBackquote": h.fn("strconv.CanBackquote", func(a []any) (any, error) { return strconv.CanBackquote(str(a[0])), nil }, strconv.CanBackquote),
		// The Append family appends to dst and returns the extended slice;
		// callers always use the return value, so crossing dst as a host
		// copy is faithful (net/http's writeStatusLine builds into a
		// scratch buffer this way).
		"AppendBool": h.fn2("strconv.AppendBool", func(a []any) (any, error) {
			b, _ := a[1].(bool)
			return strconv.AppendBool(byteSlice(a[0]), b), nil
		}),
		"AppendInt": h.fn3("strconv.AppendInt", func(a []any) (any, error) {
			return strconv.AppendInt(byteSlice(a[0]), int64Of(a[1]), intOf(a[2])), nil
		}),
		"AppendUint": h.fn3("strconv.AppendUint", func(a []any) (any, error) {
			return strconv.AppendUint(byteSlice(a[0]), uint64(int64Of(a[1])), intOf(a[2])), nil
		}),
		"AppendFloat": h.fn("strconv.AppendFloat", func(a []any) (any, error) {
			f, _ := a[1].(float64)
			return strconv.AppendFloat(byteSlice(a[0]), f, byte(intOf(a[2])), intOf(a[3]), intOf(a[4])), nil
		}),
		"AppendQuote": h.fn2("strconv.AppendQuote", func(a []any) (any, error) {
			return strconv.AppendQuote(byteSlice(a[0]), str(a[1])), nil
		}),
		"AppendQuoteToASCII": h.fn2("strconv.AppendQuoteToASCII", func(a []any) (any, error) {
			return strconv.AppendQuoteToASCII(byteSlice(a[0]), str(a[1])), nil
		}),
		"AppendQuoteRune": h.fn2("strconv.AppendQuoteRune", func(a []any) (any, error) {
			return strconv.AppendQuoteRune(byteSlice(a[0]), runeOf(a[1])), nil
		}),
		"AppendQuoteRuneToASCII": h.fn2("strconv.AppendQuoteRuneToASCII", func(a []any) (any, error) {
			return strconv.AppendQuoteRuneToASCII(byteSlice(a[0]), runeOf(a[1])), nil
		}),
		// the host's value is the architecture-appropriate one (64-bit
		// only build); interpreted stdlib code reaches it, e.g.
		// encoding/base64's decoder.
		"IntSize": &runtime.UConst{V: constant.MakeInt64(strconv.IntSize)},
		// sentinels ride GoValue boxes so `err == strconv.ErrSyntax` and
		// errors.Is against the chain see the real objects; *NumError
		// already arrives boxed from the parse functions and unwraps to
		// them natively. The NumError typedef makes `var e *strconv.NumError`
		// (an errors.As target) resolvable.
		"ErrSyntax": &runtime.GoValue{V: strconv.ErrSyntax},
		"ErrRange":  &runtime.GoValue{V: strconv.ErrRange},
		"NumError":  hostType("strconv.NumError", func() any { return &strconv.NumError{} }),
	})
	e.Bind("bytes", map[string]runtime.Value{
		// `var buf bytes.Buffer` / `new(bytes.Buffer)` box a real
		// *bytes.Buffer so methods (WriteString, String, ...) dispatch
		// on the host value.
		"Buffer":    hostType("bytes.Buffer", func() any { return &bytes.Buffer{} }),
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
	e.Bind("unicode", map[string]runtime.Value{
		"IsControl": h.fn("unicode.IsControl", func(a []any) (any, error) { return unicode.IsControl(runeOf(a[0])), nil }, unicode.IsControl),
		"IsDigit":   h.fn("unicode.IsDigit", func(a []any) (any, error) { return unicode.IsDigit(runeOf(a[0])), nil }, unicode.IsDigit),
		"IsGraphic": h.fn("unicode.IsGraphic", func(a []any) (any, error) { return unicode.IsGraphic(runeOf(a[0])), nil }, unicode.IsGraphic),
		"IsLetter":  h.fn("unicode.IsLetter", func(a []any) (any, error) { return unicode.IsLetter(runeOf(a[0])), nil }, unicode.IsLetter),
		"IsLower":   h.fn("unicode.IsLower", func(a []any) (any, error) { return unicode.IsLower(runeOf(a[0])), nil }, unicode.IsLower),
		"IsMark":    h.fn("unicode.IsMark", func(a []any) (any, error) { return unicode.IsMark(runeOf(a[0])), nil }, unicode.IsMark),
		"IsNumber":  h.fn("unicode.IsNumber", func(a []any) (any, error) { return unicode.IsNumber(runeOf(a[0])), nil }, unicode.IsNumber),
		"IsPrint":   h.fn("unicode.IsPrint", func(a []any) (any, error) { return unicode.IsPrint(runeOf(a[0])), nil }, unicode.IsPrint),
		"IsPunct":   h.fn("unicode.IsPunct", func(a []any) (any, error) { return unicode.IsPunct(runeOf(a[0])), nil }, unicode.IsPunct),
		"IsSpace":   h.fn("unicode.IsSpace", func(a []any) (any, error) { return unicode.IsSpace(runeOf(a[0])), nil }, unicode.IsSpace),
		"IsSymbol":  h.fn("unicode.IsSymbol", func(a []any) (any, error) { return unicode.IsSymbol(runeOf(a[0])), nil }, unicode.IsSymbol),
		"IsTitle":   h.fn("unicode.IsTitle", func(a []any) (any, error) { return unicode.IsTitle(runeOf(a[0])), nil }, unicode.IsTitle),
		"IsUpper":   h.fn("unicode.IsUpper", func(a []any) (any, error) { return unicode.IsUpper(runeOf(a[0])), nil }, unicode.IsUpper),
		"ToLower":   h.fn("unicode.ToLower", func(a []any) (any, error) { return unicode.ToLower(runeOf(a[0])), nil }),
		"ToUpper":   h.fn("unicode.ToUpper", func(a []any) (any, error) { return unicode.ToUpper(runeOf(a[0])), nil }),
		"ToTitle":   h.fn("unicode.ToTitle", func(a []any) (any, error) { return unicode.ToTitle(runeOf(a[0])), nil }),
		"To":        h.fn2("unicode.To", func(a []any) (any, error) { return unicode.To(intOf(a[0]), runeOf(a[1])), nil }),
		// RangeTable membership checks — encoding/xml's isName builds its
		// own tables and calls these.
		"Is": h.fn2("unicode.Is", func(a []any) (any, error) {
			return unicode.Is(goNative(a[0]).(*unicode.RangeTable), runeOf(a[1])), nil
		}, unicode.Is),
		"In": h.fn2("unicode.In", func(a []any) (any, error) {
			var tabs []*unicode.RangeTable
			for _, t := range a[1:] {
				tabs = append(tabs, goNative(t).(*unicode.RangeTable))
			}
			return unicode.In(runeOf(a[0]), tabs...), nil
		}),
		"UpperCase": int64(unicode.UpperCase), "LowerCase": int64(unicode.LowerCase), "TitleCase": int64(unicode.TitleCase),
		// these consts are untyped in Go — they adapt to an operand's
		// declared type like a literal, so they ride as UConst.
		"MaxRune":         &runtime.UConst{V: constant.MakeInt64(unicode.MaxRune), Rune: true},
		"MaxASCII":        &runtime.UConst{V: constant.MakeInt64(unicode.MaxASCII), Rune: true},
		"ReplacementChar": &runtime.UConst{V: constant.MakeInt64(unicode.ReplacementChar), Rune: true},
		// stdlib code (encoding/xml's init) builds RangeTables as composite
		// literals — bind the range structs as host types.
		"RangeTable": hostType("unicode.RangeTable", func() any { return &unicode.RangeTable{} }),
		"Range16":    hostType("unicode.Range16", func() any { return &unicode.Range16{} }),
		"Range32":    hostType("unicode.Range32", func() any { return &unicode.Range32{} }),
		"CaseRange":  hostType("unicode.CaseRange", func() any { return &unicode.CaseRange{} }),
	})
	e.Bind("unicode/utf8", map[string]runtime.Value{
		"RuneCountInString": h.fn("utf8.RuneCountInString", func(a []any) (any, error) { return utf8.RuneCountInString(str(a[0])), nil }),
		"RuneCount":         h.fn("utf8.RuneCount", func(a []any) (any, error) { return utf8.RuneCount(byteSlice(a[0])), nil }),
		"RuneLen":           h.fn("utf8.RuneLen", func(a []any) (any, error) { return utf8.RuneLen(runeOf(a[0])), nil }),
		"RuneStart":         h.fn("utf8.RuneStart", func(a []any) (any, error) { return utf8.RuneStart(byte(intOf(a[0]))), nil }, utf8.RuneStart),
		"Valid":             h.fn("utf8.Valid", func(a []any) (any, error) { return utf8.Valid(byteSlice(a[0])), nil }, utf8.Valid),
		"ValidString":       h.fn("utf8.ValidString", func(a []any) (any, error) { return utf8.ValidString(str(a[0])), nil }, utf8.ValidString),
		"ValidRune":         h.fn("utf8.ValidRune", func(a []any) (any, error) { return utf8.ValidRune(runeOf(a[0])), nil }, utf8.ValidRune),
		"FullRune":          h.fn("utf8.FullRune", func(a []any) (any, error) { return utf8.FullRune(byteSlice(a[0])), nil }, utf8.FullRune),
		"FullRuneInString":  h.fn("utf8.FullRuneInString", func(a []any) (any, error) { return utf8.FullRuneInString(str(a[0])), nil }, utf8.FullRuneInString),
		"DecodeRuneInString": h.fn("utf8.DecodeRuneInString", func(a []any) (any, error) {
			r, n := utf8.DecodeRuneInString(str(a[0]))
			return &runtime.Tuple{Elems: []runtime.Value{scriptVal(r), int64(n)}}, nil
		}),
		"DecodeRune": h.fn("utf8.DecodeRune", func(a []any) (any, error) {
			r, n := utf8.DecodeRune(byteSlice(a[0]))
			return &runtime.Tuple{Elems: []runtime.Value{scriptVal(r), int64(n)}}, nil
		}),
		"DecodeLastRuneInString": h.fn("utf8.DecodeLastRuneInString", func(a []any) (any, error) {
			r, n := utf8.DecodeLastRuneInString(str(a[0]))
			return &runtime.Tuple{Elems: []runtime.Value{scriptVal(r), int64(n)}}, nil
		}),
		"DecodeLastRune": h.fn("utf8.DecodeLastRune", func(a []any) (any, error) {
			r, n := utf8.DecodeLastRune(byteSlice(a[0]))
			return &runtime.Tuple{Elems: []runtime.Value{scriptVal(r), int64(n)}}, nil
		}),
		// gc signature: writes into the caller's []byte (through the
		// script slice's own backing so sibling views see the bytes) and
		// returns the width. A too-small p panics index-out-of-range at
		// p[width-1] before any write; out-of-range and surrogate runes
		// write RuneError's encoding.
		"EncodeRune": &runtime.BuiltinFunc{Name: "utf8.EncodeRune", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("utf8.EncodeRune needs 2 args, got %d", len(args))
			}
			var elems []runtime.Value
			var styp *runtime.TypeDef
			switch x := runtime.Unwrap(args[0]).(type) {
			case runtime.Nil:
				// an untyped nil encodes like an empty []byte
			case *runtime.TypedNil:
				if x.Typ == nil || x.Typ.Kind != runtime.KindSlice {
					return nil, fmt.Errorf("utf8.EncodeRune on %T", args[0])
				}
				styp = x.Typ
			default:
				s, ok := sliceOf(args[0])
				if !ok {
					return nil, fmt.Errorf("utf8.EncodeRune on %T", args[0])
				}
				elems, styp = s.Elems, s.Typ
			}
			var buf [utf8.UTFMax]byte
			n := utf8.EncodeRune(buf[:], rune(int64Of(goNative(args[1]))))
			if len(elems) < n {
				// gc's `_ = p[width-1]` bounds check fires first.
				panic(runtime.BoundsPanic(n-1, len(elems)))
			}
			var et *runtime.TypeDef
			if styp != nil {
				et = vc.TypeOf(vc.ElemZero(styp))
			}
			for i, b := range buf[:n] {
				a := runtime.Value(int64(b))
				if et != nil {
					// elements tag like the declared element type, the
					// same conversion an index store applies.
					if cv, err := vc.Convert(et, a); err == nil {
						a = cv
					}
				}
				elems[i] = a
			}
			return int64(n), nil
		}, Target: utf8.EncodeRune},
		"AppendRune": &runtime.BuiltinFunc{Name: "utf8.AppendRune", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("utf8.AppendRune needs 2 args, got %d", len(args))
			}
			// the append runs on the script slice's own backing — going
			// through goNative would copy it, losing the spare-capacity
			// sharing Go's append preserves (b[:2] sees the rune and the
			// writes through the result).
			var elems []runtime.Value
			var styp *runtime.TypeDef
			switch x := runtime.Unwrap(args[0]).(type) {
			case runtime.Nil:
				// an untyped nil appends like an empty []byte
			case *runtime.TypedNil:
				if x.Typ == nil || x.Typ.Kind != runtime.KindSlice {
					return nil, fmt.Errorf("utf8.AppendRune on %T", args[0])
				}
				styp = x.Typ
			default:
				s, ok := sliceOf(args[0])
				if !ok {
					return nil, fmt.Errorf("utf8.AppendRune on %T", args[0])
				}
				elems, styp = s.Elems, s.Typ
			}
			var buf [utf8.UTFMax]byte
			n := utf8.EncodeRune(buf[:], rune(int64Of(goNative(args[1]))))
			var et *runtime.TypeDef
			if styp != nil {
				et = vc.TypeOf(vc.ElemZero(styp))
			}
			add := make([]runtime.Value, n)
			for i, b := range buf[:n] {
				a := runtime.Value(int64(b))
				if et != nil {
					// elements tag like the declared element type, the
					// same conversion the append builtin applies.
					if cv, err := vc.Convert(et, a); err == nil {
						a = cv
					}
				}
				add[i] = a
			}
			return &runtime.Slice{Elems: append(elems, add...), Typ: anonSliceTyp("byte")}, nil
		}, Target: utf8.AppendRune},
		"UTFMax":    &runtime.UConst{V: constant.MakeInt64(utf8.UTFMax)},
		"RuneError": &runtime.UConst{V: constant.MakeInt64(utf8.RuneError), Rune: true},
		"RuneSelf":  &runtime.UConst{V: constant.MakeInt64(utf8.RuneSelf)},
	})
	// math's float consts are untyped literals with more precision than
	// float64 — re-parse the stdlib's source literal so constant-domain
	// math (and float32 conversions) see the same value gc does.
	mathFloat := func(lit string) runtime.Value {
		return &runtime.UConst{V: constant.MakeFromLiteral(lit, token.FLOAT, 0)}
	}
	mathLn2 := constant.MakeFromLiteral("0.693147180559945309417232121458176568075500134360255254120680009", token.FLOAT, 0)
	mathLn10 := constant.MakeFromLiteral("2.30258509299404568401799145468436420760110148862877297603332790", token.FLOAT, 0)
	e.Bind("math", map[string]runtime.Value{
		// math's consts are all untyped — they keep constant-domain
		// precision and adapt to an operand's declared type.
		"Pi":      mathFloat("3.14159265358979323846264338327950288419716939937510582097494459"),
		"E":       mathFloat("2.71828182845904523536028747135266249775724709369995957496696763"),
		"Phi":     mathFloat("1.61803398874989484820458683436563811772030917980576286213544862"),
		"Sqrt2":   mathFloat("1.41421356237309504880168872420969807856967187537694807317667974"),
		"SqrtE":   mathFloat("1.64872127070012814684865078781416357165377610071014801157507931"),
		"SqrtPi":  mathFloat("1.77245385090551602729816748334114518279754945612238712821380779"),
		"SqrtPhi": mathFloat("1.27201964951406896425242246173749149171560804184009624861664038"),
		"Ln2":     &runtime.UConst{V: mathLn2},
		"Log2E":   &runtime.UConst{V: constant.BinaryOp(constant.MakeInt64(1), token.QUO, mathLn2)}, // 1 / Ln2
		"Ln10":    &runtime.UConst{V: mathLn10},
		"Log10E":  &runtime.UConst{V: constant.BinaryOp(constant.MakeInt64(1), token.QUO, mathLn10)}, // 1 / Ln10
		"MaxInt":  &runtime.UConst{V: constant.MakeInt64(math.MaxInt)}, "MinInt": &runtime.UConst{V: constant.MakeInt64(math.MinInt)},
		"MaxInt8": &runtime.UConst{V: constant.MakeInt64(math.MaxInt8)}, "MinInt8": &runtime.UConst{V: constant.MakeInt64(math.MinInt8)},
		"MaxInt16": &runtime.UConst{V: constant.MakeInt64(math.MaxInt16)}, "MinInt16": &runtime.UConst{V: constant.MakeInt64(math.MinInt16)},
		"MaxInt32": &runtime.UConst{V: constant.MakeInt64(math.MaxInt32)}, "MinInt32": &runtime.UConst{V: constant.MakeInt64(math.MinInt32)},
		"MaxInt64": &runtime.UConst{V: constant.MakeInt64(math.MaxInt64)}, "MinInt64": &runtime.UConst{V: constant.MakeInt64(math.MinInt64)},
		"MaxUint8":  &runtime.UConst{V: constant.MakeInt64(math.MaxUint8)},
		"MaxUint16": &runtime.UConst{V: constant.MakeInt64(math.MaxUint16)},
		"MaxUint32": &runtime.UConst{V: constant.MakeInt64(math.MaxUint32)},
		// the 64-bit ceiling constants don't fit int64 — they stay
		// untyped constants so `x << (math.MaxUint + 0.)` still
		// evaluates in the constant domain like Go's declaration.
		"MaxUint64":  &runtime.UConst{V: constant.MakeUint64(math.MaxUint64)},
		"MaxUint":    &runtime.UConst{V: constant.MakeUint64(math.MaxUint)},
		"MaxUintptr": &runtime.UConst{V: constant.MakeUint64(math.MaxUint64)},
		// the float limit consts ARE their float64 value exactly
		// (0x1p127*(1+(1-0x1p-23)) etc. are representable), so
		// MakeFloat64 reproduces the stdlib constant bit-for-bit — a
		// decimal literal would parse to a different constant.
		"MaxFloat32":             &runtime.UConst{V: constant.MakeFloat64(math.MaxFloat32)},
		"MaxFloat64":             &runtime.UConst{V: constant.MakeFloat64(math.MaxFloat64)},
		"SmallestNonzeroFloat32": &runtime.UConst{V: constant.MakeFloat64(math.SmallestNonzeroFloat32)},
		"SmallestNonzeroFloat64": &runtime.UConst{V: constant.MakeFloat64(math.SmallestNonzeroFloat64)},
		"Abs":                    h.fn("math.Abs", func(a []any) (any, error) { return math.Abs(floatOf(a[0])), nil }, math.Abs),
		"Ceil":                   h.fn("math.Ceil", func(a []any) (any, error) { return math.Ceil(floatOf(a[0])), nil }, math.Ceil),
		"Floor":                  h.fn("math.Floor", func(a []any) (any, error) { return math.Floor(floatOf(a[0])), nil }, math.Floor),
		"Round":                  h.fn("math.Round", func(a []any) (any, error) { return math.Round(floatOf(a[0])), nil }, math.Round),
		"RoundToEven":            h.fn("math.RoundToEven", func(a []any) (any, error) { return math.RoundToEven(floatOf(a[0])), nil }, math.RoundToEven),
		"Trunc":                  h.fn("math.Trunc", func(a []any) (any, error) { return math.Trunc(floatOf(a[0])), nil }, math.Trunc),
		"Sqrt":                   h.fn("math.Sqrt", func(a []any) (any, error) { return math.Sqrt(floatOf(a[0])), nil }, math.Sqrt),
		"Cbrt":                   h.fn("math.Cbrt", func(a []any) (any, error) { return math.Cbrt(floatOf(a[0])), nil }, math.Cbrt),
		"Hypot":                  h.fn2("math.Hypot", func(a []any) (any, error) { return math.Hypot(floatOf(a[0]), floatOf(a[1])), nil }, math.Hypot),
		"Pow":                    h.fn2("math.Pow", func(a []any) (any, error) { return math.Pow(floatOf(a[0]), floatOf(a[1])), nil }, math.Pow),
		"Pow10":                  h.fn("math.Pow10", func(a []any) (any, error) { return math.Pow10(intOf(a[0])), nil }, math.Pow10),
		"Exp":                    h.fn("math.Exp", func(a []any) (any, error) { return math.Exp(floatOf(a[0])), nil }, math.Exp),
		"Exp2":                   h.fn("math.Exp2", func(a []any) (any, error) { return math.Exp2(floatOf(a[0])), nil }, math.Exp2),
		"Expm1":                  h.fn("math.Expm1", func(a []any) (any, error) { return math.Expm1(floatOf(a[0])), nil }, math.Expm1),
		"Log":                    h.fn("math.Log", func(a []any) (any, error) { return math.Log(floatOf(a[0])), nil }, math.Log),
		"Frexp": h.fn("math.Frexp", func(a []any) (any, error) {
			frac, exp := math.Frexp(floatOf(a[0]))
			return &runtime.Tuple{Elems: []runtime.Value{frac, int64(exp)}}, nil
		}),
		"Modf": h.fn("math.Modf", func(a []any) (any, error) {
			i, frac := math.Modf(floatOf(a[0]))
			return &runtime.Tuple{Elems: []runtime.Value{i, frac}}, nil
		}),
		"Log2":            h.fn("math.Log2", func(a []any) (any, error) { return math.Log2(floatOf(a[0])), nil }, math.Log2),
		"Log10":           h.fn("math.Log10", func(a []any) (any, error) { return math.Log10(floatOf(a[0])), nil }, math.Log10),
		"Log1p":           h.fn("math.Log1p", func(a []any) (any, error) { return math.Log1p(floatOf(a[0])), nil }, math.Log1p),
		"Mod":             h.fn2("math.Mod", func(a []any) (any, error) { return math.Mod(floatOf(a[0]), floatOf(a[1])), nil }, math.Mod),
		"Remainder":       h.fn2("math.Remainder", func(a []any) (any, error) { return math.Remainder(floatOf(a[0]), floatOf(a[1])), nil }, math.Remainder),
		"Max":             h.fn2("math.Max", func(a []any) (any, error) { return math.Max(floatOf(a[0]), floatOf(a[1])), nil }, math.Max),
		"Min":             h.fn2("math.Min", func(a []any) (any, error) { return math.Min(floatOf(a[0]), floatOf(a[1])), nil }, math.Min),
		"Dim":             h.fn2("math.Dim", func(a []any) (any, error) { return math.Dim(floatOf(a[0]), floatOf(a[1])), nil }, math.Dim),
		"Sin":             h.fn("math.Sin", func(a []any) (any, error) { return math.Sin(floatOf(a[0])), nil }, math.Sin),
		"Cos":             h.fn("math.Cos", func(a []any) (any, error) { return math.Cos(floatOf(a[0])), nil }, math.Cos),
		"Tan":             h.fn("math.Tan", func(a []any) (any, error) { return math.Tan(floatOf(a[0])), nil }, math.Tan),
		"Asin":            h.fn("math.Asin", func(a []any) (any, error) { return math.Asin(floatOf(a[0])), nil }, math.Asin),
		"Acos":            h.fn("math.Acos", func(a []any) (any, error) { return math.Acos(floatOf(a[0])), nil }, math.Acos),
		"Atan":            h.fn("math.Atan", func(a []any) (any, error) { return math.Atan(floatOf(a[0])), nil }, math.Atan),
		"Atan2":           h.fn2("math.Atan2", func(a []any) (any, error) { return math.Atan2(floatOf(a[0]), floatOf(a[1])), nil }, math.Atan2),
		"Sinh":            h.fn("math.Sinh", func(a []any) (any, error) { return math.Sinh(floatOf(a[0])), nil }, math.Sinh),
		"Cosh":            h.fn("math.Cosh", func(a []any) (any, error) { return math.Cosh(floatOf(a[0])), nil }, math.Cosh),
		"Tanh":            h.fn("math.Tanh", func(a []any) (any, error) { return math.Tanh(floatOf(a[0])), nil }, math.Tanh),
		"Erf":             h.fn("math.Erf", func(a []any) (any, error) { return math.Erf(floatOf(a[0])), nil }, math.Erf),
		"Erfc":            h.fn("math.Erfc", func(a []any) (any, error) { return math.Erfc(floatOf(a[0])), nil }, math.Erfc),
		"Gamma":           h.fn("math.Gamma", func(a []any) (any, error) { return math.Gamma(floatOf(a[0])), nil }, math.Gamma),
		"Ldexp":           h.fn2("math.Ldexp", func(a []any) (any, error) { return math.Ldexp(floatOf(a[0]), intOf(a[1])), nil }, math.Ldexp),
		"Nextafter":       h.fn2("math.Nextafter", func(a []any) (any, error) { return math.Nextafter(floatOf(a[0]), floatOf(a[1])), nil }, math.Nextafter),
		"Copysign":        h.fn2("math.Copysign", func(a []any) (any, error) { return math.Copysign(floatOf(a[0]), floatOf(a[1])), nil }, math.Copysign),
		"Signbit":         h.fn("math.Signbit", func(a []any) (any, error) { return math.Signbit(floatOf(a[0])), nil }, math.Signbit),
		"Float32bits":     h.fn("math.Float32bits", func(a []any) (any, error) { return math.Float32bits(float32(floatOf(a[0]))), nil }, math.Float32bits),
		"Float64bits":     h.fn("math.Float64bits", func(a []any) (any, error) { return math.Float64bits(floatOf(a[0])), nil }, math.Float64bits),
		"Float32frombits": h.fn("math.Float32frombits", func(a []any) (any, error) { return math.Float32frombits(uint32(intOf(a[0]))), nil }, math.Float32frombits),
		"Float64frombits": h.fn("math.Float64frombits", func(a []any) (any, error) { return math.Float64frombits(uint64(intOf(a[0]))), nil }, math.Float64frombits),
		"IsNaN":           h.fn("math.IsNaN", func(a []any) (any, error) { return math.IsNaN(floatOf(a[0])), nil }, math.IsNaN),
		"IsInf":           h.fn2("math.IsInf", func(a []any) (any, error) { return math.IsInf(floatOf(a[0]), intOf(a[1])), nil }, math.IsInf),
		"NaN":             h.fn("math.NaN", func(a []any) (any, error) { return math.NaN(), nil }, math.NaN),
		"Inf":             h.fn("math.Inf", func(a []any) (any, error) { return math.Inf(intOf(a[0])), nil }, math.Inf),
	})
	e.Bind("regexp", map[string]runtime.Value{
		"Compile":     h.fn("regexp.Compile", func(a []any) (any, error) { return retErr2(regexp.Compile(str(a[0]))) }),
		"MustCompile": h.fn("regexp.MustCompile", func(a []any) (any, error) { return regexp.MustCompile(str(a[0])), nil }, regexp.MustCompile),
		"MatchString": h.fn2("regexp.MatchString", func(a []any) (any, error) { return retErr2(regexp.MatchString(str(a[0]), str(a[1]))) }),
		"Match":       h.fn2("regexp.Match", func(a []any) (any, error) { return retErr2(regexp.Match(str(a[0]), byteSlice(a[1]))) }),
		"QuoteMeta":   h.fn("regexp.QuoteMeta", func(a []any) (any, error) { return regexp.QuoteMeta(str(a[0])), nil }, regexp.QuoteMeta),
	})
	e.Bind("encoding/base64", map[string]runtime.Value{
		"StdEncoding":    &runtime.GoValue{V: base64.StdEncoding},
		"URLEncoding":    &runtime.GoValue{V: base64.URLEncoding},
		"RawStdEncoding": &runtime.GoValue{V: base64.RawStdEncoding},
		"RawURLEncoding": &runtime.GoValue{V: base64.RawURLEncoding},
	})
	e.Bind("encoding/hex", map[string]runtime.Value{
		"EncodeToString": h.fn("hex.EncodeToString", func(a []any) (any, error) { return hex.EncodeToString(byteSlice(a[0])), nil }, hex.EncodeToString),
		"DecodeString":   h.fn("hex.DecodeString", func(a []any) (any, error) { return retErr2(hex.DecodeString(str(a[0]))) }),
		"EncodedLen":     h.fn("hex.EncodedLen", func(a []any) (any, error) { return hex.EncodedLen(intOf(a[0])), nil }),
		"DecodedLen":     h.fn("hex.DecodedLen", func(a []any) (any, error) { return hex.DecodedLen(intOf(a[0])), nil }),
	})
	e.Bind("encoding/json", map[string]runtime.Value{
		"Number": &runtime.TypeDef{Name: "encoding/json.Number", Kind: runtime.KindNamedBasic, Anon: ast.NewIdent("string"), HostNew: func() any { return json.Number("") }},
		// Marshal/MarshalIndent take raw runtime args — h.fn's goNative
		// would stringify *runtime.Struct before goJSON can field-map it.
		"Marshal": &runtime.BuiltinFunc{Name: "json.Marshal", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("json.Marshal needs 1 arg, got %d", len(args))
			}
			b, err := json.Marshal(goJSON(v, args[0]))
			var se *jsonScriptErr
			if errors.As(err, &se) {
				return &runtime.Tuple{Elems: []runtime.Value{scriptVal([]byte{}), errVal(jsonMarshalerErr(v, args[0], se.sv))}}, nil
			}
			return &runtime.Tuple{Elems: []runtime.Value{scriptVal(b), errVal(err)}}, nil
		}},
		"MarshalIndent": &runtime.BuiltinFunc{Name: "json.MarshalIndent", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 3 {
				return nil, fmt.Errorf("json.MarshalIndent needs 3 args, got %d", len(args))
			}
			b, err := json.MarshalIndent(goJSON(v, args[0]), str(goNative(args[1])), str(goNative(args[2])))
			var se *jsonScriptErr
			if errors.As(err, &se) {
				return &runtime.Tuple{Elems: []runtime.Value{scriptVal([]byte{}), errVal(jsonMarshalerErr(v, args[0], se.sv))}}, nil
			}
			return &runtime.Tuple{Elems: []runtime.Value{scriptVal(b), errVal(err)}}, nil
		}},
		// Two shapes: Unmarshal(data) decodes into the runtime value tree
		// (maps/slices/scalars) and returns it; Unmarshal(data, &v) decodes
		// into the pointer target like the stdlib and returns just error.
		"Unmarshal": &runtime.BuiltinFunc{Name: "json.Unmarshal", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) == 1 {
				var dec any
				err := json.Unmarshal(byteSlice(goNative(args[0])), &dec)
				return &runtime.Tuple{Elems: []runtime.Value{scriptVal(jsonDeep(dec)), errVal(err)}}, nil
			}
			if len(args) != 2 {
				return nil, fmt.Errorf("json.Unmarshal needs 1 or 2 args, got %d", len(args))
			}
			// gc: Unmarshal(nil) / Unmarshal(non-pointer T) is an
			// InvalidUnmarshalError before any decoding happens.
			if _, ok := runtime.Deref(args[1]); !ok {
				atd := v.TypeOf(args[1])
				if atd == nil {
					return errVal(fmt.Errorf("json: Unmarshal(nil)")), nil
				}
				return errVal(fmt.Errorf("json: Unmarshal(non-pointer %s)", runtime.DisplayName(atd))), nil
			}
			data := byteSlice(goNative(args[0]))
			// gc validates the input up front (checkValid): a syntax
			// error reports before any UnmarshalJSON dispatch.
			var dec any
			if err := json.Unmarshal(data, &dec); err != nil {
				return errVal(err), nil
			}
			// a target whose method set offers UnmarshalJSON decodes
			// itself — gc's indirect() hands it the raw literal instead
			// of walking by shape. The pointer's method set is checked,
			// so both value- and pointer-receiver methods qualify.
			// gc's indirect() allocates a nil pointer before the
			// call — without it the method derefs nil. The member
			// probe runs after the realloc so the bound receiver
			// sees the fresh pointee.
			if prior0, _ := runtime.Deref(args[1]); jsonNilish(prior0) {
				if ptd := derefTyp(v.TypeOf(args[1])); ptd != nil && ptd.Kind == runtime.KindPointer {
					if z := v.ElemZero(ptd); z != nil {
						runtime.SetRef(args[1], &runtime.Cell{Elem: z})
					}
				}
			}
			if m, ok := runtime.IfaceMember(v, args[1], "UnmarshalJSON"); ok && jsonUnmarshalShape(m) {
				r, cerr := v.Call(m, []runtime.Value{scriptVal(data)})
				if cerr != nil {
					return nil, cerr
				}
				return jsonErrResult(r), nil
			}
			prior, _ := runtime.Deref(args[1])
			ectx := &jsonErrCtx{}
			if std := derefTyp(v.TypeOf(args[1])); std != nil && std.Kind == runtime.KindStruct && std.Name != "" {
				ectx.strct = jsonBareName(std)
			}
			sv := jsonShape(v, dec, derefTyp(v.TypeOf(args[1])), prior, ectx)
			if !runtime.SetRef(args[1], sv) {
				return nil, fmt.Errorf("json.Unmarshal: cannot assign to %T", args[1])
			}
			// Like gc, the shaped value is written even on type errors —
			// successfully-decoded fields land and the reported error is
			// the FIRST in input order, not decode order.
			if ectx.fatal != nil {
				return nil, ectx.fatal
			}
			if ectx.callErr != nil {
				return ectx.callErr, nil
			}
			return errVal(jsonInputErr(data, ectx.errs)), nil
		}},
		"Valid": h.fn("json.Valid", func(a []any) (any, error) { return json.Valid(byteSlice(a[0])), nil }, json.Valid),
	})
	// net/url needs no intrinsic: its source interprets cleanly once
	// the internal/godebug stub answers the knob lookups below.
	// internal/godebug cannot be imported outside GOROOT, so a stub
	// Setting answers the knobs stdlib sources consult: Value() reports
	// the setting's GODEBUG entry, or "" when unset — every gate
	// defaults to its enabled behavior (e.g. url's query-param limit
	// check in ParseQuery), and a t.Setenv-style GODEBUG write made
	// through internal/testenv.SetGODEBUG below is honored.
	e.Bind("internal/godebug", map[string]runtime.Value{
		"New": h.fn1("godebug.New", func(a []any) (any, error) {
			return &runtime.GoValue{V: &godebugSetting{name: str(a[0])}}, nil
		}),
		"Setting": hostType("internal/godebug.Setting", func() any { return &godebugSetting{} }),
	})
	// internal/testenv exists only for GOROOT's own tests; the one entry
	// the upstream suites reach for is SetGODEBUG. Upstream calls
	// t.Helper() + t.Setenv (which registers a cleanup restoring the
	// env) — writing the host env directly gives the same observable
	// result for a test process, and the bound godebug Setting reads
	// that same host env, so the knob actually flips. There is no
	// testing.TB to satisfy: the t argument is ignored.
	e.Bind("internal/testenv", map[string]runtime.Value{
		"SetGODEBUG": h.fn2("testenv.SetGODEBUG", func(a []any) (any, error) {
			return nil, os.Setenv("GODEBUG", os.Getenv("GODEBUG")+","+str(a[1]))
		}),
	})
	// syscall's sources need unsafe layout (route_bsd's Offsetof) in
	// init. Bind the portable surface tools reach for — signals and
	// common errnos — as host values.
	e.Bind("syscall", map[string]runtime.Value{
		"SIGINT":  &runtime.GoValue{V: syscall.SIGINT},
		"SIGQUIT": &runtime.GoValue{V: syscall.SIGQUIT},
		"SIGTERM": &runtime.GoValue{V: syscall.SIGTERM},
		"SIGKILL": &runtime.GoValue{V: syscall.SIGKILL},
		"SIGHUP":  &runtime.GoValue{V: syscall.SIGHUP},
		"ENOENT":  &runtime.GoValue{V: syscall.ENOENT},
		"EEXIST":  &runtime.GoValue{V: syscall.EEXIST},
		"EINTR":   &runtime.GoValue{V: syscall.EINTR},
		"EPIPE":   &runtime.GoValue{V: syscall.EPIPE},
		"EACCES":  &runtime.GoValue{V: syscall.EACCES},
		"ENOTDIR": &runtime.GoValue{V: syscall.ENOTDIR},
		"EISDIR":  &runtime.GoValue{V: syscall.EISDIR},
		"Getpid":  h.fn("syscall.Getpid", func(a []any) (any, error) { return int(syscall.Getpid()), nil }),
	})
	// internal/bytealg and internal/stringslite back strings/bytes in
	// GOROOT, and their sources lean on unsafe (bytealg's init reads
	// unsafe.Offsetof of cpu flags). Stub the pure entry points stdlib
	// sources call — embed.FS's lookup, for one — over strings/bytes.
	e.Bind("internal/bytealg", map[string]runtime.Value{
		"MaxLen":        int64(bytealgMaxLen()),
		"MaxBruteForce": int64(bytealgMaxBruteForce()),
		"Compare":       h.fn2("bytealg.Compare", func(a []any) (any, error) { return int(bytes.Compare(byteSlice(a[0]), byteSlice(a[1]))), nil }),
		"Count": h.fn2("bytealg.Count", func(a []any) (any, error) {
			return int(bytes.Count(byteSlice(a[0]), []byte{byte(int64Of(a[1]))})), nil
		}),
		"CountString": h.fn2("bytealg.CountString", func(a []any) (any, error) {
			return int(strings.Count(str(a[0]), string([]byte{byte(int64Of(a[1]))}))), nil
		}),
		"Equal":           h.fn2("bytealg.Equal", func(a []any) (any, error) { return bytes.Equal(byteSlice(a[0]), byteSlice(a[1])), nil }),
		"Index":           h.fn2("bytealg.Index", func(a []any) (any, error) { return int(bytes.Index(byteSlice(a[0]), byteSlice(a[1]))), nil }),
		"IndexString":     h.fn2("bytealg.IndexString", func(a []any) (any, error) { return int(strings.Index(str(a[0]), str(a[1]))), nil }),
		"IndexByte":       h.fn2("bytealg.IndexByte", func(a []any) (any, error) { return int(bytes.IndexByte(byteSlice(a[0]), byte(int64Of(a[1])))), nil }),
		"IndexByteString": h.fn2("bytealg.IndexByteString", func(a []any) (any, error) { return int(strings.IndexByte(str(a[0]), byte(int64Of(a[1])))), nil }),
		"LastIndexByte": h.fn2("bytealg.LastIndexByte", func(a []any) (any, error) {
			return int(bytes.LastIndexByte(byteSlice(a[0]), byte(int64Of(a[1])))), nil
		}),
		"LastIndexByteString": h.fn2("bytealg.LastIndexByteString", func(a []any) (any, error) { return int(strings.LastIndexByte(str(a[0]), byte(int64Of(a[1])))), nil }),
		"CompareString":       h.fn2("bytealg.CompareString", func(a []any) (any, error) { return strings.Compare(str(a[0]), str(a[1])), nil }),
		// gc's Cutover is an arch-tuned threshold: IndexByte calls it to
		// decide when too many false-positive hits warrant switching to
		// Index. Either search path returns the same offset, so a small
		// constant preserves observable behavior.
		"Cutover": h.fn1("bytealg.Cutover", func(a []any) (any, error) { return int(2), nil }),
		// gc hands the runtime a zeroed []byte of len n to fill; a script
		// []byte of zeroed elements does the same for the callers (bytes's
		// Join/Repeat/ToUpper, strings.Builder's grow).
		"MakeNoZero": h.fn1("bytealg.MakeNoZero", func(a []any) (any, error) {
			return make([]byte, int(int64Of(a[0]))), nil
		}),
	})
	e.Bind("internal/stringslite", map[string]runtime.Value{
		"HasPrefix": h.fn2("stringslite.HasPrefix", func(a []any) (any, error) { return strings.HasPrefix(str(a[0]), str(a[1])), nil }),
		"HasSuffix": h.fn2("stringslite.HasSuffix", func(a []any) (any, error) { return strings.HasSuffix(str(a[0]), str(a[1])), nil }),
		"IndexByte": h.fn2("stringslite.IndexByte", func(a []any) (any, error) { return int(strings.IndexByte(str(a[0]), byte(int64Of(a[1])))), nil }),
		"Index":     h.fn2("stringslite.Index", func(a []any) (any, error) { return int(strings.Index(str(a[0]), str(a[1]))), nil }),
		"Cut": h.fn2("stringslite.Cut", func(a []any) (any, error) {
			b, f, ok := strings.Cut(str(a[0]), str(a[1]))
			return &runtime.Tuple{Elems: []runtime.Value{b, f, ok}}, nil
		}),
		"CutPrefix": h.fn2("stringslite.CutPrefix", func(a []any) (any, error) {
			r, ok := strings.CutPrefix(str(a[0]), str(a[1]))
			return &runtime.Tuple{Elems: []runtime.Value{r, ok}}, nil
		}),
		"CutSuffix": h.fn2("stringslite.CutSuffix", func(a []any) (any, error) {
			r, ok := strings.CutSuffix(str(a[0]), str(a[1]))
			return &runtime.Tuple{Elems: []runtime.Value{r, ok}}, nil
		}),
		"TrimPrefix":      h.fn2("stringslite.TrimPrefix", func(a []any) (any, error) { return strings.TrimPrefix(str(a[0]), str(a[1])), nil }),
		"TrimSuffix":      h.fn2("stringslite.TrimSuffix", func(a []any) (any, error) { return strings.TrimSuffix(str(a[0]), str(a[1])), nil }),
		"Clone":           h.fn1("stringslite.Clone", func(a []any) (any, error) { return strings.Clone(str(a[0])), nil }),
		"IndexByteString": h.fn2("stringslite.IndexByteString", func(a []any) (any, error) { return int(strings.IndexByte(str(a[0]), byte(int64Of(a[1])))), nil }),
	})
	e.Bind("html", map[string]runtime.Value{
		"EscapeString":   h.fn("html.EscapeString", func(a []any) (any, error) { return html.EscapeString(str(a[0])), nil }, html.EscapeString),
		"UnescapeString": h.fn("html.UnescapeString", func(a []any) (any, error) { return html.UnescapeString(str(a[0])), nil }, html.UnescapeString),
	})
	e.Bind("path", map[string]runtime.Value{
		"Base":  h.fn("path.Base", func(a []any) (any, error) { return path.Base(str(a[0])), nil }, path.Base),
		"Clean": h.fn("path.Clean", func(a []any) (any, error) { return path.Clean(str(a[0])), nil }, path.Clean),
		"Dir":   h.fn("path.Dir", func(a []any) (any, error) { return path.Dir(str(a[0])), nil }, path.Dir),
		"Ext":   h.fn("path.Ext", func(a []any) (any, error) { return path.Ext(str(a[0])), nil }, path.Ext),
		"IsAbs": h.fn("path.IsAbs", func(a []any) (any, error) { return path.IsAbs(str(a[0])), nil }, path.IsAbs),
		"Join":  h.fn("path.Join", func(a []any) (any, error) { return path.Join(strArgs(a)...), nil }, path.Join),
		"Match": h.fn2("path.Match", func(a []any) (any, error) { return retErr2(path.Match(str(a[0]), str(a[1]))) }),
		"Split": h.fn("path.Split", func(a []any) (any, error) {
			d, f := path.Split(str(a[0]))
			return &runtime.Tuple{Elems: []runtime.Value{d, f}}, nil
		}),
	})
	// reflect is bound by installReflect (the minireflect facade).
	e.installReflect()
	e.Bind("sort", map[string]runtime.Value{
		"Ints":     h.sortInPlace("sort.Ints"),
		"Float64s": h.sortInPlace("sort.Float64s"),
		"Strings":  h.sortInPlace("sort.Strings"),
		// sort.Sort adapts a script object with Len/Less/Swap methods to
		// a host sort.Interface (a GoValue sort.Interface passes through).
		"Sort":  &runtime.BuiltinFunc{Name: "sort.Sort", Fn: h.sortInterface},
		"Slice": &runtime.BuiltinFunc{Name: "sort.Slice", Fn: h.sortSlice},
		"SliceIsSorted": &runtime.BuiltinFunc{Name: "sort.SliceIsSorted", SyncCallbacks: true, Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			s, ok := sliceOf(args[0])
			if !ok {
				if nilSliceArg(args[0]) {
					return true, nil // a nil slice is sorted
				}
				return nil, fmt.Errorf("sort.SliceIsSorted: first arg must be a slice")
			}
			less := args[1]
			for i := len(s.Elems) - 1; i > 0; i-- {
				r := callOrPanic(v, less, []runtime.Value{int64(i), int64(i - 1)})
				if b, _ := r.(bool); b {
					return false, nil
				}
			}
			return true, nil
		}},
		"Search": &runtime.BuiltinFunc{Name: "sort.Search", SyncCallbacks: true, Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			n := int64Of(goNative(args[0]))
			f := args[1]
			i, j := int64(0), n
			for i < j {
				m := int64(uint64(i+j) >> 1)
				r := callOrPanic(v, f, []runtime.Value{m})
				if b, _ := r.(bool); b {
					j = m
				} else {
					i = m + 1
				}
			}
			return i, nil
		}},
		"SliceStable": &runtime.BuiltinFunc{Name: "sort.SliceStable", SyncCallbacks: true, Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			s, ok := sliceOf(args[0])
			if !ok {
				if nilSliceArg(args[0]) {
					return runtime.NIL, nil // sorting a nil slice is a no-op in Go
				}
				return nil, fmt.Errorf("sort.SliceStable: first arg must be a slice")
			}
			less := args[1]
			sort.SliceStable(s.Elems, func(i, j int) bool {
				r := callOrPanic(v, less, []runtime.Value{int64(i), int64(j)})
				b, _ := r.(bool)
				return b
			})
			return runtime.NIL, nil
		}},
	})
	e.Bind("slices", map[string]runtime.Value{
		"Sort": h.sortInPlace("slices.Sort"),
		"Contains": h.fn2("slices.Contains", func(a []any) (any, error) {
			return slices.Contains(anySlice(a[0]), a[1]), nil
		}),
		"Index": h.fn2("slices.Index", func(a []any) (any, error) {
			return slices.Index(anySlice(a[0]), a[1]), nil
		}),
		"Clone": &runtime.BuiltinFunc{Name: "slices.Clone", Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("slices.Clone needs 1 arg")
			}
			switch s := args[0].(type) {
			case *runtime.TypedNil:
				// Clone(nil) is the same typed nil — a host Clone on a nil
				// []any would surface an empty non-nil slice.
				return s, nil
			case *runtime.Slice:
				// keep the declared slice type: a clone boxed as []any loses
				// `[]string` and `sort.Strings(c)` traps on the reuse.
				return &runtime.Slice{Elems: append([]runtime.Value{}, s.Elems...), Typ: s.Typ}, nil
			case *runtime.Named:
				if _, ok := s.V.(*runtime.TypedNil); ok {
					return s, nil
				}
				if sl, ok := s.V.(*runtime.Slice); ok {
					return runtime.Tag(s.Typ, &runtime.Slice{Elems: append([]runtime.Value{}, sl.Elems...), Typ: sl.Typ}), nil
				}
			}
			return nil, fmt.Errorf("slices.Clone: arg must be a slice")
		}},
		"Concat": h.fn("slices.Concat", func(a []any) (any, error) {
			var parts [][]any
			for _, p := range a {
				parts = append(parts, anySlice(p))
			}
			return slices.Concat(parts...), nil
		}),
		"Equal": h.fn2("slices.Equal", func(a []any) (any, error) {
			return slices.Equal(anySlice(a[0]), anySlice(a[1])), nil
		}),
		"IsSorted": h.fn("slices.IsSorted", func(a []any) (any, error) {
			el := scriptElems(a[0])
			return sort.SliceIsSorted(el, func(i, j int) bool { return lessScript(el[i], el[j]) }), nil
		}),
		"Sorted": &runtime.BuiltinFunc{Name: "slices.Sorted", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("slices.Sorted needs 1 arg")
			}
			el, err := seqElems(vc, args[0])
			if err != nil {
				return nil, fmt.Errorf("slices.Sorted: %w", err)
			}
			sort.Slice(el, func(i, j int) bool { return lessScript(el[i], el[j]) })
			return &runtime.Slice{Elems: el, Typ: sliceTypOf(args[0])}, nil
		}},
		"DeleteFunc": &runtime.BuiltinFunc{Name: "slices.DeleteFunc", SyncCallbacks: true, Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("slices.DeleteFunc needs 2 args")
			}
			s, ok := sliceOf(args[0])
			if !ok {
				if nilish(args[0]) {
					return args[0], nil // deleting from nil yields nil
				}
				return nil, fmt.Errorf("slices.DeleteFunc: arg must be a slice")
			}
			var out []runtime.Value
			for _, el := range s.Elems {
				r, err := vc.Call(args[1], []runtime.Value{el})
				if err != nil {
					return nil, err
				}
				if drop, _ := r.(bool); !drop {
					out = append(out, el)
				}
			}
			return h.packSlice(s, out), nil
		}},
		"Compact": &runtime.BuiltinFunc{Name: "slices.Compact", Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("slices.Compact needs 1 arg")
			}
			s, ok := sliceOf(args[0])
			if !ok {
				if nilish(args[0]) {
					return args[0], nil
				}
				return nil, fmt.Errorf("slices.Compact: arg must be a slice")
			}
			var out []runtime.Value
			for i, el := range s.Elems {
				if i > 0 && atomicCellEq(out[len(out)-1], el) {
					continue
				}
				out = append(out, el)
			}
			return h.packSlice(s, out), nil
		}},
		"Repeat": &runtime.BuiltinFunc{Name: "slices.Repeat", Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("slices.Repeat needs 2 args")
			}
			s, ok := sliceOf(args[0])
			if !ok {
				return nil, fmt.Errorf("slices.Repeat: arg must be a slice")
			}
			n := int64Of(args[1])
			if n < 0 {
				return nil, fmt.Errorf("slices.Repeat: negative count %d", n)
			}
			var out []runtime.Value
			for i := int64(0); i < n; i++ {
				out = append(out, s.Elems...)
			}
			return &runtime.Slice{Elems: out, Typ: s.Typ}, nil
		}},
		"SortFunc":       h.sortByCmpFunc("slices.SortFunc"),
		"SortStableFunc": h.sortByCmpFunc("slices.SortStableFunc"),
		"BinarySearch": &runtime.BuiltinFunc{Name: "slices.BinarySearch", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			s, ok := args[0].(*runtime.Slice)
			if !ok {
				return nil, fmt.Errorf("slices.BinarySearch: first arg must be a slice")
			}
			target := args[1]
			i := sort.Search(len(s.Elems), func(i int) bool { return !lessScript(s.Elems[i], target) })
			found := i < len(s.Elems) && equalScript(s.Elems[i], target)
			return &runtime.Tuple{Elems: []runtime.Value{int64(i), found}}, nil
		}},
		"BinarySearchFunc": &runtime.BuiltinFunc{Name: "slices.BinarySearchFunc", SyncCallbacks: true, Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			s, ok := args[0].(*runtime.Slice)
			if !ok {
				return nil, fmt.Errorf("slices.BinarySearchFunc: first arg must be a slice")
			}
			target := args[1]
			cf := args[2]
			cmpAt := func(i int) int64 {
				r := callOrPanic(v, cf, []runtime.Value{s.Elems[i], target})
				n, _ := runtime.Unwrap(r).(int64)
				return n
			}
			i, j := 0, len(s.Elems)
			for i < j {
				m := int(uint(i+j) >> 1)
				if cmpAt(m) < 0 {
					i = m + 1
				} else {
					j = m
				}
			}
			found := false
			if i < len(s.Elems) {
				found = cmpAt(i) == 0
			}
			return &runtime.Tuple{Elems: []runtime.Value{int64(i), found}}, nil
		}},
		"EqualFunc": &runtime.BuiltinFunc{Name: "slices.EqualFunc", SyncCallbacks: true, Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			a, _ := args[0].(*runtime.Slice)
			b, _ := args[1].(*runtime.Slice)
			eq := args[2]
			if a == nil || b == nil {
				return nil, fmt.Errorf("slices.EqualFunc: first two args must be slices")
			}
			if len(a.Elems) != len(b.Elems) {
				return false, nil
			}
			for i := range a.Elems {
				r := callOrPanic(v, eq, []runtime.Value{a.Elems[i], b.Elems[i]})
				if ok, _ := r.(bool); !ok {
					return false, nil
				}
			}
			return true, nil
		}},
		"IndexFunc": &runtime.BuiltinFunc{Name: "slices.IndexFunc", SyncCallbacks: true, Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			s, ok := args[0].(*runtime.Slice)
			if !ok {
				return nil, fmt.Errorf("slices.IndexFunc: first arg must be a slice")
			}
			for i, el := range s.Elems {
				r := callOrPanic(v, args[1], []runtime.Value{el})
				if ok, _ := r.(bool); ok {
					return int64(i), nil
				}
			}
			return int64(-1), nil
		}},
		"Max": h.fn("slices.Max", func(a []any) (any, error) {
			el := scriptElems(a[0])
			if len(el) == 0 {
				return nil, fmt.Errorf("slices.Max: empty slice")
			}
			best := el[0]
			for _, x := range el[1:] {
				if lessScript(best, x) {
					best = x
				}
			}
			return best, nil
		}),
		"Min": h.fn("slices.Min", func(a []any) (any, error) {
			el := scriptElems(a[0])
			if len(el) == 0 {
				return nil, fmt.Errorf("slices.Min: empty slice")
			}
			best := el[0]
			for _, x := range el[1:] {
				if lessScript(x, best) {
					best = x
				}
			}
			return best, nil
		}),
		"Reverse": &runtime.BuiltinFunc{Name: "slices.Reverse", Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			s, ok := args[0].(*runtime.Slice)
			if !ok {
				return nil, fmt.Errorf("slices.Reverse: arg must be a slice")
			}
			slices.Reverse(s.Elems)
			return runtime.NIL, nil
		}},
		"Insert": &runtime.BuiltinFunc{Name: "slices.Insert", Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			s, ok := args[0].(*runtime.Slice)
			if !ok || len(args) < 2 {
				return nil, fmt.Errorf("slices.Insert(slice, i, elems...)")
			}
			i, _ := runtime.Unwrap(args[1]).(int64)
			el := slices.Insert(s.Elems, int(i), args[2:]...)
			return &runtime.Slice{Elems: el, Typ: s.Typ}, nil
		}},
		"Delete": &runtime.BuiltinFunc{Name: "slices.Delete", Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			s, ok := args[0].(*runtime.Slice)
			if !ok || len(args) != 3 {
				return nil, fmt.Errorf("slices.Delete(slice, i, j)")
			}
			i, _ := runtime.Unwrap(args[1]).(int64)
			j, _ := runtime.Unwrap(args[2]).(int64)
			el := slices.Delete(s.Elems, int(i), int(j))
			return &runtime.Slice{Elems: el, Typ: s.Typ}, nil
		}},
	})
	e.Bind("maps", map[string]runtime.Value{
		"Keys":   h.fn("maps.Keys", func(a []any) (any, error) { return mapKeys(a[0]), nil }),
		"Values": h.fn("maps.Values", func(a []any) (any, error) { return mapValues(a[0]), nil }),
		"Clone": &runtime.BuiltinFunc{Name: "maps.Clone", Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("maps.Clone needs 1 arg")
			}
			clone := func(m *runtime.Map) *runtime.Map {
				pairs := make(map[runtime.Value]runtime.Value, len(m.Pairs))
				for k, v := range m.Pairs {
					pairs[k] = v
				}
				// keep the declared map type: without Typ a missing key
				// reads NIL and `c[k]++` traps instead of zero-starting.
				return &runtime.Map{Pairs: pairs, Order: append([]runtime.Value{}, m.Order...), Keys: append([]runtime.Value{}, m.Keys...), Typ: m.Typ}
			}
			switch m := args[0].(type) {
			case *runtime.TypedNil:
				// Clone(nil) is the same typed nil map.
				return m, nil
			case *runtime.Map:
				return clone(m), nil
			case *runtime.Named:
				if _, ok := m.V.(*runtime.TypedNil); ok {
					return m, nil
				}
				if mm, ok := m.V.(*runtime.Map); ok {
					return runtime.Tag(m.Typ, clone(mm)), nil
				}
			}
			return nil, fmt.Errorf("maps.Clone: arg must be a map")
		}},
		"Copy": &runtime.BuiltinFunc{Name: "maps.Copy", Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			dst, _ := args[0].(*runtime.Map)
			src, _ := args[1].(*runtime.Map)
			if dst == nil || src == nil {
				return nil, fmt.Errorf("maps.Copy: args must be maps")
			}
			for i := 0; i < src.Len(); i++ {
				k, e := src.At(i)
				dst.Insert(k, e)
			}
			return runtime.NIL, nil
		}},
		"Equal": &runtime.BuiltinFunc{Name: "maps.Equal", Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			a, _ := args[0].(*runtime.Map)
			b, _ := args[1].(*runtime.Map)
			if a == nil || b == nil {
				return args[0] == args[1], nil // nil == nil
			}
			if len(a.Pairs) != len(b.Pairs) {
				return false, nil
			}
			for k, av := range a.Pairs {
				bv, ok := b.LookupCanonical(k)
				if !ok || !equalScript(av, bv) {
					return false, nil
				}
			}
			return true, nil
		}},
	})
	// os: an interpreted program must never observe or terminate the host
	// process — Exit unwinds out as a process exit; the environment/argv surface is only
	// bound when the engine is unrestricted (no AllowedRoots). File-system
	// operations are always bound: each path argument resolves through
	// e.fsPath, which anchors relative paths at the engine's virtual cwd
	// and enforces AllowedRoots per call — that is the restricted-mode
	// file policy (host-surface gating stays per-symbol via WithHostPolicy).
	ospkg := map[string]runtime.Value{
		"Exit": h.fn("os.Exit", func(a []any) (any, error) {
			// os.Exit unwinds past every defer straight out of the
			// interpreter — ExitRequest ends the run at the Call
			// boundary: 0 cleanly, N as `exit status N`.
			panic(&vm.ExitRequest{Code: int(intOf(a[0]))})
		}),
		"Stat":     h.fn1("os.Stat", func(a []any) (any, error) { return fsOp2(e, "os.Stat", a, os.Stat) }),
		"Lstat":    h.fn1("os.Lstat", func(a []any) (any, error) { return fsOp2(e, "os.Lstat", a, os.Lstat) }),
		"ReadFile": h.fn1("os.ReadFile", func(a []any) (any, error) { return fsOp2(e, "os.ReadFile", a, os.ReadFile) }),
		"WriteFile": h.fn3("os.WriteFile", func(a []any) (any, error) {
			p, err := e.fsPath(str(a[0]))
			if err != nil {
				return nil, err
			}
			return errVal(os.WriteFile(p, byteSlice(a[1]), fs.FileMode(intOf(a[2])))), nil
		}),
		"Mkdir": h.fn2("os.Mkdir", func(a []any) (any, error) {
			p, err := e.fsPath(str(a[0]))
			if err != nil {
				return nil, err
			}
			return errVal(os.Mkdir(p, fs.FileMode(intOf(a[1])))), nil
		}),
		"MkdirAll": h.fn2("os.MkdirAll", func(a []any) (any, error) {
			p, err := e.fsPath(str(a[0]))
			if err != nil {
				return nil, err
			}
			return errVal(os.MkdirAll(p, fs.FileMode(intOf(a[1])))), nil
		}),
		"Remove":    h.fn1("os.Remove", func(a []any) (any, error) { return fsErrOp(e, "os.Remove", a, os.Remove) }),
		"RemoveAll": h.fn1("os.RemoveAll", func(a []any) (any, error) { return fsErrOp(e, "os.RemoveAll", a, os.RemoveAll) }),
		"Truncate": h.fn2("os.Truncate", func(a []any) (any, error) {
			p, err := e.fsPath(str(a[0]))
			if err != nil {
				return nil, err
			}
			return errVal(os.Truncate(p, int64Of(a[1]))), nil
		}),
		"Rename": h.fn2("os.Rename", func(a []any) (any, error) {
			old, err := e.fsPath(str(a[0]))
			if err != nil {
				return nil, err
			}
			newp, err := e.fsPath(str(a[1]))
			if err != nil {
				return nil, err
			}
			return errVal(os.Rename(old, newp)), nil
		}),
		"ReadDir": h.fn1("os.ReadDir", func(a []any) (any, error) {
			p, err := e.fsPath(str(a[0]))
			if err != nil {
				return nil, err
			}
			entries, err := os.ReadDir(p)
			el := make([]runtime.Value, len(entries))
			for i, en := range entries {
				el[i] = &runtime.GoValue{V: en}
			}
			return &runtime.Tuple{Elems: []runtime.Value{&runtime.Slice{Elems: el}, errVal(err)}}, nil
		}),
		// Getwd/Chdir operate on the engine's virtual cwd (see WithWorkingDir):
		// the host process cwd is never touched, so scripts can "cd" freely
		// without side effects on the embedding tool.
		"Getwd": h.fn("os.Getwd", func(a []any) (any, error) { return retErr2(e.cwd, nil) }),
		"Chdir": h.fn1("os.Chdir", func(a []any) (any, error) {
			p, err := e.fsPath(str(a[0]))
			if err != nil {
				return nil, err
			}
			st, err := os.Stat(p)
			if err != nil {
				return errVal(err), nil
			}
			if !st.IsDir() {
				return errVal(fmt.Errorf("chdir %s: not a directory", p)), nil
			}
			e.cwd = p
			return runtime.NIL, nil
		}),
		"Open":   h.fn1("os.Open", func(a []any) (any, error) { return fsOp2(e, "os.Open", a, os.Open) }),
		"Create": h.fn1("os.Create", func(a []any) (any, error) { return fsOp2(e, "os.Create", a, os.Create) }),
		"OpenFile": h.fn3("os.OpenFile", func(a []any) (any, error) {
			p, err := e.fsPath(str(a[0]))
			if err != nil {
				return nil, err
			}
			return retErr2(os.OpenFile(p, intOf(a[1]), fs.FileMode(intOf(a[2]))))
		}),
		"MkdirTemp": h.fn2("os.MkdirTemp", func(a []any) (any, error) {
			dir, err := e.fsTempDir(str(a[0]))
			if err != nil {
				return nil, err
			}
			return retErr2(os.MkdirTemp(dir, str(a[1])))
		}),
		"CreateTemp": h.fn2("os.CreateTemp", func(a []any) (any, error) {
			dir, err := e.fsTempDir(str(a[0]))
			if err != nil {
				return nil, err
			}
			return retErr2(os.CreateTemp(dir, str(a[1])))
		}),
		// DirFS resolves the dir through the same cwd+roots check as the
		// file ops; the returned host fs.DirFS keeps its Open rooted
		// there (os.DirFS itself never escapes — reads go through the
		// rooted handle, like Open).
		"DirFS": h.fn1("os.DirFS", func(a []any) (any, error) {
			p, err := e.fsPath(str(a[0]))
			if err != nil {
				return nil, err
			}
			return &runtime.GoValue{V: os.DirFS(p)}, nil
		}),
		"IsNotExist":   h.fn1("os.IsNotExist", func(a []any) (any, error) { return os.IsNotExist(asErr(a[0])), nil }, os.IsNotExist),
		"IsExist":      h.fn1("os.IsExist", func(a []any) (any, error) { return os.IsExist(asErr(a[0])), nil }, os.IsExist),
		"IsPermission": h.fn1("os.IsPermission", func(a []any) (any, error) { return os.IsPermission(asErr(a[0])), nil }, os.IsPermission),
		"IsTimeout":    h.fn1("os.IsTimeout", func(a []any) (any, error) { return os.IsTimeout(asErr(a[0])), nil }, os.IsTimeout),
		// error sentinels for errors.Is on the script side
		"ErrNotExist": &runtime.GoValue{V: fs.ErrNotExist},
		// *os.File asserts (x/tools' gocommand checks cmd.Stdout's type)
		"File":           hostType("os.File", func() any { return &os.File{} }),
		"Interrupt":      &runtime.GoValue{V: os.Interrupt},
		"Kill":           &runtime.GoValue{V: os.Kill},
		"ErrProcessDone": &runtime.GoValue{V: os.ErrProcessDone},
		"ErrExist":       &runtime.GoValue{V: fs.ErrExist},
		"ErrPermission":  &runtime.GoValue{V: fs.ErrPermission},
		"ErrClosed":      &runtime.GoValue{V: fs.ErrClosed},
		"ErrInvalid":     &runtime.GoValue{V: fs.ErrInvalid},
		"ErrNoDeadline":  &runtime.GoValue{V: os.ErrNoDeadline},
		// consts
		"PathSeparator":     &runtime.UConst{V: constant.MakeInt64(os.PathSeparator), Rune: true},
		"PathListSeparator": &runtime.UConst{V: constant.MakeInt64(os.PathListSeparator), Rune: true},
		"DevNull":           os.DevNull,
		"O_RDONLY":          int64(os.O_RDONLY),
		"O_WRONLY":          int64(os.O_WRONLY),
		"O_RDWR":            int64(os.O_RDWR),
		"O_APPEND":          int64(os.O_APPEND),
		"O_CREATE":          int64(os.O_CREATE),
		"O_EXCL":            int64(os.O_EXCL),
		"O_SYNC":            int64(os.O_SYNC),
		"O_TRUNC":           int64(os.O_TRUNC),
		"ModeDir":           &runtime.GoValue{V: fs.ModeDir},
		"ModeAppend":        &runtime.GoValue{V: fs.ModeAppend},
		"ModeExclusive":     &runtime.GoValue{V: fs.ModeExclusive},
		"ModeTemporary":     &runtime.GoValue{V: fs.ModeTemporary},
		"ModeSymlink":       &runtime.GoValue{V: fs.ModeSymlink},
		"ModeNamedPipe":     &runtime.GoValue{V: fs.ModeNamedPipe},
		"ModeSocket":        &runtime.GoValue{V: fs.ModeSocket},
		"ModeSetuid":        &runtime.GoValue{V: fs.ModeSetuid},
		"ModeSetgid":        &runtime.GoValue{V: fs.ModeSetgid},
		"ModeCharDevice":    &runtime.GoValue{V: fs.ModeCharDevice},
		"ModeSticky":        &runtime.GoValue{V: fs.ModeSticky},
		"ModeIrregular":     &runtime.GoValue{V: fs.ModeIrregular},
		"ModePerm":          &runtime.GoValue{V: fs.ModePerm},
		"ModeType":          &runtime.GoValue{V: fs.ModeType},
		"SeekStart":         &runtime.UConst{V: constant.MakeInt64(io.SeekStart)},
		"SeekCurrent":       &runtime.UConst{V: constant.MakeInt64(io.SeekCurrent)},
		"SeekEnd":           &runtime.UConst{V: constant.MakeInt64(io.SeekEnd)},
		// marker interface typedefs so `os.DirEntry`/`os.FileInfo` resolve
		// in callback signatures (filepath.WalkDir's funclit); member
		// access on the host values behind them dispatches by reflection.
		"DirEntry": &runtime.TypeDef{Name: "DirEntry", Kind: runtime.KindInterface},
		"FileInfo": &runtime.TypeDef{Name: "FileInfo", Kind: runtime.KindInterface},
	}
	if len(e.cfg.AllowedRoots) == 0 {
		ospkg["Getenv"] = h.fn("os.Getenv", func(a []any) (any, error) { return os.Getenv(str(a[0])), nil })
		ospkg["Setenv"] = h.fn2("os.Setenv", func(a []any) (any, error) { return errVal(os.Setenv(str(a[0]), str(a[1]))), nil })
		ospkg["Unsetenv"] = h.fn1("os.Unsetenv", func(a []any) (any, error) { return errVal(os.Unsetenv(str(a[0]))), nil })
		ospkg["Clearenv"] = h.fn("os.Clearenv", func(a []any) (any, error) { os.Clearenv(); return nil, nil })
		ospkg["Environ"] = h.fn("os.Environ", func(a []any) (any, error) { return strsSlice(os.Environ()), nil })
		// os.Args is the script-visible argv (WithArgs; else the host
		// process argv), bound as a VARIABLE like Go's — flag's package
		// init reads it via len(os.Args).
		argv := os.Args
		if e.args != nil {
			argv = e.args
		}
		ospkg["Args"] = strsSlice(argv)
		ospkg["Hostname"] = h.fn("os.Hostname", func(a []any) (any, error) { return retErr2(os.Hostname()) })
		// process stdio, boxed for cmd.Stdout / cmd.Stderr wiring.
		// os.Stdout is a *os.File facade whose writes forward to the
		// engine's configured output so host consumers handed it —
		// text/template's Execute above all — write where print/println
		// write instead of escaping to the process stdout. (Stdin/Stderr
		// keep the real files: there is no engine-level abstraction for
		// them.)
		ospkg["Stdin"] = &runtime.GoValue{V: os.Stdin}
		ospkg["Stdout"] = &runtime.GoValue{V: &engineStdout{File: os.Stdout, h: h}}
		ospkg["Stderr"] = &runtime.GoValue{V: os.Stderr}
		ospkg["Pipe"] = h.fn("os.Pipe", func(a []any) (any, error) {
			r, w, err := os.Pipe()
			return &runtime.Tuple{Elems: []runtime.Value{&runtime.GoValue{V: r}, &runtime.GoValue{V: w}, errVal(err)}}, nil
		})
		ospkg["TempDir"] = h.fn("os.TempDir", func(a []any) (any, error) { return os.TempDir(), nil })
		ospkg["UserHomeDir"] = h.fn("os.UserHomeDir", func(a []any) (any, error) { return retErr2(os.UserHomeDir()) })
		ospkg["UserCacheDir"] = h.fn("os.UserCacheDir", func(a []any) (any, error) { return retErr2(os.UserCacheDir()) })
		ospkg["UserConfigDir"] = h.fn("os.UserConfigDir", func(a []any) (any, error) { return retErr2(os.UserConfigDir()) })
	}
	e.Bind("os", ospkg)
	// path/filepath: pure path math is always available; operations that
	// touch the filesystem (Glob, WalkDir, EvalSymlinks) go through
	// e.fsPath like the os.* equivalents.
	e.Bind("path/filepath", map[string]runtime.Value{
		"Join":          h.fn("filepath.Join", func(a []any) (any, error) { return filepath.Join(strSlice(a)...), nil }, filepath.Join),
		"Base":          h.fn1("filepath.Base", func(a []any) (any, error) { return filepath.Base(str(a[0])), nil }, filepath.Base),
		"Dir":           h.fn1("filepath.Dir", func(a []any) (any, error) { return filepath.Dir(str(a[0])), nil }, filepath.Dir),
		"Ext":           h.fn1("filepath.Ext", func(a []any) (any, error) { return filepath.Ext(str(a[0])), nil }, filepath.Ext),
		"Clean":         h.fn1("filepath.Clean", func(a []any) (any, error) { return filepath.Clean(str(a[0])), nil }, filepath.Clean),
		"VolumeName":    h.fn1("filepath.VolumeName", func(a []any) (any, error) { return filepath.VolumeName(str(a[0])), nil }, filepath.VolumeName),
		"IsAbs":         h.fn1("filepath.IsAbs", func(a []any) (any, error) { return filepath.IsAbs(str(a[0])), nil }, filepath.IsAbs),
		"ToSlash":       h.fn1("filepath.ToSlash", func(a []any) (any, error) { return filepath.ToSlash(str(a[0])), nil }, filepath.ToSlash),
		"FromSlash":     h.fn1("filepath.FromSlash", func(a []any) (any, error) { return filepath.FromSlash(str(a[0])), nil }, filepath.FromSlash),
		"SplitList":     h.fn1("filepath.SplitList", func(a []any) (any, error) { return filepath.SplitList(str(a[0])), nil }, filepath.SplitList),
		"Match":         h.fn2("filepath.Match", func(a []any) (any, error) { return retErr2(filepath.Match(str(a[0]), str(a[1]))) }),
		"Separator":     &runtime.UConst{V: constant.MakeInt64(os.PathSeparator), Rune: true},
		"ListSeparator": &runtime.UConst{V: constant.MakeInt64(os.PathListSeparator), Rune: true},
		"Split": h.fn1("filepath.Split", func(a []any) (any, error) {
			d, f := filepath.Split(str(a[0]))
			return &runtime.Tuple{Elems: []runtime.Value{d, f}}, nil
		}),
		// Abs/Rel anchor relative paths at the engine's virtual cwd, not the
		// host process's (divergence from real filepath.Abs is deliberate).
		"Abs": h.fn1("filepath.Abs", func(a []any) (any, error) { return retErr2(e.cwdAbs(str(a[0])), nil) }),
		"Rel": h.fn2("filepath.Rel", func(a []any) (any, error) {
			return retErr2(filepath.Rel(e.cwdAbs(str(a[0])), e.cwdAbs(str(a[1]))))
		}),
		"EvalSymlinks": h.fn1("filepath.EvalSymlinks", func(a []any) (any, error) {
			p, err := e.fsPath(str(a[0]))
			if err != nil {
				return nil, err
			}
			return retErr2(filepath.EvalSymlinks(p))
		}),
		"Glob": h.fn1("filepath.Glob", func(a []any) (any, error) {
			pat := str(a[0])
			ap, err := e.fsPath(pat)
			if err != nil {
				return nil, err
			}
			m, err := filepath.Glob(ap)
			if err != nil {
				return retErr2([]string(nil), err)
			}
			// A pattern inside the roots can still expand through an in-root
			// symlink into files outside them; matches are re-checked and an
			// escape comes back as the call's error value (Go's shape), not
			// a trap — the script gets nil matches either way.
			for _, p := range m {
				if err := e.cfg.CheckPath(p); err != nil {
					return retErr2([]string(nil), err)
				}
			}
			if !filepath.IsAbs(pat) {
				// Go returns matches in the shape of the pattern: keep
				// relative patterns relative to the virtual cwd.
				for i, p := range m {
					if rel, rerr := filepath.Rel(e.cwd, p); rerr == nil {
						m[i] = rel
					}
				}
			}
			return retErr2(m, nil)
		}),
		"WalkDir": &runtime.BuiltinFunc{Name: "filepath.WalkDir", SyncCallbacks: true, Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("filepath.WalkDir needs 2 args, got %d", len(args))
			}
			root := str(goNative(args[0]))
			cb := args[1]
			ap, err := e.fsPath(root)
			if err != nil {
				return nil, err
			}
			relIn := !filepath.IsAbs(root)
			werr := filepath.WalkDir(ap, func(p string, d fs.DirEntry, werr error) error {
				sp := p
				if relIn {
					if rel, rerr := filepath.Rel(e.cwd, p); rerr == nil {
						sp = rel
					}
				}
				r := callOrPanic(v, cb, []runtime.Value{sp, &runtime.GoValue{V: d}, errVal(werr)})
				return asErr(goNative(r))
			})
			return errVal(werr), nil
		}},
		"SkipDir": &runtime.GoValue{V: filepath.SkipDir},
		"SkipAll": &runtime.GoValue{V: filepath.SkipAll},
	})
	// os/exec: spawning a subprocess escapes per-path confinement, so the
	// package is only bound for unrestricted engines. Commands default to
	// the engine's virtual cwd via cmd.Dir; scripts wire stdio through the
	// boxed os.Stdin/Stdout/Stderr handles.
	if len(e.cfg.AllowedRoots) == 0 {
		e.Bind("os/exec", map[string]runtime.Value{
			"Command": h.fn("exec.Command", func(a []any) (any, error) {
				if len(a) == 0 {
					return nil, errors.New("exec.Command needs a name")
				}
				cmd := exec.Command(str(a[0]), strSlice(a[1:])...)
				cmd.Dir = e.cwd
				return &runtime.GoValue{V: cmd}, nil
			}),
			"LookPath": h.fn1("exec.LookPath", func(a []any) (any, error) {
				name := str(a[0])
				// A separator-bearing relative name is checked against the
				// caller's cwd — here the engine's virtual one — but Go returns
				// the name in the shape it was given, so don't absolutize the
				// result.
				if strings.ContainsRune(name, '/') && !filepath.IsAbs(name) {
					if _, err := exec.LookPath(e.cwdAbs(name)); err != nil {
						return retErr2("", err)
					}
					return retErr2(name, nil)
				}
				return retErr2(exec.LookPath(name))
			}),
			"ErrNotFound": &runtime.GoValue{V: exec.ErrNotFound},
			"ErrDot":      &runtime.GoValue{V: exec.ErrDot},
		})
	}
	// host: the gopls-friendly stub-package surface (plan §11). Scripts may
	// spell the import either canonically ("minigo.dev/host") or via the
	// in-repo stub package ("github.com/podhmo/minigo/host",
	// which ships panic("minigo intrinsic") bodies) — both paths resolve
	// to this intrinsic table, so stub bodies never execute.
	hostpkg := map[string]runtime.Value{
		"Exit": h.fn("host.Exit", func(a []any) (any, error) {
			return nil, errors.New("host.Exit is not supported: an interpreted program cannot terminate the host process")
		}),
	}
	if len(e.cfg.AllowedRoots) == 0 {
		hostpkg["Getenv"] = h.fn("host.Getenv", func(a []any) (any, error) { return os.Getenv(str(a[0])), nil })
		hostpkg["Environ"] = h.fn("host.Environ", func(a []any) (any, error) { return strsSlice(os.Environ()), nil })
		hostpkg["Args"] = h.fn("host.Args", func(a []any) (any, error) { return strsSlice(os.Args), nil })
		hostpkg["Hostname"] = h.fn("host.Hostname", func(a []any) (any, error) {
			return retErr2(os.Hostname())
		})
		hostpkg["Getwd"] = h.fn("host.Getwd", func(a []any) (any, error) {
			return retErr2(os.Getwd())
		})
	}
	for _, path := range []string{"minigo.dev/host", "github.com/podhmo/minigo/host"} {
		e.Bind(path, hostpkg)
	}
	// inspect: the package/symbol introspection surface
	// (docs/sketch/plan-package-introspection.md).
	e.installInspect()
	// unsafe/runtime: the fixed runtime primitives source cannot reach
	// (plan §11). Sizes are 64-bit host approximations over the boxed
	// representation; NumGoroutine is pinned to 1 — the VM is
	// single-threaded by design.
	// unsafe.StringData interns each string's byte backing so the same
	// string value resolves to the same element pointer — pointer
	// identity for a shared backing is the only guarantee unsafe offers
	// here, and it is what callers like x/tools' stringptr rely on.
	strDataBacks := map[string]*runtime.Slice{}
	e.Bind("unsafe", map[string]runtime.Value{
		"Sizeof": &runtime.BuiltinFunc{Name: "unsafe.Sizeof", Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			return e.unsafeSizeOf(args[0]), nil
		}},
		"Alignof": &runtime.BuiltinFunc{Name: "unsafe.Alignof", Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			return e.unsafeAlignOf(args[0]), nil
		}},
		"Offsetof": &runtime.BuiltinFunc{Name: "unsafe.Offsetof", Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			// The compiler rewrites unsafe.Offsetof(s.f) to pass the
			// base and the field name — the selector's value alone
			// cannot name its field.
			if len(args) != 2 {
				return nil, errors.New("unsafe.Offsetof requires a field selector operand")
			}
			name, ok := unsafeStringArg(args[1])
			if !ok {
				return nil, errors.New("unsafe.Offsetof requires a field selector operand")
			}
			off, err := e.unsafeFieldOffset(args[0], name)
			if err != nil {
				return nil, err
			}
			return int64(off), nil
		}},
		// Element pointers are &s[i] refs over a script slice, so the
		// StringData/String and SliceData/Slice round trips used for
		// zero-copy string packing (x/tools' event labels) hold.
		"StringData": &runtime.BuiltinFunc{Name: "unsafe.StringData", Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			str, _ := runtime.Unwrap(args[0]).(string)
			if str == "" {
				// "" still yields a (zero-length) element ref: Go leaves its
				// result unspecified, and a nil would not convert to a
				// pointer type over unsafe.Pointer (label's stringptr).
				str = "\x00"
			}
			if strDataBacks[str] == nil {
				strDataBacks[str] = unsafeBytes(str)
			}
			return &runtime.IndexRef{Base: strDataBacks[str], Key: int64(0)}, nil
		}},
		"SliceData": &runtime.BuiltinFunc{Name: "unsafe.SliceData", Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			sl, ok := runtime.Unwrap(args[0]).(*runtime.Slice)
			if !ok || sl.Len() == 0 && cap(sl.Elems) == 0 {
				return runtime.NIL, nil
			}
			return &runtime.IndexRef{Base: sl, Key: int64(0)}, nil
		}},
		"String": &runtime.BuiltinFunc{Name: "unsafe.String", Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			elems, err := unsafeElems(args[0], args[1])
			if err != nil {
				return nil, err
			}
			return string(byteSlice(&runtime.Slice{Elems: elems})), nil
		}},
		"Slice": &runtime.BuiltinFunc{Name: "unsafe.Slice", Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			n := int(int64Of(goNative(args[1])))
			// a nil pointer with len 0 is legal and reports the nil slice
			// (Go panics only when len != 0) — typed nils keep their
			// element type so the result is still a []T nil.
			if tn, ok := runtime.Unwrap(args[0]).(*runtime.TypedNil); ok && tn.Typ != nil && tn.Typ.Kind == runtime.KindPointer {
				if n == 0 {
					return &runtime.TypedNil{Typ: sliceTypFromPtr(tn.Typ)}, nil
				}
				return nil, errors.New("unsafe: ptr is nil and len is not zero")
			}
			elems, err := unsafeElems(args[0], args[1])
			if err != nil {
				return nil, err
			}
			var typ *runtime.TypeDef
			if ref, ok := runtime.Unwrap(args[0]).(*runtime.IndexRef); ok {
				if sl := ref.Slice(); sl != nil {
					typ = sliceTypFromPtr(sl.Typ)
				}
			}
			return &runtime.Slice{Elems: elems, Typ: typ}, nil
		}},
	})
	e.Bind("runtime", map[string]runtime.Value{
		"GOOS":   goruntime.GOOS,
		"GOARCH": goruntime.GOARCH,
		"NumGoroutine": h.fn("runtime.NumGoroutine", func(a []any) (any, error) {
			return goruntime.NumGoroutine(), nil
		}),
		"Gosched": h.fn("runtime.Gosched", func(a []any) (any, error) {
			goruntime.Gosched()
			return nil, nil
		}),
		"NumCPU": h.fn("runtime.NumCPU", func(a []any) (any, error) { return goruntime.NumCPU(), nil }),
		"GOMAXPROCS": &runtime.BuiltinFunc{Name: "runtime.GOMAXPROCS", Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			// read-only on the script side: GOMAXPROCS(0) reports the
			// current setting without mutating the host process's
			// parallelism.
			return goruntime.GOMAXPROCS(0), nil
		}},
		"Version":  h.fn("runtime.Version", func(a []any) (any, error) { return goruntime.Version(), nil }),
		"GC":       h.fn("runtime.GC", func(a []any) (any, error) { return nil, nil }),
		"Compiler": "gc",
		// runtime.GOROOT is deprecated for the host; report the env's
		// root, falling back to `go env GOROOT` like go/build does.
		"GOROOT": h.fn("runtime.GOROOT", func(a []any) (any, error) {
			if gr := os.Getenv("GOROOT"); gr != "" {
				return gr, nil
			}
			out, err := exec.Command("go", "env", "GOROOT").Output()
			if err != nil {
				return "", err
			}
			return strings.TrimSpace(string(out)), nil
		}),
		// a debugger trap is a no-op for the interpreter — the program
		// just continues, which is what these tests rely on.
		"Breakpoint": h.fn("runtime.Breakpoint", func(a []any) (any, error) { return nil, nil }),
		// no GC: registering a finalizer is accepted but never fires —
		// same observable behavior as a run with no GC pressure.
		"SetFinalizer": h.fn("runtime.SetFinalizer", func(a []any) (any, error) {
			if len(a) != 2 {
				return nil, errors.New("runtime.SetFinalizer needs 2 args")
			}
			return nil, nil
		}),
		"MemProfileRate": &runtime.Cell{Elem: int64(512 * 1024)},
		// liveness hints and heap profiling have no GC to act on —
		// KeepAlive is a no-op; MemProfile reports no records.
		"KeepAlive": h.fn("runtime.KeepAlive", func(a []any) (any, error) { return nil, nil }),
		"MemProfile": &runtime.BuiltinFunc{Name: "runtime.MemProfile", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			return &runtime.Tuple{Elems: []runtime.Value{int64(0), true}}, nil
		}},
		"MemProfileRecord": &runtime.TypeDef{
			Name: "runtime.MemProfileRecord", Kind: runtime.KindStruct,
			Fields: []string{"AllocBytes", "FreeBytes", "AllocObjects", "FreeObjects"},
		},
		"Error":    &runtime.TypeDef{Name: "runtime.Error", Kind: runtime.KindInterface, MReqs: []string{"Error", "RuntimeError"}},
		"MemStats": hostType("runtime.MemStats", func() any { return &goruntime.MemStats{} }),
		"ReadMemStats": h.fn("runtime.ReadMemStats", func(a []any) (any, error) {
			m, ok := a[0].(*goruntime.MemStats)
			if !ok {
				return nil, fmt.Errorf("ReadMemStats needs *runtime.MemStats, got %T", a[0])
			}
			// Serving the host allocator's counters leaks interpreter
			// activity into script assertions: any delta check
			// (`n0 != m.Mallocs`) false-positives and depends on GC
			// timing. minigo exposes no per-script heap accounting, so
			// fill one snapshot and serve it thereafter — every delta
			// reads as 0 and sanity checks like `m.Sys > 0` still hold.
			memStatsOnce.Do(func() { goruntime.ReadMemStats(&memStatsSnapshot) })
			*m = memStatsSnapshot
			return nil, nil
		}),
		"Caller": &runtime.BuiltinFunc{Name: "runtime.Caller", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) < 1 {
				return nil, errors.New("runtime.Caller needs 1 arg")
			}
			// pcs is top-first: index 0 is the innermost live frame —
			// the builtin's own call site, which is Caller(0) in Go.
			skip := intOf(goNative(args[0]))
			pcs := vc.CallerPCs()
			if skip < 0 || skip >= len(pcs) {
				return &runtime.Tuple{Elems: []runtime.Value{int64(0), "", int64(0), false}}, nil
			}
			pc := pcs[skip]
			s, _ := vc.CallerFrame(pc)
			return &runtime.Tuple{Elems: []runtime.Value{int64(pc), s.File, int64(s.Line), true}}, nil
		}},
		"Callers": &runtime.BuiltinFunc{Name: "runtime.Callers", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) < 2 {
				return nil, errors.New("runtime.Callers needs 2 args")
			}
			sl, ok := args[1].(*runtime.Slice)
			if !ok {
				return nil, fmt.Errorf("runtime.Callers: pc slice is %T", args[1])
			}
			// index 0 is the Callers builtin itself, like Go — CallerFrame
			// never resolves it. skip drops that many leading PCs.
			pcs := append([]uintptr{0}, vc.CallerPCs()...)
			skip := intOf(goNative(args[0]))
			if skip > len(pcs) {
				skip = len(pcs)
			}
			pcs = pcs[skip:]
			n := len(pcs)
			if len(sl.Elems) < n {
				n = len(sl.Elems)
			}
			for i := 0; i < n; i++ {
				sl.Elems[i] = int64(pcs[i])
			}
			return int64(n), nil
		}},
		"CallersFrames": &runtime.BuiltinFunc{Name: "runtime.CallersFrames", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			sl, ok := args[0].(*runtime.Slice)
			if !ok {
				return nil, fmt.Errorf("runtime.CallersFrames: pcs is %T", args[0])
			}
			sites := []runtime.CallSite{}
			for _, e := range sl.Elems {
				if s, ok := vc.CallerFrame(uintptr(int64Of(goNative(e)))); ok {
					sites = append(sites, s)
				}
			}
			return &runtime.GoValue{V: &callerFrames{sites: sites}}, nil
		}},
	})
	// monthTD backs both the time.Month conversion and the January..
	// December consts: bound consts carry the conversion's shape
	// (Named{time.Month, int64}) so time.January == time.Month(1) like
	// gc — a raw time.Month bind never adopted the typedef, so `m ==
	// time.Month(1)` and `m == 1` both diverged.
	monthTD := &runtime.TypeDef{Name: "time.Month", Kind: runtime.KindNamedBasic, Anon: ast.NewIdent("int"), HostScalar: time.Month(0)}
	monthConst := func(m time.Month) runtime.Value {
		return &runtime.Named{Typ: monthTD, V: int64(m)}
	}
	e.Bind("time", map[string]runtime.Value{
		"Sleep": h.fn("time.Sleep", func(a []any) (any, error) { time.Sleep(durOf(a[0])); return nil, nil }),
		"After": h.fnvc("time.After", func(vc runtime.VMCaller, a []any) (any, error) {
			// a proxy feed like NewTimer's: once the fire is delivered the
			// channel is dead, and a second receive must count as asleep
			// for deadlock detection instead of hanging forever.
			f := newChanFeed(vc, time.After(durOf(a[0])), true)
			// gc hands out a <-chan: a script-side send on it must trap,
			// not silently succeed on the bidirectional proxy.
			return (<-chan time.Time)(f.C), nil
		}),
		"NewTimer": h.fnvc("time.NewTimer", func(vc runtime.VMCaller, a []any) (any, error) {
			t := time.NewTimer(durOf(a[0]))
			f := newChanFeed(vc, t.C, true)
			return &timerChanBox{t: t, C: f.C, feed: f}, nil
		}),
		"NewTicker": h.fnvc("time.NewTicker", func(vc runtime.VMCaller, a []any) (any, error) {
			t := time.NewTicker(durOf(a[0]))
			f := newChanFeed(vc, t.C, false)
			return &tickerChanBox{t: t, C: f.C, feed: f}, nil
		}),
		"AfterFunc": &runtime.BuiltinFunc{Name: "time.AfterFunc", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) < 2 {
				return nil, errors.New("time.AfterFunc needs 2 args")
			}
			f := args[1]
			switch f.(type) {
			case *runtime.Function, *runtime.Closure, *runtime.BoundMethod, *runtime.BuiltinFunc, *runtime.Named,
				runtime.Nil, *runtime.TypedNil, *runtime.IfaceNil:
				// a nil callback registers like Go — calling it fails at
				// fire time through the goroutine-failure path
			default:
				return nil, fmt.Errorf("time.AfterFunc: cannot use %T as func()", f)
			}
			// the pending timer keeps the process alive for deadlock
			// detection (a gc timer does the same); the wrapper releases
			// the note when the callback starts — after which the
			// spawned goroutine counts itself — or when Stop prevents
			// the callback from ever running.
			t := &afterFuncTimer{note: func() func() { return vm.NoteExternalWait(vc) }}
			t.hold()
			t.Timer = time.AfterFunc(durOf(goNative(args[0])), func() {
				t.drop()
				// the timer fires on a host goroutine — run the
				// callback like `go f()`: a panic inside fails the
				// process through the same path as a goroutine's.
				vc.Spawn(f, nil)
			})
			return &runtime.GoValue{V: t}, nil
		}},
		"Now":       h.fn("time.Now", func(a []any) (any, error) { return time.Now(), nil }, time.Now),
		"Time":      hostType("time.Time", func() any { return time.Time{} }),
		"Duration":  &runtime.TypeDef{Name: "time.Duration", Kind: runtime.KindNamedBasic, Anon: ast.NewIdent("int64"), HostScalar: time.Duration(0)},
		"Month":     monthTD,
		"January":   monthConst(time.January),
		"February":  monthConst(time.February),
		"March":     monthConst(time.March),
		"April":     monthConst(time.April),
		"May":       monthConst(time.May),
		"June":      monthConst(time.June),
		"July":      monthConst(time.July),
		"August":    monthConst(time.August),
		"September": monthConst(time.September),
		"October":   monthConst(time.October),
		"November":  monthConst(time.November),
		"December":  monthConst(time.December),
		"Location":  hostType("time.Location", func() any { return time.Local }),
		"UTC":       &runtime.GoValue{V: time.UTC},
		"Local":     &runtime.GoValue{V: time.Local},
		"Since": h.fn("time.Since", func(a []any) (any, error) {
			if t, ok := a[0].(time.Time); ok {
				return time.Since(t), nil
			}
			return nil, fmt.Errorf("time.Since: not a Time")
		}),
		"Parse": h.fn2("time.Parse", func(a []any) (any, error) {
			t, err := time.Parse(str(a[0]), str(a[1]))
			return &runtime.Tuple{Elems: []runtime.Value{scriptVal(t), errVal(err)}}, nil
		}),
		"ParseInLocation": h.fn("time.ParseInLocation", func(a []any) (any, error) {
			if len(a) != 3 {
				return nil, errors.New("time.ParseInLocation needs 3 args")
			}
			loc, ok := a[2].(*time.Location)
			if !ok {
				return nil, fmt.Errorf("time.ParseInLocation: %T is not a *time.Location", a[2])
			}
			t, err := time.ParseInLocation(str(a[0]), str(a[1]), loc)
			return &runtime.Tuple{Elems: []runtime.Value{scriptVal(t), errVal(err)}}, nil
		}),
		"ParseDuration": h.fn1("time.ParseDuration", func(a []any) (any, error) {
			d, err := time.ParseDuration(str(a[0]))
			return &runtime.Tuple{Elems: []runtime.Value{scriptVal(d), errVal(err)}}, nil
		}),
		"Date": h.fn("time.Date", func(a []any) (any, error) {
			if len(a) != 8 {
				return nil, fmt.Errorf("time.Date needs 8 args, got %d", len(a))
			}
			loc, ok := a[7].(*time.Location)
			if !ok {
				return nil, fmt.Errorf("time.Date: %T is not a *time.Location", a[7])
			}
			return &runtime.GoValue{V: time.Date(int(int64Of(a[0])), time.Month(int64Of(a[1])),
				int(int64Of(a[2])), int(int64Of(a[3])), int(int64Of(a[4])), int(int64Of(a[5])), int(int64Of(a[6])), loc)}, nil
		}),
		"Unix": h.fn2("time.Unix", func(a []any) (any, error) {
			return &runtime.GoValue{V: time.Unix(int64Of(a[0]), int64Of(a[1]))}, nil
		}),
		"FixedZone": h.fn2("time.FixedZone", func(a []any) (any, error) {
			return &runtime.GoValue{V: time.FixedZone(str(a[0]), int(int64Of(a[1])))}, nil
		}),
		"Nanosecond":  time.Nanosecond,
		"Microsecond": time.Microsecond,
		"Second":      time.Second,
		"Minute":      time.Minute,
		"Hour":        time.Hour,
		"Millisecond": time.Millisecond,
		"Layout":      time.Layout,
		"ANSIC":       time.ANSIC,
		"UnixDate":    time.UnixDate,
		"RubyDate":    time.RubyDate,
		"RFC822":      time.RFC822,
		"RFC822Z":     time.RFC822Z,
		"RFC850":      time.RFC850,
		"RFC1123":     time.RFC1123,
		"RFC1123Z":    time.RFC1123Z,
		"RFC3339":     time.RFC3339,
		"RFC3339Nano": time.RFC3339Nano,
		"Kitchen":     time.Kitchen,
		"Stamp":       time.Stamp,
		"StampMilli":  time.StampMilli,
		"StampMicro":  time.StampMicro,
		"StampNano":   time.StampNano,
		"DateTime":    time.DateTime,
		"DateOnly":    time.DateOnly,
		"TimeOnly":    time.TimeOnly,
	})
	// sync: the real host types back `var wg sync.WaitGroup` — TypeDef.HostNew
	// boxes a fresh Go value per zero, and member access dispatches through
	// the host method set (Lock/Unlock, Add/Wait/Done, Do).
	e.Bind("sync", map[string]runtime.Value{
		"WaitGroup": hostType("sync.WaitGroup", func() any { return &sync.WaitGroup{} }),
		"Mutex":     hostType("sync.Mutex", func() any { return &sync.Mutex{} }),
		"RWMutex":   hostType("sync.RWMutex", func() any { return &sync.RWMutex{} }),
		"Once":      hostType("sync.Once", func() any { return &sync.Once{} }),
		"Map":       hostType("sync.Map", func() any { return &sync.Map{} }),
		"Pool":      hostType("sync.Pool", func() any { return &sync.Pool{} }),
		"Cond":      hostType("sync.Cond", func() any { return &sync.Cond{} }),
		// an interface typedef: `var l sync.Locker` / parameter coercion
		// duck-type against the reflect method set (GoValue) or the
		// declared method set (script types).
		"Locker": &runtime.TypeDef{
			Name: "sync.Locker", Kind: runtime.KindInterface,
			MReqs: []string{"Lock", "Unlock"},
		},
		"NewCond": &runtime.BuiltinFunc{Name: "sync.NewCond", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("sync.NewCond needs 1 arg, got %d", len(args))
			}
			l, err := lockerOf(vc, args[0])
			if err != nil {
				return nil, err
			}
			return &runtime.GoValue{V: sync.NewCond(l)}, nil
		}},
		// sync.OnceFunc family: memoization lives on the host (a real
		// sync.Once), while the wrapped function runs on whichever VM
		// calls the result — like Go, a panic on the first call still
		// consumes the once.
		"OnceFunc": &runtime.BuiltinFunc{Name: "sync.OnceFunc", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			return syncOnceWrap("sync.OnceFunc", args)
		}},
		"OnceValue": &runtime.BuiltinFunc{Name: "sync.OnceValue", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			return syncOnceWrap("sync.OnceValue", args)
		}},
		"OnceValues": &runtime.BuiltinFunc{Name: "sync.OnceValues", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			return syncOnceWrap("sync.OnceValues", args)
		}},
	})
	// sync/atomic: the source implementation reinterprets values through
	// unsafe.Pointer, so the package is bound instead. The scalar types
	// are real host values; Value and Pointer[T] share a mutex-guarded
	// box holding script values verbatim (deepHost already flattens
	// containers at the call boundary, everything else stays raw), and
	// the pointer-taking functions operate on script cells through
	// Deref/SetRef — atomic in intent, since a script &x has no host
	// address to hand the real runtime.
	e.Bind("sync/atomic", map[string]runtime.Value{
		"Bool":    hostType("sync/atomic.Bool", func() any { return &atomic.Bool{} }),
		"Int32":   hostType("sync/atomic.Int32", func() any { return &atomic.Int32{} }),
		"Int64":   hostType("sync/atomic.Int64", func() any { return &atomic.Int64{} }),
		"Uint32":  hostType("sync/atomic.Uint32", func() any { return &atomic.Uint32{} }),
		"Uint64":  hostType("sync/atomic.Uint64", func() any { return &atomic.Uint64{} }),
		"Uintptr": hostType("sync/atomic.Uintptr", func() any { return &atomic.Uintptr{} }),
		"Value":   hostType("sync/atomic.Value", func() any { return &atomicBox{} }),
		// generic: a host reflect instantiation is impossible, so the
		// typedef declares T and HostNew boxes the same cell for every
		// instantiation — the stored script values carry the typing.
		"Pointer": &runtime.TypeDef{
			Name: "sync/atomic.Pointer", Kind: runtime.KindStruct,
			TParams: []string{"T"},
			HostNew: func() any { return &atomicBox{} },
		},
		"AddInt32":              atomicOp("atomic.AddInt32", atomicOpAdd),
		"AddInt64":              atomicOp("atomic.AddInt64", atomicOpAdd),
		"AddUint32":             atomicOp("atomic.AddUint32", atomicOpAdd),
		"AddUint64":             atomicOp("atomic.AddUint64", atomicOpAdd),
		"AddUintptr":            atomicOp("atomic.AddUintptr", atomicOpAdd),
		"AndInt32":              atomicOp("atomic.AndInt32", atomicOpAnd),
		"AndInt64":              atomicOp("atomic.AndInt64", atomicOpAnd),
		"AndUint32":             atomicOp("atomic.AndUint32", atomicOpAnd),
		"AndUint64":             atomicOp("atomic.AndUint64", atomicOpAnd),
		"AndUintptr":            atomicOp("atomic.AndUintptr", atomicOpAnd),
		"OrInt32":               atomicOp("atomic.OrInt32", atomicOpOr),
		"OrInt64":               atomicOp("atomic.OrInt64", atomicOpOr),
		"OrUint32":              atomicOp("atomic.OrUint32", atomicOpOr),
		"OrUint64":              atomicOp("atomic.OrUint64", atomicOpOr),
		"OrUintptr":             atomicOp("atomic.OrUintptr", atomicOpOr),
		"CompareAndSwapInt32":   atomicOp("atomic.CompareAndSwapInt32", atomicOpCAS),
		"CompareAndSwapInt64":   atomicOp("atomic.CompareAndSwapInt64", atomicOpCAS),
		"CompareAndSwapUint32":  atomicOp("atomic.CompareAndSwapUint32", atomicOpCAS),
		"CompareAndSwapUint64":  atomicOp("atomic.CompareAndSwapUint64", atomicOpCAS),
		"CompareAndSwapUintptr": atomicOp("atomic.CompareAndSwapUintptr", atomicOpCAS),
		"CompareAndSwapPointer": atomicOp("atomic.CompareAndSwapPointer", atomicOpCAS),
		"LoadInt32":             atomicOp("atomic.LoadInt32", atomicOpLoad),
		"LoadInt64":             atomicOp("atomic.LoadInt64", atomicOpLoad),
		"LoadUint32":            atomicOp("atomic.LoadUint32", atomicOpLoad),
		"LoadUint64":            atomicOp("atomic.LoadUint64", atomicOpLoad),
		"LoadUintptr":           atomicOp("atomic.LoadUintptr", atomicOpLoad),
		"LoadPointer":           atomicOp("atomic.LoadPointer", atomicOpLoad),
		"StoreInt32":            atomicOp("atomic.StoreInt32", atomicOpStore),
		"StoreInt64":            atomicOp("atomic.StoreInt64", atomicOpStore),
		"StoreUint32":           atomicOp("atomic.StoreUint32", atomicOpStore),
		"StoreUint64":           atomicOp("atomic.StoreUint64", atomicOpStore),
		"StoreUintptr":          atomicOp("atomic.StoreUintptr", atomicOpStore),
		"StorePointer":          atomicOp("atomic.StorePointer", atomicOpStore),
		"SwapInt32":             atomicOp("atomic.SwapInt32", atomicOpSwap),
		"SwapInt64":             atomicOp("atomic.SwapInt64", atomicOpSwap),
		"SwapUint32":            atomicOp("atomic.SwapUint32", atomicOpSwap),
		"SwapUint64":            atomicOp("atomic.SwapUint64", atomicOpSwap),
		"SwapUintptr":           atomicOp("atomic.SwapUintptr", atomicOpSwap),
		"SwapPointer":           atomicOp("atomic.SwapPointer", atomicOpSwap),
	})
	// context: bound so script code hands REAL host contexts to bound
	// APIs (net.Dialer.DialContext, http transports) — an interpreted
	// context object cannot satisfy a host context.Context parameter.
	// With* family values ride GoValue boxes; CancelFunc results arrive
	// as plain host funcs.
	e.Bind("context", map[string]runtime.Value{
		"Context": &runtime.TypeDef{Name: "context.Context", Kind: runtime.KindInterface,
			MReqs: []string{"Deadline", "Done", "Err", "Value"}},
		"CancelFunc": &runtime.TypeDef{Name: "context.CancelFunc", Kind: runtime.KindFunc,
			Anon: &ast.FuncType{Params: &ast.FieldList{}}},
		"CancelCauseFunc": &runtime.TypeDef{Name: "context.CancelCauseFunc", Kind: runtime.KindFunc,
			Anon: &ast.FuncType{Params: &ast.FieldList{List: []*ast.Field{{Type: ast.NewIdent("error")}}}}},
		"Canceled":         &runtime.GoValue{V: context.Canceled},
		"DeadlineExceeded": &runtime.GoValue{V: context.DeadlineExceeded},
		"Background":       h.fn("context.Background", func(a []any) (any, error) { return context.Background(), nil }, context.Background),
		"TODO":             h.fn("context.TODO", func(a []any) (any, error) { return context.TODO(), nil }, context.TODO),
		"WithCancel": h.fnvc("context.WithCancel", func(vc runtime.VMCaller, a []any) (any, error) {
			c, err := asCtx(a[0])
			if err != nil {
				return nil, err
			}
			nc, cancel := context.WithCancel(c)
			watchCtxDone(vc, c, nc)
			return &runtime.Tuple{Elems: []runtime.Value{scriptVal(nc), scriptVal(cancel)}}, nil
		}),
		"WithCancelCause": h.fnvc("context.WithCancelCause", func(vc runtime.VMCaller, a []any) (any, error) {
			c, err := asCtx(a[0])
			if err != nil {
				return nil, err
			}
			nc, cancel := context.WithCancelCause(c)
			watchCtxDone(vc, c, nc)
			return &runtime.Tuple{Elems: []runtime.Value{scriptVal(nc), scriptVal(cancel)}}, nil
		}),
		"WithDeadline": h.fn("context.WithDeadline", func(a []any) (any, error) {
			c, err := asCtx(a[0])
			if err != nil {
				return nil, err
			}
			d, ok := a[1].(time.Time)
			if !ok {
				return nil, fmt.Errorf("context.WithDeadline needs a time.Time, got %T", a[1])
			}
			nc, cancel := context.WithDeadline(c, d)
			return &runtime.Tuple{Elems: []runtime.Value{scriptVal(nc), scriptVal(cancel)}}, nil
		}),
		"WithDeadlineCause": h.fn("context.WithDeadlineCause", func(a []any) (any, error) {
			c, err := asCtx(a[0])
			if err != nil {
				return nil, err
			}
			d, ok := a[1].(time.Time)
			if !ok {
				return nil, fmt.Errorf("context.WithDeadlineCause needs a time.Time, got %T", a[1])
			}
			nc, cancel := context.WithDeadlineCause(c, d, asErr(a[2]))
			return &runtime.Tuple{Elems: []runtime.Value{scriptVal(nc), scriptVal(cancel)}}, nil
		}),
		"WithTimeout": h.fn("context.WithTimeout", func(a []any) (any, error) {
			c, err := asCtx(a[0])
			if err != nil {
				return nil, err
			}
			nc, cancel := context.WithTimeout(c, durOf(a[1]))
			return &runtime.Tuple{Elems: []runtime.Value{scriptVal(nc), scriptVal(cancel)}}, nil
		}),
		"WithTimeoutCause": h.fn("context.WithTimeoutCause", func(a []any) (any, error) {
			c, err := asCtx(a[0])
			if err != nil {
				return nil, err
			}
			nc, cancel := context.WithTimeoutCause(c, durOf(a[1]), asErr(a[2]))
			return &runtime.Tuple{Elems: []runtime.Value{scriptVal(nc), scriptVal(cancel)}}, nil
		}),
		"WithoutCancel": h.fn("context.WithoutCancel", func(a []any) (any, error) {
			c, err := asCtx(a[0])
			if err != nil {
				return nil, err
			}
			return context.WithoutCancel(c), nil
		}, context.WithoutCancel),
		// WithValue stores the key in its canonical form: the valueCtx
		// lookup (opaqueStore's key position) canonicalizes the same
		// way, so equal script keys hit by content and an unhashable key
		// panics like gc's "key is not comparable" — pointer keys keep
		// identity (e.g. http.LocalAddrContextKey).
		"WithValue": &runtime.BuiltinFunc{Name: "context.WithValue", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 3 {
				return nil, fmt.Errorf("context.WithValue needs 3 args, got %d", len(args))
			}
			c, err := asCtx(goNative(args[0]))
			if err != nil {
				return nil, err
			}
			return &runtime.GoValue{V: context.WithValue(c, contextKey(args[1]), args[2])}, nil
		}},
		"AfterFunc": &runtime.BuiltinFunc{Name: "context.AfterFunc", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("context.AfterFunc needs 2 args, got %d", len(args))
			}
			c, err := asCtx(goNative(args[0]))
			if err != nil {
				return nil, err
			}
			f := args[1]
			// a pending AfterFunc callback is a wake source for
			// deadlock detection, like a pending gc timer.
			release := vm.NoteExternalWait(vc)
			stop := context.AfterFunc(c, func() {
				release()
				if _, err := vc.Call(f, nil); err != nil {
					// a dead process refuses the spawn — the callback
					// dies with the run like a Go timer's pending call.
					if !vm.IsProcExit(err) {
						panic(err)
					}
				}
			})
			return &runtime.BuiltinFunc{Name: "context.AfterFunc.stop", Fn: func(_ runtime.VMCaller, _ []runtime.Value) (runtime.Value, error) {
				stopped := stop()
				if stopped {
					release()
				}
				return stopped, nil
			}}, nil
		}},
		"Cause": h.fn("context.Cause", func(a []any) (any, error) {
			c, err := asCtx(a[0])
			if err != nil {
				return nil, err
			}
			return errVal(context.Cause(c)), nil
		}),
	})
	// net: sockets, DNS and syscall plumbing cannot be interpreted, so
	// the package is bound wholesale. The interfaces stay typedefs so
	// script code declares net.Conn/net.Listener/net.Addr/net.Error;
	// concrete values arrive as GoValue boxes whose methods dispatch
	// through reflection, and script pointers (e.g. &net.TCPAddr{...})
	// unbox via goNative at call boundaries.
	e.Bind("net", map[string]runtime.Value{
		"Addr":             &runtime.TypeDef{Name: "net.Addr", Kind: runtime.KindInterface, MReqs: []string{"Network", "String"}},
		"Conn":             &runtime.TypeDef{Name: "net.Conn", Kind: runtime.KindInterface, MReqs: []string{"Read", "Write", "Close", "LocalAddr", "RemoteAddr", "SetDeadline", "SetReadDeadline", "SetWriteDeadline"}},
		"Listener":         &runtime.TypeDef{Name: "net.Listener", Kind: runtime.KindInterface, MReqs: []string{"Accept", "Close", "Addr"}},
		"PacketConn":       &runtime.TypeDef{Name: "net.PacketConn", Kind: runtime.KindInterface, MReqs: []string{"ReadFrom", "WriteTo", "Close", "LocalAddr", "SetDeadline", "SetReadDeadline", "SetWriteDeadline"}},
		"Error":            &runtime.TypeDef{Name: "net.Error", Kind: runtime.KindInterface, MReqs: []string{"Error", "Timeout", "Temporary"}},
		"Buffers":          hostType("net.Buffers", func() any { return &net.Buffers{} }),
		"Dialer":           hostType("net.Dialer", func() any { return &net.Dialer{} }),
		"ListenConfig":     hostType("net.ListenConfig", func() any { return &net.ListenConfig{} }),
		"Resolver":         hostType("net.Resolver", func() any { return &net.Resolver{} }),
		"Interface":        hostType("net.Interface", func() any { return net.Interface{} }),
		"HardwareAddr":     hostType("net.HardwareAddr", func() any { return net.HardwareAddr{} }),
		"IP":               hostType("net.IP", func() any { return net.IP{} }),
		"IPAddr":           hostType("net.IPAddr", func() any { return &net.IPAddr{} }),
		"IPNet":            hostType("net.IPNet", func() any { return &net.IPNet{} }),
		"TCPAddr":          hostType("net.TCPAddr", func() any { return &net.TCPAddr{} }),
		"TCPConn":          hostType("net.TCPConn", func() any { return &net.TCPConn{} }),
		"TCPListener":      hostType("net.TCPListener", func() any { return &net.TCPListener{} }),
		"UDPAddr":          hostType("net.UDPAddr", func() any { return &net.UDPAddr{} }),
		"UDPConn":          hostType("net.UDPConn", func() any { return &net.UDPConn{} }),
		"UnixAddr":         hostType("net.UnixAddr", func() any { return &net.UnixAddr{} }),
		"UnixConn":         hostType("net.UnixConn", func() any { return &net.UnixConn{} }),
		"UnixListener":     hostType("net.UnixListener", func() any { return &net.UnixListener{} }),
		"OpError":          hostType("net.OpError", func() any { return &net.OpError{} }),
		"DNSError":         hostType("net.DNSError", func() any { return &net.DNSError{} }),
		"AddrError":        hostType("net.AddrError", func() any { return &net.AddrError{} }),
		"ParseError":       hostType("net.ParseError", func() any { return &net.ParseError{} }),
		"InvalidAddrError": hostType("net.InvalidAddrError", func() any { return net.InvalidAddrError("") }),
		"Dial":             h.fn("net.Dial", func(a []any) (any, error) { return retErr2(net.Dial(str(a[0]), str(a[1]))) }),
		"DialIP":           h.fn("net.DialIP", func(a []any) (any, error) { return retErr2(net.DialIP(str(a[0]), ipAddrOf(a[1]), ipAddrOf(a[2]))) }),
		"DialTCP":          h.fn("net.DialTCP", func(a []any) (any, error) { return retErr2(net.DialTCP(str(a[0]), tcpAddrOf(a[1]), tcpAddrOf(a[2]))) }),
		"DialTimeout":      h.fn("net.DialTimeout", func(a []any) (any, error) { return retErr2(net.DialTimeout(str(a[0]), str(a[1]), durOf(a[2]))) }),
		"DialUDP":          h.fn("net.DialUDP", func(a []any) (any, error) { return retErr2(net.DialUDP(str(a[0]), udpAddrOf(a[1]), udpAddrOf(a[2]))) }),
		"DialUnix": h.fn("net.DialUnix", func(a []any) (any, error) {
			return retErr2(net.DialUnix(str(a[0]), unixAddrOf(a[1]), unixAddrOf(a[2])))
		}),
		"Listen":       h.fn("net.Listen", func(a []any) (any, error) { return retErr2(net.Listen(str(a[0]), str(a[1]))) }),
		"ListenIP":     h.fn("net.ListenIP", func(a []any) (any, error) { return retErr2(net.ListenIP(str(a[0]), ipAddrOf(a[1]))) }),
		"ListenPacket": h.fn("net.ListenPacket", func(a []any) (any, error) { return retErr2(net.ListenPacket(str(a[0]), str(a[1]))) }),
		"ListenTCP":    h.fn("net.ListenTCP", func(a []any) (any, error) { return retErr2(net.ListenTCP(str(a[0]), tcpAddrOf(a[1]))) }),
		"ListenUDP":    h.fn("net.ListenUDP", func(a []any) (any, error) { return retErr2(net.ListenUDP(str(a[0]), udpAddrOf(a[1]))) }),
		"ListenUnix":   h.fn("net.ListenUnix", func(a []any) (any, error) { return retErr2(net.ListenUnix(str(a[0]), unixAddrOf(a[1]))) }),
		"ListenMulticastUDP": h.fn("net.ListenMulticastUDP", func(a []any) (any, error) {
			return retErr2(net.ListenMulticastUDP(str(a[0]), netIfaceOf(a[1]), udpAddrOf(a[2])))
		}),
		"FileConn":       h.fn("net.FileConn", func(a []any) (any, error) { return retErr2(net.FileConn(netFileOf(a[0]))) }),
		"FileListener":   h.fn("net.FileListener", func(a []any) (any, error) { return retErr2(net.FileListener(netFileOf(a[0]))) }),
		"FilePacketConn": h.fn("net.FilePacketConn", func(a []any) (any, error) { return retErr2(net.FilePacketConn(netFileOf(a[0]))) }),
		"Pipe": h.fn("net.Pipe", func(a []any) (any, error) {
			c1, c2 := net.Pipe()
			return &runtime.Tuple{Elems: []runtime.Value{scriptVal(c1), scriptVal(c2)}}, nil
		}),
		"ResolveTCPAddr":      h.fn("net.ResolveTCPAddr", func(a []any) (any, error) { return retErr2(net.ResolveTCPAddr(str(a[0]), str(a[1]))) }),
		"ResolveUDPAddr":      h.fn("net.ResolveUDPAddr", func(a []any) (any, error) { return retErr2(net.ResolveUDPAddr(str(a[0]), str(a[1]))) }),
		"ResolveIPAddr":       h.fn("net.ResolveIPAddr", func(a []any) (any, error) { return retErr2(net.ResolveIPAddr(str(a[0]), str(a[1]))) }),
		"ResolveUnixAddr":     h.fn("net.ResolveUnixAddr", func(a []any) (any, error) { return retErr2(net.ResolveUnixAddr(str(a[0]), str(a[1]))) }),
		"TCPAddrFromAddrPort": h.fn("net.TCPAddrFromAddrPort", func(a []any) (any, error) { return net.TCPAddrFromAddrPort(addrPortOf(a[0])), nil }),
		"UDPAddrFromAddrPort": h.fn("net.UDPAddrFromAddrPort", func(a []any) (any, error) { return net.UDPAddrFromAddrPort(addrPortOf(a[0])), nil }),
		"SplitHostPort": h.fn("net.SplitHostPort", func(a []any) (any, error) {
			h, p, err := net.SplitHostPort(str(a[0]))
			return &runtime.Tuple{Elems: []runtime.Value{h, p, errVal(err)}}, nil
		}),
		"JoinHostPort": h.fn("net.JoinHostPort", func(a []any) (any, error) { return net.JoinHostPort(str(a[0]), str(a[1])), nil }),
		"ParseIP":      h.fn("net.ParseIP", func(a []any) (any, error) { return net.ParseIP(str(a[0])), nil }),
		"ParseCIDR": h.fn("net.ParseCIDR", func(a []any) (any, error) {
			ip, ipn, err := net.ParseCIDR(str(a[0]))
			return &runtime.Tuple{Elems: []runtime.Value{scriptVal(ip), scriptVal(ipn), errVal(err)}}, nil
		}),
		"IPv4": h.fn("net.IPv4", func(a []any) (any, error) {
			return net.IPv4(byte(intOf(a[0])), byte(intOf(a[1])), byte(intOf(a[2])), byte(intOf(a[3]))), nil
		}),
		"IPv4Mask": h.fn("net.IPv4Mask", func(a []any) (any, error) {
			return net.IPv4Mask(byte(intOf(a[0])), byte(intOf(a[1])), byte(intOf(a[2])), byte(intOf(a[3]))), nil
		}),
		"CIDRMask":    h.fn("net.CIDRMask", func(a []any) (any, error) { return net.CIDRMask(intOf(a[0]), intOf(a[1])), nil }),
		"LookupAddr":  h.fn("net.LookupAddr", func(a []any) (any, error) { return retErr2(net.LookupAddr(str(a[0]))) }),
		"LookupCNAME": h.fn("net.LookupCNAME", func(a []any) (any, error) { return retErr2(net.LookupCNAME(str(a[0]))) }),
		"LookupHost":  h.fn("net.LookupHost", func(a []any) (any, error) { return retErr2(net.LookupHost(str(a[0]))) }),
		"LookupIP":    h.fn("net.LookupIP", func(a []any) (any, error) { return retErr2(net.LookupIP(str(a[0]))) }),
		"LookupMX":    h.fn("net.LookupMX", func(a []any) (any, error) { return retErr2(net.LookupMX(str(a[0]))) }),
		"LookupNS":    h.fn("net.LookupNS", func(a []any) (any, error) { return retErr2(net.LookupNS(str(a[0]))) }),
		"LookupPort":  h.fn("net.LookupPort", func(a []any) (any, error) { return retErr2(net.LookupPort(str(a[0]), str(a[1]))) }),
		"LookupSRV": h.fn("net.LookupSRV", func(a []any) (any, error) {
			cname, srvs, err := net.LookupSRV(str(a[0]), str(a[1]), str(a[2]))
			return &runtime.Tuple{Elems: []runtime.Value{cname, scriptVal(srvs), errVal(err)}}, nil
		}),
		"LookupTXT":                  h.fn("net.LookupTXT", func(a []any) (any, error) { return retErr2(net.LookupTXT(str(a[0]))) }),
		"Interfaces":                 h.fn("net.Interfaces", func(a []any) (any, error) { return retErr2(net.Interfaces()) }),
		"InterfaceAddrs":             h.fn("net.InterfaceAddrs", func(a []any) (any, error) { return retErr2(net.InterfaceAddrs()) }),
		"InterfaceByIndex":           h.fn("net.InterfaceByIndex", func(a []any) (any, error) { return retErr2(net.InterfaceByIndex(intOf(a[0]))) }),
		"InterfaceByName":            h.fn("net.InterfaceByName", func(a []any) (any, error) { return retErr2(net.InterfaceByName(str(a[0]))) }),
		"DefaultResolver":            &runtime.GoValue{V: net.DefaultResolver},
		"ErrClosed":                  &runtime.GoValue{V: net.ErrClosed},
		"ErrWriteToConnected":        &runtime.GoValue{V: net.ErrWriteToConnected},
		"IPv4zero":                   &runtime.GoValue{V: net.IPv4zero},
		"IPv4bcast":                  &runtime.GoValue{V: net.IPv4bcast},
		"IPv4allsys":                 &runtime.GoValue{V: net.IPv4allsys},
		"IPv4allrouter":              &runtime.GoValue{V: net.IPv4allrouter},
		"IPv6zero":                   &runtime.GoValue{V: net.IPv6zero},
		"IPv6unspecified":            &runtime.GoValue{V: net.IPv6unspecified},
		"IPv6interfacelocalallnodes": &runtime.GoValue{V: net.IPv6interfacelocalallnodes},
		"IPv6linklocalallnodes":      &runtime.GoValue{V: net.IPv6linklocalallnodes},
		"IPv6linklocalallrouters":    &runtime.GoValue{V: net.IPv6linklocalallrouters},
		"IPv6loopback":               &runtime.GoValue{V: net.IPv6loopback},
	})
	// net/netip: the value types back net/http's addr plumbing
	// (TCPAddrFromAddrPort and friends); bound as host values like net.
	e.Bind("net/netip", map[string]runtime.Value{
		"Addr":       hostType("net/netip.Addr", func() any { return netip.Addr{} }),
		"AddrPort":   hostType("net/netip.AddrPort", func() any { return netip.AddrPort{} }),
		"Prefix":     hostType("net/netip.Prefix", func() any { return netip.Prefix{} }),
		"AddrFrom4":  h.fn("netip.AddrFrom4", func(a []any) (any, error) { return netip.AddrFrom4(byteSlice4(a[0])), nil }),
		"AddrFrom16": h.fn("netip.AddrFrom16", func(a []any) (any, error) { return netip.AddrFrom16(byteSlice16(a[0])), nil }),
		"AddrFromSlice": h.fn("netip.AddrFromSlice", func(a []any) (any, error) {
			ip, ok := netip.AddrFromSlice(byteSlice(a[0]))
			return &runtime.Tuple{Elems: []runtime.Value{scriptVal(ip), ok}}, nil
		}),
		"AddrPortFrom": h.fn("netip.AddrPortFrom", func(a []any) (any, error) {
			return netip.AddrPortFrom(netipAddrOf(a[0]), uint16(intOf(a[1]))), nil
		}),
		"PrefixFrom": h.fn("netip.PrefixFrom", func(a []any) (any, error) {
			return netip.PrefixFrom(netipAddrOf(a[0]), intOf(a[1])), nil
		}),
		"MustParseAddr":         h.fn("netip.MustParseAddr", func(a []any) (any, error) { return netip.MustParseAddr(str(a[0])), nil }),
		"MustParseAddrPort":     h.fn("netip.MustParseAddrPort", func(a []any) (any, error) { return netip.MustParseAddrPort(str(a[0])), nil }),
		"MustParsePrefix":       h.fn("netip.MustParsePrefix", func(a []any) (any, error) { return netip.MustParsePrefix(str(a[0])), nil }),
		"ParseAddr":             h.fn("netip.ParseAddr", func(a []any) (any, error) { return retErr2(netip.ParseAddr(str(a[0]))) }),
		"ParsePrefix":           h.fn("netip.ParsePrefix", func(a []any) (any, error) { return retErr2(netip.ParsePrefix(str(a[0]))) }),
		"IPv4Unspecified":       h.fn("netip.IPv4Unspecified", func(a []any) (any, error) { return netip.IPv4Unspecified(), nil }),
		"IPv6LinkLocalAllNodes": h.fn("netip.IPv6LinkLocalAllNodes", func(a []any) (any, error) { return netip.IPv6LinkLocalAllNodes(), nil }),
		"IPv6Loopback":          h.fn("netip.IPv6Loopback", func(a []any) (any, error) { return netip.IPv6Loopback(), nil }),
		"IPv6Unspecified":       h.fn("netip.IPv6Unspecified", func(a []any) (any, error) { return netip.IPv6Unspecified(), nil }),
	})
	// io: bound as natives because the interpreted io package cannot provide
	// singleton identity — `err == io.EOF` compares GoValues by pointer, so
	// EOF must be the real host sentinel. Reader/Writer interfaces are
	// typedefs so script values can declare and satisfy them.
	// ioCall adapts host io helpers: each spec letter picks how an arg is
	// adapted — 'r' an io.Reader, 'w' an io.Writer (both through the
	// caller's VM so script types like http.bodyEOFSignal resolve by
	// method set), 'v' a value used verbatim (byteSlice/str/intOf see the
	// raw runtime.Value). A trailing '*' repeats the first spec letter
	// over every arg for variadic helpers.
	ioCall := func(name, spec string, f func(a []any) (any, error)) *runtime.BuiltinFunc {
		return &runtime.BuiltinFunc{Name: name, Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			n := len(spec)
			if n > 0 && spec[n-1] == '*' {
				// variadic: io.MultiReader() with zero args is valid
			} else if len(args) != n {
				return nil, fmt.Errorf("%s needs %d args, got %d", name, n, len(args))
			}
			a := make([]any, len(args))
			for i := range args {
				c := spec[0]
				if n > 0 && spec[n-1] != '*' {
					c = spec[i]
				}
				switch c {
				case 'r':
					r, err := h.asReaderVM(vc, args[i])
					if err != nil {
						return nil, err
					}
					a[i] = r
				case 'w':
					w, err := asWriterVM(vc, args[i])
					if err != nil {
						return nil, err
					}
					a[i] = w
				default:
					a[i] = args[i]
				}
			}
			return f(a)
		}}
	}
	e.Bind("io", map[string]runtime.Value{
		"Reader":       &runtime.TypeDef{Name: "io.Reader", Kind: runtime.KindInterface, MReqs: []string{"Read"}},
		"Writer":       &runtime.TypeDef{Name: "io.Writer", Kind: runtime.KindInterface, MReqs: []string{"Write"}},
		"Closer":       &runtime.TypeDef{Name: "io.Closer", Kind: runtime.KindInterface, MReqs: []string{"Close"}},
		"ReadWriter":   &runtime.TypeDef{Name: "io.ReadWriter", Kind: runtime.KindInterface, MReqs: []string{"Read", "Write"}},
		"Seeker":       &runtime.TypeDef{Name: "io.Seeker", Kind: runtime.KindInterface, MReqs: []string{"Seek"}},
		"ReadSeeker":   &runtime.TypeDef{Name: "io.ReadSeeker", Kind: runtime.KindInterface, MReqs: []string{"Read", "Seek"}},
		"WriterAt":     &runtime.TypeDef{Name: "io.WriterAt", Kind: runtime.KindInterface, MReqs: []string{"WriteAt"}},
		"ReaderAt":     &runtime.TypeDef{Name: "io.ReaderAt", Kind: runtime.KindInterface, MReqs: []string{"ReadAt"}},
		"ReaderFrom":   &runtime.TypeDef{Name: "io.ReaderFrom", Kind: runtime.KindInterface, MReqs: []string{"ReadFrom"}},
		"WriterTo":     &runtime.TypeDef{Name: "io.WriterTo", Kind: runtime.KindInterface, MReqs: []string{"WriteTo"}},
		"ByteReader":   &runtime.TypeDef{Name: "io.ByteReader", Kind: runtime.KindInterface, MReqs: []string{"ReadByte"}},
		"ByteWriter":   &runtime.TypeDef{Name: "io.ByteWriter", Kind: runtime.KindInterface, MReqs: []string{"WriteByte"}},
		"RuneReader":   &runtime.TypeDef{Name: "io.RuneReader", Kind: runtime.KindInterface, MReqs: []string{"ReadRune"}},
		"StringWriter": &runtime.TypeDef{Name: "io.StringWriter", Kind: runtime.KindInterface, MReqs: []string{"WriteString"}},
		"ReadCloser":   &runtime.TypeDef{Name: "io.ReadCloser", Kind: runtime.KindInterface, MReqs: []string{"Read", "Close"}},
		"WriteCloser":  &runtime.TypeDef{Name: "io.WriteCloser", Kind: runtime.KindInterface, MReqs: []string{"Write", "Close"}},
		"ReadWriteCloser": &runtime.TypeDef{Name: "io.ReadWriteCloser", Kind: runtime.KindInterface,
			MReqs: []string{"Read", "Write", "Close"}},
		"WriteSeeker": &runtime.TypeDef{Name: "io.WriteSeeker", Kind: runtime.KindInterface, MReqs: []string{"Write", "Seek"}},
		"ReadSeekCloser": &runtime.TypeDef{Name: "io.ReadSeekCloser", Kind: runtime.KindInterface,
			MReqs: []string{"Read", "Seek", "Close"}},
		"ReadWriteSeeker": &runtime.TypeDef{Name: "io.ReadWriteSeeker", Kind: runtime.KindInterface,
			MReqs: []string{"Read", "Write", "Seek"}},
		"NewSectionReader": h.fn3("io.NewSectionReader", func(a []any) (any, error) {
			ra, ok := a[0].(io.ReaderAt)
			if !ok {
				if g, isGV := a[0].(*runtime.GoValue); isGV {
					if rr, isRA := g.V.(io.ReaderAt); isRA {
						ra = rr
						ok = true
					}
				}
			}
			if !ok {
				return nil, fmt.Errorf("io.NewSectionReader: not an io.ReaderAt: %T", a[0])
			}
			return &runtime.GoValue{V: io.NewSectionReader(ra, int64Of(a[1]), int64Of(a[2]))}, nil
		}, io.NewSectionReader),
		"EOF":              errVal(io.EOF),
		"ErrClosedPipe":    errVal(io.ErrClosedPipe),
		"ErrNoProgress":    errVal(io.ErrNoProgress),
		"ErrShortBuffer":   errVal(io.ErrShortBuffer),
		"ErrShortWrite":    errVal(io.ErrShortWrite),
		"ErrUnexpectedEOF": errVal(io.ErrUnexpectedEOF),
		"Discard":          &runtime.GoValue{V: io.Discard},
		// *io.LimitedReader assertions (http's body.readLocked probes
		// `lr.N == 0` for early-EOF) need the typedef; field reads like
		// `lr.N` dispatch on the host struct through reflection.
		"LimitedReader": hostType("io.LimitedReader", func() any { return &io.LimitedReader{} }),
		"SeekStart":     &runtime.UConst{V: constant.MakeInt64(io.SeekStart)},
		"SeekCurrent":   &runtime.UConst{V: constant.MakeInt64(io.SeekCurrent)},
		"SeekEnd":       &runtime.UConst{V: constant.MakeInt64(io.SeekEnd)},
		"ReadAll": ioCall("io.ReadAll", "r", func(a []any) (any, error) {
			b, rerr := io.ReadAll(a[0].(io.Reader))
			return &runtime.Tuple{Elems: []runtime.Value{scriptVal(b), errVal(rerr)}}, nil
		}),
		"WriteString": ioCall("io.WriteString", "wv", func(a []any) (any, error) {
			n, werr := io.WriteString(a[0].(io.Writer), str(a[1]))
			return &runtime.Tuple{Elems: []runtime.Value{int64(n), errVal(werr)}}, nil
		}),
		"Copy": ioCall("io.Copy", "wr", func(a []any) (any, error) {
			n, cerr := io.Copy(a[0].(io.Writer), a[1].(io.Reader))
			return &runtime.Tuple{Elems: []runtime.Value{int64(n), errVal(cerr)}}, nil
		}),
		"CopyBuffer": ioCall("io.CopyBuffer", "wrv", func(a []any) (any, error) {
			n, cerr := io.CopyBuffer(a[0].(io.Writer), a[1].(io.Reader), byteSlice(a[2]))
			return &runtime.Tuple{Elems: []runtime.Value{int64(n), errVal(cerr)}}, nil
		}),
		"CopyN": ioCall("io.CopyN", "wrv", func(a []any) (any, error) {
			n, cerr := io.CopyN(a[0].(io.Writer), a[1].(io.Reader), int64Of(a[2]))
			return &runtime.Tuple{Elems: []runtime.Value{int64(n), errVal(cerr)}}, nil
		}),
		// ReadFull borrows the script buffer instead of a byteSlice copy —
		// a copy would drop the reader's writes and leave the script slice
		// untouched (#362).
		"ReadFull": ioCall("io.ReadFull", "rv", func(a []any) (any, error) {
			bs, commit := borrowBytes(a[1])
			n, rerr := io.ReadFull(a[0].(io.Reader), bs)
			commit()
			return &runtime.Tuple{Elems: []runtime.Value{int64(n), errVal(rerr)}}, nil
		}),
		"ReadAtLeast": ioCall("io.ReadAtLeast", "rvv", func(a []any) (any, error) {
			bs, commit := borrowBytes(a[1])
			n, rerr := io.ReadAtLeast(a[0].(io.Reader), bs, intOf(a[2]))
			commit()
			return &runtime.Tuple{Elems: []runtime.Value{int64(n), errVal(rerr)}}, nil
		}),
		"LimitReader": ioCall("io.LimitReader", "rv", func(a []any) (any, error) {
			return &runtime.GoValue{V: io.LimitReader(a[0].(io.Reader), int64Of(a[1]))}, nil
		}),
		"TeeReader": ioCall("io.TeeReader", "rw", func(a []any) (any, error) {
			return &runtime.GoValue{V: io.TeeReader(a[0].(io.Reader), a[1].(io.Writer))}, nil
		}),
		"MultiReader": ioCall("io.MultiReader", "r*", func(a []any) (any, error) {
			rs := make([]io.Reader, len(a))
			for i, x := range a {
				rs[i] = x.(io.Reader)
			}
			return &runtime.GoValue{V: io.MultiReader(rs...)}, nil
		}),
		"MultiWriter": ioCall("io.MultiWriter", "w*", func(a []any) (any, error) {
			ws := make([]io.Writer, len(a))
			for i, x := range a {
				ws[i] = x.(io.Writer)
			}
			return &runtime.GoValue{V: io.MultiWriter(ws...)}, nil
		}),
		"NopCloser": &runtime.BuiltinFunc{Name: "io.NopCloser", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("io.NopCloser needs 1 arg, got %d", len(args))
			}
			r, err := h.asReaderVM(vc, args[0])
			if err != nil {
				return nil, err
			}
			return &runtime.GoValue{V: io.NopCloser(r)}, nil
		}},
		"Pipe": h.fn("io.Pipe", func(a []any) (any, error) {
			pr, pw := io.Pipe()
			return &runtime.Tuple{Elems: []runtime.Value{&runtime.GoValue{V: pr}, &runtime.GoValue{V: pw}}}, nil
		}, io.Pipe),
	})
	// crypto/sha256: Sum256's [32]byte result unboxes element-wise so
	// `sum[:]` slices and hex.EncodeToString consume it.
	e.Bind("crypto/sha256", map[string]runtime.Value{
		"New": h.fn("sha256.New", func(a []any) (any, error) {
			return &runtime.GoValue{V: sha256.New()}, nil
		}, sha256.New),
		"New224": h.fn("sha256.New224", func(a []any) (any, error) {
			return &runtime.GoValue{V: sha256.New224()}, nil
		}, sha256.New224),
		"Sum256": h.fn1("sha256.Sum256", func(a []any) (any, error) {
			sum := sha256.Sum256(byteSlice(a[0]))
			return scriptVal(sum[:]), nil
		}, sha256.Sum256),
		"Sum224": h.fn1("sha256.Sum224", func(a []any) (any, error) {
			sum := sha256.Sum224(byteSlice(a[0]))
			return scriptVal(sum[:]), nil
		}, sha256.Sum224),
		"Size":      &runtime.UConst{V: constant.MakeInt64(sha256.Size)},
		"Size224":   &runtime.UConst{V: constant.MakeInt64(sha256.Size224)},
		"BlockSize": &runtime.UConst{V: constant.MakeInt64(sha256.BlockSize)},
	})
	// encoding/csv: NewReader binds the real *csv.Reader so Read/ReadAll and
	// field tuning (Comma/FieldsPerRecord via host field writes) work.
	e.Bind("encoding/csv", map[string]runtime.Value{
		"NewReader": h.fn1("csv.NewReader", func(a []any) (any, error) {
			r, err := asReader(a[0])
			if err != nil {
				return nil, err
			}
			return &runtime.GoValue{V: csv.NewReader(r)}, nil
		}, csv.NewReader),
		"NewWriter": h.fn1("csv.NewWriter", func(a []any) (any, error) {
			w, err := asWriter(a[0])
			if err != nil {
				return nil, err
			}
			return &runtime.GoValue{V: csv.NewWriter(w)}, nil
		}, csv.NewWriter),
	})
	// bufio: Scanner/Reader/Writer box the host types — Scan/Text/Err and
	// friends dispatch through reflection.
	e.Bind("bufio", map[string]runtime.Value{
		// the sentinel is the real host error: `err == bufio.ErrBufferFull`
		// in csv's reader compares GoValues by identity.
		"ErrBufferFull": errVal(bufio.ErrBufferFull),
		// type assertions like `w.(*bufio.Writer)` need the typedefs;
		// methods run on the boxed host object through reflection.
		"Reader":     hostType("bufio.Reader", func() any { return bufio.NewReader(nil) }),
		"Writer":     hostType("bufio.Writer", func() any { return bufio.NewWriter(io.Discard) }),
		"Scanner":    hostType("bufio.Scanner", func() any { return bufio.NewScanner(nil) }),
		"ReadWriter": hostType("bufio.ReadWriter", func() any { return bufio.NewReadWriter(bufio.NewReader(nil), bufio.NewWriter(io.Discard)) }),
		"NewScanner": &runtime.BuiltinFunc{Name: "bufio.NewScanner", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("bufio.NewScanner needs 1 arg")
			}
			r, err := h.asReaderVM(vc, args[0])
			if err != nil {
				return nil, err
			}
			return &runtime.GoValue{V: bufio.NewScanner(r)}, nil
		}},
		"NewReader": &runtime.BuiltinFunc{Name: "bufio.NewReader", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("bufio.NewReader needs 1 arg")
			}
			r, err := h.asReaderVM(vc, args[0])
			if err != nil {
				return nil, err
			}
			return &runtime.GoValue{V: bufio.NewReader(r)}, nil
		}},
		"NewReaderSize": &runtime.BuiltinFunc{Name: "bufio.NewReaderSize", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("bufio.NewReaderSize needs 2 args")
			}
			r, err := h.asReaderVM(vc, args[0])
			if err != nil {
				return nil, err
			}
			return &runtime.GoValue{V: bufio.NewReaderSize(r, int(int64Of(args[1])))}, nil
		}},
		"NewWriter": &runtime.BuiltinFunc{Name: "bufio.NewWriter", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("bufio.NewWriter needs 1 arg")
			}
			w, err := asWriterVM(vc, args[0])
			if err != nil {
				return nil, err
			}
			return &runtime.GoValue{V: bufio.NewWriter(w)}, nil
		}},
		"NewWriterSize": &runtime.BuiltinFunc{Name: "bufio.NewWriterSize", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("bufio.NewWriterSize needs 2 args")
			}
			w, err := asWriterVM(vc, args[0])
			if err != nil {
				return nil, err
			}
			return &runtime.GoValue{V: bufio.NewWriterSize(w, int(int64Of(args[1])))}, nil
		}},
	})
	// text/template: New/Parse/Must box *template.Template — Execute's
	// io.Writer arg adapts `&b` cells via toReflectValue's auto-pointer
	// rule and the data map marshals through goNative.
	e.Bind("text/template", map[string]runtime.Value{
		"Template": hostType("text/template.Template", func() any { return template.New("") }),
		"New": h.fn1("template.New", func(a []any) (any, error) {
			return &runtime.GoValue{V: template.New(str(a[0]))}, nil
		}, template.New),
		"Must": h.fn1("template.Must", func(a []any) (any, error) {
			// Must takes the (t, err) pair of New/Parse: a Tuple arg
			// unpacks, a plain value passes through — the panic on a
			// non-nil err mirrors template.Must.
			t, err := a[0], error(nil)
			if tup, ok := a[0].(*runtime.Tuple); ok && len(tup.Elems) == 2 {
				t = tup.Elems[0]
				err = asErr(goNative(tup.Elems[1]))
			}
			if err != nil {
				panic(err)
			}
			return scriptVal(t), nil
		}, template.Must),
		"HTMLEscapeString": h.fn1("template.HTMLEscapeString", func(a []any) (any, error) {
			return template.HTMLEscapeString(str(a[0])), nil
		}, template.HTMLEscapeString),
		"JSEscapeString": h.fn1("template.JSEscapeString", func(a []any) (any, error) {
			return template.JSEscapeString(str(a[0])), nil
		}, template.JSEscapeString),
		"URLQueryEscaper": h.fn("template.URLQueryEscaper", func(a []any) (any, error) {
			ss := make([]any, len(a))
			for i, x := range a {
				ss[i] = str(x)
			}
			return template.URLQueryEscaper(ss...), nil
		}, template.URLQueryEscaper),
	})
}

// chanFeed proxies a timer/ticker channel so a parked receiver's wake
// state can follow the channel's liveness: armed while a future send
// exists (the timer pending, the ticker ticking), dead once none can
// arrive (Stop won, or a one-shot fire was already delivered). The
// proxy registers its hchan as managed — chanOf then counts a receiver
// parked on a dead feed as asleep, the way gc treats a channel no timer
// can feed, instead of leaving the park wakeable forever.
type chanFeed struct {
	vc    runtime.VMCaller
	C     chan time.Time   // the script-visible channel (proxied)
	src   <-chan time.Time // the real timer channel the pump forwards
	ptr   uintptr          // managed-channel id (0 = unmanaged, no proc)
	done  chan struct{}    // closed to retire the pump
	once  bool             // one-shot feed: die after the first value
	mu    sync.Mutex
	armed bool // the pump is expected to deliver (more) values
	gen   int  // lifecycle generation — arm() bumps it so a superseded pump can't die on the new arm's behalf
}

// newChanFeed starts a pump forwarding src into a buffered proxy channel
// and registers the proxy's liveness with the caller's process.
func newChanFeed(vc runtime.VMCaller, src <-chan time.Time, once bool) *chanFeed {
	f := &chanFeed{
		vc:    vc,
		C:     make(chan time.Time, 1),
		src:   src,
		once:  once,
		done:  make(chan struct{}),
		armed: true,
	}
	f.ptr = vm.RegisterWakeChan(vc, reflect.ValueOf(f.C))
	go f.pump(f.done, f.gen)
	return f
}

// pump forwards each src value into the proxy until done closes; a
// one-shot feed dies after its single delivery. The done channel and
// the generation pin the pump to exactly one lifecycle — after a
// die→arm or supersede→arm cycle the retired pump must exit on the
// channel it was started with and must not retire the newer arm, so
// neither observes f.done/f.gen swapped under it.
func (f *chanFeed) pump(done chan struct{}, gen int) {
	for {
		select {
		case v := <-f.src:
			if !f.once {
				// a repeating feed drops a tick nobody has collected
				// yet, like gc's ticker — the 1-buffer keeps only the
				// latest.
				select {
				case f.C <- v:
				case <-done:
					return
				default:
				}
				continue
			}
			select {
			case f.C <- v:
			case <-done:
				return
			}
			f.dieIf(gen)
			return
		case <-done:
			return
		}
	}
}

// dieIf retires the feed on a pump's behalf — but only while that
// pump's generation is still the live one. A Reset that already
// re-armed the feed owns its lifecycle; a superseded pump's death must
// not kill it.
func (f *chanFeed) dieIf(gen int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if gen != f.gen || !f.armed {
		return
	}
	f.armed = false
	close(f.done)
	if f.ptr != 0 {
		vm.SetWakeChanDead(f.vc, f.ptr, true)
	}
}

// die marks the feed unable to deliver again: the pump retires and the
// managed channel goes dead, so parked receivers join the asleep count.
// It is unconditional — a Stop wins whatever generation is live.
func (f *chanFeed) die() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.armed {
		return
	}
	f.armed = false
	close(f.done)
	if f.ptr != 0 {
		vm.SetWakeChanDead(f.vc, f.ptr, true)
	}
}

// arm (re)starts the feed after a Reset: the managed channel reports
// alive again and a fresh pump forwards the rescheduled sends. An
// already-armed feed is superseded, not skipped — its pump retires on
// the old done channel so a mid-flight one-shot death cannot mark the
// re-armed channel dead.
func (f *chanFeed) arm() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ptr != 0 {
		vm.SetWakeChanDead(f.vc, f.ptr, false)
	}
	if f.armed {
		close(f.done)
	}
	f.gen++
	f.armed = true
	f.done = make(chan struct{})
	go f.pump(f.done, f.gen)
}

// timerChanBox is the time.Timer NewTimer hands to the script: only the
// Timer surface (C/Stop/Reset) while the feed tracks liveness. The
// underlying timer stays unexported so the real channel cannot leak past
// the proxy.
type timerChanBox struct {
	t    *time.Timer
	C    <-chan time.Time
	feed *chanFeed
}

// Stop cancels the timer like time.Timer.Stop: a true return means no
// fire can arrive, so the feed dies and parked receivers count asleep.
func (b *timerChanBox) Stop() bool {
	if !b.t.Stop() {
		return false
	}
	b.feed.die()
	return true
}

// Reset reschedules the timer like time.Timer.Reset: a dead feed
// re-arms, a live one keeps its pump.
func (b *timerChanBox) Reset(d time.Duration) bool {
	active := b.t.Reset(d)
	b.feed.arm()
	return active
}

// tickerChanBox is the time.Ticker NewTicker hands to the script; the
// feed stays armed until Stop, since a ticker can always send again.
type tickerChanBox struct {
	t    *time.Ticker
	C    <-chan time.Time
	feed *chanFeed
}

// Stop halts the ticker like time.Ticker.Stop: no more ticks can arrive,
// so the feed dies and parked receivers count asleep.
func (b *tickerChanBox) Stop() {
	b.t.Stop()
	b.feed.die()
}

// proxyHostTypes maps each liveness-tracking proxy box to the API type
// it stands in for: the script must see *time.Timer/*time.Ticker (in %T
// and reflect), never the box's own name.
var proxyHostTypes = map[reflect.Type]reflect.Type{
	reflect.TypeOf((*timerChanBox)(nil)):   reflect.TypeOf((*time.Timer)(nil)),
	reflect.TypeOf((*tickerChanBox)(nil)):  reflect.TypeOf((*time.Ticker)(nil)),
	reflect.TypeOf((*afterFuncTimer)(nil)): reflect.TypeOf((*time.Timer)(nil)),
}

// proxyHostType resolves the public spelling of a host type — the
// TypeAlias hook for the reflect facade.
func proxyHostType(rt reflect.Type) reflect.Type {
	if rt == nil {
		return nil
	}
	if a, ok := proxyHostTypes[rt]; ok {
		return a
	}
	return rt
}

// proxyHostTypeOf is proxyHostType over a value — the fmt path's %T
// lookup.
func proxyHostTypeOf(v any) reflect.Type {
	if v == nil {
		return nil
	}
	return proxyHostTypes[reflect.TypeOf(v)]
}

// Reset restarts the ticker like time.Ticker.Reset: a stopped feed
// re-arms so parked receivers wake again.
func (b *tickerChanBox) Reset(d time.Duration) {
	b.t.Reset(d)
	b.feed.arm()
}

// afterFuncTimer is the time.Timer AfterFunc hands to the script: a
// *time.Timer wrapper that owns the pending-callback note registered
// for deadlock detection. The note releases when the callback fires or
// when Stop wins; Reset re-arms it, like gc rescheduling the callback.
// hold/drop keep exactly one note per pending fire.
type afterFuncTimer struct {
	*time.Timer
	mu      sync.Mutex
	note    func() func() // registers a fresh external-wait note
	release func()        // releases the held note (nil when none)
}

// hold takes a new note if none is held. Call with no lock held.
func (t *afterFuncTimer) hold() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.release == nil {
		t.release = t.note()
	}
}

// drop releases the held note, if any. The release funcs are
// once-guarded, but clearing the field also stops a later Stop from
// double-counting a callback that already fired.
func (t *afterFuncTimer) drop() {
	t.mu.Lock()
	r := t.release
	t.release = nil
	t.mu.Unlock()
	if r != nil {
		r()
	}
}

// Stop cancels the timer like time.Timer.Stop: a true return means the
// callback will never run, so its wait note is released.
func (t *afterFuncTimer) Stop() bool {
	if !t.Timer.Stop() {
		return false
	}
	t.drop()
	return true
}

// Reset reschedules the timer like time.Timer.Reset: a callback the
// timer may now fire must hold a wait note again.
func (t *afterFuncTimer) Reset(d time.Duration) bool {
	active := t.Timer.Reset(d)
	t.hold()
	return active
}

// hostType is a TypeDef whose zero is a host value: `var m T` and `T{}`
// produce a boxed *newT() so selectMember sees the real method set.
func hostType(name string, new func() any) *runtime.TypeDef {
	return &runtime.TypeDef{Name: name, Kind: runtime.KindStruct, HostNew: new}
}

// lockerOf resolves a script value to a sync.Locker for NewCond: host
// values pass through goNative (*sync.Mutex, *sync.RWMutex), while a
// script-defined value adapts through its Lock/Unlock members — calls
// run back on the VM that built the Cond, the same hazard as every
// retained host callback.
func lockerOf(vc runtime.VMCaller, v runtime.Value) (sync.Locker, error) {
	if l, ok := goNative(v).(sync.Locker); ok {
		return l, nil
	}
	lock, lok := runtime.IfaceMember(vc, v, "Lock")
	unlock, uok := runtime.IfaceMember(vc, v, "Unlock")
	if lok && uok {
		return &scriptLocker{vc: vc, lock: lock, unlock: unlock}, nil
	}
	return nil, fmt.Errorf("sync.NewCond: cannot use %T as sync.Locker", v)
}

// scriptLocker adapts a script value with Lock/Unlock methods to a host
// sync.Locker so sync.Cond can wait on it.
type scriptLocker struct {
	vc           runtime.VMCaller
	lock, unlock runtime.Value
}

func (s *scriptLocker) Lock()   { s.call(s.lock) }
func (s *scriptLocker) Unlock() { s.call(s.unlock) }

func (s *scriptLocker) call(fn runtime.Value) {
	if _, err := s.vc.Call(fn, nil); err != nil {
		panic(err)
	}
}

// syncOnceWrap implements the sync.OnceFunc family: the returned callable
// memoizes the script function's first result under a real host
// sync.Once. The inner call runs on the VM that invokes the wrapper
// (vc.Call inside Fn), and a panic on that first call consumes the once
// like Go's OnceFunc does.
func syncOnceWrap(name string, args []runtime.Value) (runtime.Value, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("%s needs 1 arg, got %d", name, len(args))
	}
	f := args[0]
	var once sync.Once
	var res runtime.Value
	return &runtime.BuiltinFunc{Name: name + "(...)", Fn: func(vc runtime.VMCaller, _ []runtime.Value) (runtime.Value, error) {
		once.Do(func() {
			r, err := vc.Call(f, nil)
			if err != nil {
				panic(err)
			}
			res = r
		})
		if res == nil {
			return runtime.NIL, nil
		}
		return res, nil
	}}, nil
}

// ---- sync/atomic ----

// atomicBox backs the bound sync/atomic Value and Pointer[T]: the cell
// holds script values verbatim under a mutex so Load hands back the same
// objects Store received (a host atomic.Value would serve identically —
// the boundary already decides what crosses — but the box keeps the
// semantics obvious and needs no per-instantiation host type).
type atomicBox struct {
	mu sync.Mutex
	v  any
}

func (b *atomicBox) Load() (old any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.v
}

func (b *atomicBox) Store(x any) {
	b.mu.Lock()
	b.v = x
	b.mu.Unlock()
}

func (b *atomicBox) Swap(x any) (old any) {
	b.mu.Lock()
	old, b.v = b.v, x
	b.mu.Unlock()
	return old
}

func (b *atomicBox) CompareAndSwap(old, new any) (swapped bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !atomicCellEq(b.v, old) {
		return false
	}
	b.v = new
	return true
}

// atomicCellEq compares a stored value against a CAS old operand: nils
// match nils, non-comparable payloads never match (real atomic.Value
// panics on them), identical references or scalars match.
func atomicCellEq(a, b any) bool {
	ua, ub := runtime.Unwrap(a), runtime.Unwrap(b)
	if ua == nil || ub == nil {
		return ua == nil && ub == nil
	}
	if ua == runtime.NIL || ub == runtime.NIL {
		return ua == ub
	}
	ta, tb := reflect.TypeOf(ua), reflect.TypeOf(ub)
	if ta != tb {
		// int-family payloads across a named re-wrap (CAS(0, 1) on a
		// uint32-tagged cell) compare in the int64 domain.
		ai, aok := ua.(int64)
		bi, bok := ub.(int64)
		return aok && bok && ai == bi
	}
	if !ta.Comparable() {
		return false
	}
	return ua == ub
}

// contextKey canonicalizes a context.WithValue key: the stored key must
// take the same canonical form the valueCtx lookup produces (the opaque
// store's key position) or equal script keys never hit. An unhashable
// key panics with gc's "key is not comparable" message rather than the
// hash-type error OpaqueKey reports.
func contextKey(k runtime.Value) (key any) {
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(*runtime.Panic); ok {
				panic(&runtime.Panic{Value: "key is not comparable"})
			}
			panic(r)
		}
	}()
	return runtime.OpaqueKey(k)
}

// asCtx resolves a script value to a host context.Context — bound
// context producers (Background, With*) all yield GoValue boxes, so the
// host value is already unwrapped by goNative upstream.
func asCtx(v any) (context.Context, error) {
	if v == nil {
		return nil, errors.New("context: nil parent")
	}
	if c, ok := v.(context.Context); ok {
		return c, nil
	}
	return nil, fmt.Errorf("not a context.Context: %T", v)
}

// watchCtxDone registers a cancel-family context's done channel in the
// parked-receive liveness registry: the channel closes only when a
// CancelFunc runs — a goroutine action, never an autonomous send — so a
// receive parked on it counts asleep, like gc's deadlock detection of
// `ctx, _ := context.WithCancel(bg); <-ctx.Done()`.
//
// A parent whose done channel can fire on its own propagates that
// liveness instead: a deadline parent's timer closes the child's done
// host-side without any script goroutine acting. When the parent's
// done is unmanaged or still live the child stays unmanaged (a
// conservative wake source); a nil or dead-managed parent passes the
// cancel-only property down, and the child registers as dead from
// birth.
func watchCtxDone(vc runtime.VMCaller, parent, child context.Context) {
	done := child.Done()
	if done == nil {
		return
	}
	if pdone := parent.Done(); pdone != nil {
		if managed, alive := vm.WakeChanState(vc, reflect.ValueOf(pdone).Pointer()); !managed || alive {
			return
		}
	}
	if ptr := vm.RegisterWakeChan(vc, reflect.ValueOf(done)); ptr != 0 {
		vm.SetWakeChanDead(vc, ptr, true)
	}
}

// net pointer/addr unwrappers: a script &net.TCPAddr{...} arrives at a
// host call boundary as a GoValue box, unboxed by goNative upstream;
// nil stays a nil pointer like Go's optional-addr arguments.
func tcpAddrOf(v any) *net.TCPAddr    { t, _ := v.(*net.TCPAddr); return t }
func udpAddrOf(v any) *net.UDPAddr    { t, _ := v.(*net.UDPAddr); return t }
func unixAddrOf(v any) *net.UnixAddr  { t, _ := v.(*net.UnixAddr); return t }
func ipAddrOf(v any) *net.IPAddr      { t, _ := v.(*net.IPAddr); return t }
func netIfaceOf(v any) *net.Interface { t, _ := v.(*net.Interface); return t }
func netFileOf(v any) *os.File {
	// the script-visible os.Stdout is a facade: unwrap to the real file.
	if s, ok := v.(*engineStdout); ok {
		return s.File
	}
	t, _ := v.(*os.File)
	return t
}
func netipAddrOf(v any) netip.Addr    { t, _ := v.(netip.Addr); return t }
func addrPortOf(v any) netip.AddrPort { t, _ := v.(netip.AddrPort); return t }

// byteSlice4/byteSlice16 read a script [4]byte/[16]byte (or []byte) into
// the fixed-size array netip's AddrFrom* take.
func byteSlice4(v any) (out [4]byte) {
	for i, b := range byteSlice(v) {
		if i >= 4 {
			break
		}
		out[i] = b
	}
	return out
}

func byteSlice16(v any) (out [16]byte) {
	for i, b := range byteSlice(v) {
		if i >= 16 {
			break
		}
		out[i] = b
	}
	return out
}

// atomicOpKind selects which of the pointer-taking sync/atomic function
// families an atomicOp builds.
type atomicOpKind int

const (
	atomicOpLoad atomicOpKind = iota
	atomicOpStore
	atomicOpAdd
	atomicOpAnd
	atomicOpOr
	atomicOpSwap
	atomicOpCAS
)

// atomicOp builds one of the bound sync/atomic *T-pointer functions.
// A script &x has no host address, so the builtins load and store the
// pointed-to cell through Deref/SetRef; arithmetic happens in the int64
// domain. Load* returns the cell content verbatim — a Named uint32
// keeps its tag, LoadPointer hands the stored pointer back untyped.
func atomicOp(name string, op atomicOpKind) *runtime.BuiltinFunc {
	want := 1
	switch op {
	case atomicOpStore, atomicOpAdd, atomicOpAnd, atomicOpOr, atomicOpSwap:
		want = 2
	case atomicOpCAS:
		want = 3
	}
	return &runtime.BuiltinFunc{Name: name, Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != want {
			return nil, fmt.Errorf("%s needs %d args, got %d", name, want, len(args))
		}
		cur, ok := runtime.Deref(args[0])
		if !ok {
			return nil, fmt.Errorf("%s needs a pointer operand, got %T", name, args[0])
		}
		set := func(v runtime.Value) error {
			if !runtime.SetRef(args[0], v) {
				return fmt.Errorf("%s needs a pointer operand, got %T", name, args[0])
			}
			return nil
		}
		switch op {
		case atomicOpLoad:
			return cur, nil
		case atomicOpStore:
			return runtime.NIL, set(args[1])
		case atomicOpSwap:
			if err := set(args[1]); err != nil {
				return nil, err
			}
			return cur, nil
		case atomicOpCAS:
			if !atomicCellEq(cur, args[1]) {
				return false, nil
			}
			if err := set(args[2]); err != nil {
				return nil, err
			}
			return true, nil
		}
		n := int64Of(cur)
		if op == atomicOpAdd {
			n += int64Of(args[1])
			if err := set(n); err != nil {
				return nil, err
			}
			// Add returns the NEW value.
			return n, nil
		}
		switch op {
		case atomicOpAnd:
			n &= int64Of(args[1])
		case atomicOpOr:
			n |= int64Of(args[1])
		}
		if err := set(n); err != nil {
			return nil, err
		}
		// And/Or return the OLD value.
		return cur, nil
	}}
}

// ---- value marshalling ----

// hostHelpers builds BuiltinFuncs whose Fn marshals arguments to Go natives
// and results back to runtime values.
type hostHelpers struct {
	e *Engine // for the configured output writer
}

// engineStdout is the script-visible os.Stdout: a *os.File facade whose
// writes forward to the engine's output (WithOutput) lazily, so a host
// callee receiving os.Stdout — template.Execute, fmt.Fprint's target —
// lands in the same stream the fmt intrinsics print to. The embedded
// *os.File promotes the rest of the file API (Name, Fd, Stat, Sync, the
// deadline family, Read/ReadAt/Seek, ...) onto the reflect method set,
// so member access keeps working like Go's.
type engineStdout struct {
	*os.File
	h *hostHelpers
}

func (w *engineStdout) Write(p []byte) (int, error) { return w.h.out().Write(p) }

func (w *engineStdout) WriteString(s string) (int, error) {
	if sw, ok := w.h.out().(io.StringWriter); ok {
		return sw.WriteString(s)
	}
	return w.h.out().Write([]byte(s))
}

// ReadFrom keeps io.Copy(os.Stdout, r) inside the engine's output: the
// promoted (*os.File).ReadFrom would splice straight to the process's
// fd 1 and escape WithOutput the way a bare os.Stdout did.
func (w *engineStdout) ReadFrom(r io.Reader) (int64, error) {
	return io.Copy(w.h.out(), r)
}

// out returns the engine's output writer (io.Discard when unset).
func (h *hostHelpers) out() io.Writer {
	if h.e == nil || h.e.out == nil {
		return io.Discard
	}
	return h.e.out
}

func (h *hostHelpers) fn(name string, f func([]any) (any, error), target ...any) *runtime.BuiltinFunc {
	return h.fnvc(name, func(_ runtime.VMCaller, a []any) (any, error) { return f(a) }, target...)
}

// fnvc is fn for bindings that need the caller (e.g. deadlock-wait
// bookkeeping through the channel a host call hands to the script).
func (h *hostHelpers) fnvc(name string, f func(runtime.VMCaller, []any) (any, error), target ...any) *runtime.BuiltinFunc {
	bf := &runtime.BuiltinFunc{Name: name, Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		a := make([]any, len(args))
		for i, v := range args {
			a[i] = goNative(v)
		}
		r, err := f(vc, a)
		if err != nil {
			return nil, err
		}
		return scriptVal(r), nil
	}}
	if len(target) > 0 {
		bf.Target = target[0]
	}
	return bf
}

// fn1/fn2/fn3 are arity-checked variants.
func (h *hostHelpers) fn1(name string, f func([]any) (any, error), target ...any) *runtime.BuiltinFunc {
	return h.arity(name, 1, f, target...)
}

func (h *hostHelpers) fn2(name string, f func([]any) (any, error), target ...any) *runtime.BuiltinFunc {
	return h.arity(name, 2, f, target...)
}

func (h *hostHelpers) fn3(name string, f func([]any) (any, error), target ...any) *runtime.BuiltinFunc {
	return h.arity(name, 3, f, target...)
}

func (h *hostHelpers) arity(name string, n int, f func([]any) (any, error), target ...any) *runtime.BuiltinFunc {
	bf := &runtime.BuiltinFunc{Name: name, Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) < n {
			return nil, fmt.Errorf("%s needs %d args, got %d", name, n, len(args))
		}
		a := make([]any, len(args))
		for i, v := range args {
			a[i] = goNative(v)
		}
		r, err := f(a)
		if err != nil {
			return nil, err
		}
		return scriptVal(r), nil
	}}
	if len(target) > 0 {
		bf.Target = target[0]
	}
	return bf
}

// scanFn adapts fmt's Scan family: the out-params arrive as script refs,
// which host fmt cannot store through, so each is mirrored into a host
// variable of the pointee's type, the real fmt call scans into those, and
// each result is written back through the ref. heads counts the leading
// non-pointer args (a format string or reader); headFn converts any
// prefix of them it needs to (nil: none) and scanFn goNatives the rest,
// so a converter like scanReader never has to know the arg count.
func (h *hostHelpers) scanFn(name string, heads int, headFn func(runtime.VMCaller, []runtime.Value) ([]any, error), target any, call func(head, ptrs []any) (int, error)) *runtime.BuiltinFunc {
	return &runtime.BuiltinFunc{Name: name, Target: target, Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) < heads {
			return nil, fmt.Errorf("%s needs at least %d args, got %d", name, heads, len(args))
		}
		var head []any
		if headFn != nil {
			var err error
			if head, err = headFn(v, args[:heads]); err != nil {
				return nil, err
			}
		}
		for i := len(head); i < heads; i++ {
			head = append(head, goNative(args[i]))
		}
		ptrs := make([]any, len(args)-heads)
		tds := make([]*runtime.TypeDef, len(args)-heads)
		for i, ref := range args[heads:] {
			cur, ok := runtime.Deref(ref)
			if !ok {
				return retErr(0, fmt.Errorf("%s: argument %d is not a pointer", name, i+heads+1))
			}
			// the host mirror erases the pointee's declared tag —
			// `var m MyInt; Sscan("5", &m)` — so capture it for the
			// write-back below.
			tds[i] = runtime.TagOf(cur)
			t := reflect.TypeOf(goNative(cur))
			if t == nil {
				return retErr(0, fmt.Errorf("%s: cannot infer scan target type: argument %d", name, i+heads+1))
			}
			rv := reflect.New(t)
			// Seed with the current pointee: gc leaves args the scan
			// never reached (or failed to store) untouched.
			rv.Elem().Set(reflect.ValueOf(goNative(cur)))
			ptrs[i] = rv.Interface()
		}
		n, err := call(head, ptrs)
		// gc stores whatever it managed to scan even on error, so the
		// write-back runs unconditionally.
		for i, ref := range args[heads:] {
			out := scriptVal(reflect.ValueOf(ptrs[i]).Elem().Interface())
			if tds[i] != nil {
				out = runtime.Tag(tds[i], out)
			}
			runtime.SetRef(ref, out)
		}
		return retErr(n, err)
	}}
}

// scanReader converts an Fscan family's leading reader arg, accepting
// both host io.Reader values and script-defined readers.
func (h *hostHelpers) scanReader(v runtime.VMCaller, args []runtime.Value) ([]any, error) {
	r, err := h.asReaderVM(v, args[0])
	if err != nil {
		return nil, err
	}
	return []any{r}, nil
}

// bytealgMaxLen mirrors the host build's arch-tuned init (internal/bytealg
// index_<arch>.go). amd64 is 63 with AVX2 else 31; without reading CPUID we
// take the conservative 31 — the bound only gates when IndexString may be
// used, and a smaller bound just sends longer needles to the generic path.
// arm64/ppc64x 32, s390x/loong64 64.
func bytealgMaxLen() int {
	switch goruntime.GOARCH {
	case "amd64":
		return 31
	case "arm64", "ppc64", "ppc64le":
		return 32
	case "s390x", "loong64":
		return 64
	}
	return 0
}

// bytealgMaxBruteForce mirrors the host build's arch-tuned constant
// (internal/bytealg/index_<arch>.go): amd64 and s390x brute-force up to
// 64, arm64/ppc64x/loong64 up to 16, generic 0.
func bytealgMaxBruteForce() int {
	switch goruntime.GOARCH {
	case "amd64", "s390x":
		return 64
	case "arm64", "ppc64", "ppc64le", "loong64":
		return 16
	}
	return 0
}

// sortInPlace sorts a *runtime.Slice's elements directly — going through
// goNative would sort a fresh copy and leave the script's slice untouched.
func (h *hostHelpers) sortInPlace(name string) *runtime.BuiltinFunc {
	return &runtime.BuiltinFunc{Name: name, Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != 1 {
			return nil, fmt.Errorf("%s needs 1 arg, got %d", name, len(args))
		}
		s := args[0]
		if n, ok := s.(*runtime.Named); ok {
			s = n.V
		}
		switch s := s.(type) {
		case *runtime.Slice:
			sortScript(s.Elems)
		case *runtime.TypedNil:
			// sorting a nil slice is a no-op in Go; nil pointers and
			// other typed nils still fail the reflect type check.
			if !nilSliceArg(s) {
				return nil, fmt.Errorf("%s: arg must be a slice, got %T", name, args[0])
			}
		default:
			return nil, fmt.Errorf("%s: arg must be a slice, got %T", name, args[0])
		}
		return runtime.NIL, nil
	}}
}

// sortScript orders int64/float64/string elements ascending — the element
// sets sort.Ints, sort.Strings, and slices.Sort support.
func sortScript(el []runtime.Value) {
	sort.Slice(el, func(i, j int) bool {
		bv := runtime.Unwrap(el[j])
		switch a := runtime.Unwrap(el[i]).(type) {
		case int64:
			if b, ok := bv.(int64); ok {
				return a < b
			}
		case float64:
			if b, ok := bv.(float64); ok {
				// sort.Float64s orders NaN before all other values
				return a < b || (math.IsNaN(a) && !math.IsNaN(b))
			}
		case string:
			if b, ok := bv.(string); ok {
				return a < b
			}
		}
		return false
	})
}

// sortSlice implements sort.Slice: the less function is a script callable.
// scriptElems exposes a slice argument's raw elements to Go-side helpers
// (the argument may arrive as a []any via goNative).
func scriptElems(v any) []runtime.Value {
	switch s := v.(type) {
	case *runtime.Slice:
		return s.Elems
	case []any:
		el := make([]runtime.Value, len(s))
		for i, x := range s {
			el[i] = scriptVal(x)
		}
		return el
	}
	return nil
}

// lessScript orders int64/float64/string (heterogeneous pairs rank by kind:
// numbers < strings < others, comparing numerically across int64/float64).
func lessScript(a, b runtime.Value) bool {
	a = runtime.Unwrap(a)
	b = runtime.Unwrap(b)
	an, aok := numOf(a)
	bn, bok := numOf(b)
	if aok && bok {
		// NaN orders before all other values, like cmp.Compare —
		// slices.Sort/IsSorted/Search share this ordering.
		return an < bn || (math.IsNaN(an) && !math.IsNaN(bn))
	}
	if as, ok := a.(string); ok {
		if bs, ok := b.(string); ok {
			return as < bs
		}
		return false // strings rank above numbers
	}
	if aok {
		return true
	}
	return false
}

func numOf(v runtime.Value) (float64, bool) {
	switch n := v.(type) {
	case *runtime.Named:
		return numOf(n.V)
	case *runtime.UConst:
		// a constant ranks as its default-typed numeric —
		// `slices.BinarySearch(s, 5)` compares target 5 as a number.
		return argFloat(n)
	case int64:
		return float64(n), true
	case float64:
		return n, true
	case time.Duration:
		return float64(n), true
	}
	return 0, false
}

// equalScript compares two script values by shape (identity-ish): used by
// maps.Equal where a deep compare is the closest available semantics.
func equalScript(a, b runtime.Value) bool {
	a = runtime.Unwrap(a)
	b = runtime.Unwrap(b)
	if an, ok := numOf(a); ok {
		bn, ok := numOf(b)
		return ok && an == bn
	}
	switch x := a.(type) {
	case string, bool:
		return x == b
	case *runtime.Slice:
		y, ok := b.(*runtime.Slice)
		if !ok || len(x.Elems) != len(y.Elems) {
			return false
		}
		for i := range x.Elems {
			if !equalScript(x.Elems[i], y.Elems[i]) {
				return false
			}
		}
		return true
	case *runtime.Map:
		y, ok := b.(*runtime.Map)
		if !ok || len(x.Pairs) != len(y.Pairs) {
			return false
		}
		for k, xv := range x.Pairs {
			yv, ok := y.LookupCanonical(k)
			if !ok || !equalScript(xv, yv) {
				return false
			}
		}
		return true
	}
	return a == b
}

// sortInterface adapts a script value's Len/Less/Swap members into a
// host sort.Interface for sort.Sort: the callbacks run on the calling
// VM (the usual host-callback hazard — results are only read during the
// call).
func (h *hostHelpers) sortInterface(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("sort.Sort needs 1 arg, got %d", len(args))
	}
	if ifc, ok := goNative(args[0]).(sort.Interface); ok {
		sort.Sort(ifc)
		return runtime.NIL, nil
	}
	lenFn, lok := runtime.IfaceMember(vc, args[0], "Len")
	lessFn, sok := runtime.IfaceMember(vc, args[0], "Less")
	swapFn, wok := runtime.IfaceMember(vc, args[0], "Swap")
	if !lok || !sok || !wok {
		return nil, fmt.Errorf("sort.Sort: %T does not implement sort.Interface", args[0])
	}
	sort.Sort(&scriptSortable{vc: vc, lenFn: lenFn, lessFn: lessFn, swapFn: swapFn})
	return runtime.NIL, nil
}

// scriptSortable is the sort.Interface view of a script object.
type scriptSortable struct {
	vc                    runtime.VMCaller
	lenFn, lessFn, swapFn runtime.Value
}

func (s *scriptSortable) Len() int {
	r, err := s.vc.Call(s.lenFn, nil)
	if err != nil {
		panic(err)
	}
	return int(int64Of(r))
}

func (s *scriptSortable) Less(i, j int) bool {
	r, err := s.vc.Call(s.lessFn, []runtime.Value{int64(i), int64(j)})
	if err != nil {
		panic(err)
	}
	b, _ := r.(bool)
	return b
}

func (s *scriptSortable) Swap(i, j int) {
	if _, err := s.vc.Call(s.swapFn, []runtime.Value{int64(i), int64(j)}); err != nil {
		panic(err)
	}
}

func (h *hostHelpers) sortSlice(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
	s, ok := sliceOf(args[0])
	if !ok {
		if nilSliceArg(args[0]) {
			return runtime.NIL, nil // sorting a nil slice is a no-op in Go
		}
		return nil, fmt.Errorf("sort.Slice: first arg must be a slice")
	}
	less := args[1]
	sort.Slice(s.Elems, func(i, j int) bool {
		r := callOrPanic(v, less, []runtime.Value{int64(i), int64(j)})
		b, _ := r.(bool)
		return b
	})
	return runtime.NIL, nil
}

// sortByCmpFunc implements slices.SortFunc and slices.SortStableFunc: cmp is
// a script callable returning negative/zero/positive. The sort is stable,
// like Go's implementation.
func (h *hostHelpers) sortByCmpFunc(name string) *runtime.BuiltinFunc {
	return &runtime.BuiltinFunc{Name: name, Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		s, ok := sliceOf(args[0])
		if !ok {
			if nilish(args[0]) {
				return args[0], nil // sorting a nil slice is a no-op
			}
			return nil, fmt.Errorf("%s: first arg must be a slice, got %T", name, args[0])
		}
		cmp := args[1]
		sort.SliceStable(s.Elems, func(i, j int) bool {
			r := callOrPanic(v, cmp, []runtime.Value{s.Elems[i], s.Elems[j]})
			n, _ := runtime.Unwrap(r).(int64)
			return n < 0
		})
		return runtime.NIL, nil
	}}
}

// unsafeSizeOf reports unsafe.Sizeof on amd64: the answer comes from the
// operand's DECLARED type — a struct's fields pad per their declared
// widths, an array's element type sizes it — never the runtime values
// slots happen to hold. Operands without a recoverable typedef (untyped
// literals, host boxes) fall back to the boxed representation's width.
func (e *Engine) unsafeSizeOf(v runtime.Value) int64 {
	if td := sizeDeclTyp(v); td != nil {
		return int64(minireflect.SizeOf(e.reflectHooks(), td))
	}
	switch x := v.(type) {
	case bool:
		return 1
	case int64, float64:
		return 8
	case complex128:
		return 16
	case string:
		return 16
	case *runtime.Slice:
		return 24
	case *runtime.Map, *runtime.Chan, *runtime.Cell:
		return 8
	case *runtime.GoValue:
		// a host box knows its real size — complex128 materializes
		// to a GoValue, pointer-shaped boxes report 8.
		if t := reflect.TypeOf(x.V); t != nil {
			return int64(t.Size())
		}
		return 8
	case *runtime.UConst:
		if mv, err := runtime.UConstNative(x); err == nil && mv != x {
			return e.unsafeSizeOf(mv)
		}
		return 8
	case runtime.Nil, *runtime.IfaceNil:
		return 16 // interface pair
	default:
		return 8 // pointer-sized boxes
	}
}

// sizeDeclTyp recovers the operand's declared typedef for
// unsafe.Sizeof/Alignof — the layout unsafe.Offsetof already computes
// from declared field types. A value without a stamped typedef (an
// untyped literal, a host box) reports nil and the caller falls back
// to runtime-value widths.
func sizeDeclTyp(v runtime.Value) *runtime.TypeDef {
	switch x := v.(type) {
	case *runtime.Struct:
		return x.Def
	case *runtime.Named:
		if x.Typ != nil {
			return x.Typ
		}
	case *runtime.Slice:
		return x.Typ
	case *runtime.Map:
		return x.Typ
	case *runtime.Chan:
		return x.Typ
	case *runtime.Cell:
		if x.Typ != nil {
			return x.Typ
		}
		return sizeDeclTyp(x.Elem)
	case *runtime.TypedNil:
		return x.Typ
	case *runtime.IfaceNil:
		return x.Typ
	}
	return nil
}

func (e *Engine) unsafeAlignOf(v runtime.Value) int64 {
	if td := sizeDeclTyp(v); td != nil {
		return int64(minireflect.AlignOf(e.reflectHooks(), td))
	}
	if n := e.unsafeSizeOf(v); n < 8 {
		if n < 1 {
			return 1 // alignment is always at least 1
		}
		return n
	}
	return 8
}

// unsafeStringArg reads a string-valued builtin argument — the field
// name the compiler emits for unsafe.Offsetof(s.f). Named and
// cell-boxed strings unwrap like they do everywhere else.
func unsafeStringArg(v runtime.Value) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case *runtime.UConst:
		if x.V != nil && x.V.Kind() == constant.String {
			return constant.StringVal(x.V), true
		}
	case *runtime.Named:
		return unsafeStringArg(x.V)
	case *runtime.Cell:
		return unsafeStringArg(x.Elem)
	}
	return "", false
}

// unsafeFieldOffset reports the byte offset of a named field in a
// struct base, laid out by the fields' DECLARED types — minireflect's
// fieldOffset machinery, shared with reflect's StructField.Offset.
// (Computing offsets from the runtime values fields happen to hold
// misplaces every later field: an `any` holding an int64 read as 8
// where the declared interface pair is 16.) A host-boxed struct answers
// through reflect's real field offset instead.
func (e *Engine) unsafeFieldOffset(base runtime.Value, name string) (int64, error) {
	switch x := base.(type) {
	case *runtime.Cell:
		return e.unsafeFieldOffset(x.Elem, name)
	case *runtime.Named:
		return e.unsafeFieldOffset(x.V, name)
	case *runtime.Struct:
		if x.Def == nil {
			break
		}
		off, err := minireflect.OffsetOf(e.reflectHooks(), x.Def, name)
		if err != nil {
			return 0, err
		}
		return int64(off), nil
	case *runtime.GoValue:
		rv := reflect.ValueOf(x.V)
		for rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Interface {
			rv = rv.Elem()
		}
		if rv.IsValid() && rv.Kind() == reflect.Struct {
			if sf, ok := rv.Type().FieldByName(name); ok {
				return int64(sf.Offset), nil
			}
			return 0, fmt.Errorf("unsafe.Offsetof: %s has no field %s", rv.Type(), name)
		}
	}
	return 0, fmt.Errorf("unsafe.Offsetof is not supported for %T", base)
}

// retErr wraps a (n int, err error) or single-value+error result into the
// script-visible tuple (value, err-or-nil).
func retErr(n int, err error) (any, error) {
	return &runtime.Tuple{Elems: []runtime.Value{int64(n), errVal(err)}}, nil
}

func retErr2[T any](v T, err error) (any, error) {
	return &runtime.Tuple{Elems: []runtime.Value{scriptVal(v), errVal(err)}}, nil
}

func errVal(err error) runtime.Value {
	if err == nil {
		return runtime.NIL
	}
	return &runtime.GoValue{V: err} // boxed: method calls (Error(), Unwrap()) dispatch via reflection
}

// namedSized boxes a host-sized int with its declared typedef so %T
// spells int8/int16/int32/int64/uintN like Go (rune is int32).
func namedSized(x any, v int64) runtime.Value {
	return runtime.Tag(runtime.BasicTypedef(fmt.Sprintf("%T", x)), v)
}

// scriptVal converts a Go-native result back to a runtime value. Concrete
// runtime types pass through; anything else (errors, host structs) is boxed
// as a GoValue — Value is `any`, so it cannot be a type-switch case itself.
func scriptVal(v any) runtime.Value {
	switch x := v.(type) {
	case nil:
		return runtime.NIL
	case bool, string, float64:
		return x
	case int64:
		// int64 is a named type in Go — distinct from int at assert time —
		// so host int64 results ride in the box like the other sized ints.
		return namedSized(x, x)
	case int:
		return int64(x)
	case int8, int16, int32:
		return namedSized(x, reflect.ValueOf(x).Int())
	case uint, uint8, uint16, uint32, uint64, uintptr:
		// int64(x) keeps the full bit pattern — a >=2^63 uint64 like
		// math.Float64bits(-0.0) reinterprets losslessly.
		return namedSized(x, int64(reflect.ValueOf(x).Uint()))
	case float32:
		// keep the declared width like a float32(x) conversion does —
		// equality and map keys need the float32 tag, the payload rides
		// in the float64 domain.
		return runtime.Tag(runtime.BasicTypedef("float32"), float64(x))
	case []byte:
		if x == nil {
			return &runtime.TypedNil{Typ: anonSliceTyp("byte")}
		}
		// a []byte result unmarshals to byte-tagged elements so
		// `string(b)` and indexing behave like Go source suggests, and
		// elements compare like a []byte{...} literal's.
		el := make([]runtime.Value, len(x))
		for i, b := range x {
			el[i] = runtime.Tag(runtime.BasicTypedef("byte"), int64(b))
		}
		return &runtime.Slice{Elems: el, Typ: anonSliceTyp("byte")}
	case []string:
		return strsSlice(x)
	case time.Duration:
		// durations stay raw host values: methods (.Hours(), .String())
		// dispatch through reflection and binaryOp unwraps for arithmetic.
		return x
	case []any:
		if x == nil {
			return &runtime.TypedNil{Typ: anonSliceTyp("any")}
		}
		el := make([]runtime.Value, len(x))
		for i, e := range x {
			el[i] = scriptVal(e)
		}
		return &runtime.Slice{Elems: el, Typ: anonSliceTyp("any")}
	case map[any]any:
		if x == nil {
			return &runtime.TypedNil{Typ: &runtime.TypeDef{Kind: runtime.KindMap,
				Anon: &ast.MapType{Key: ast.NewIdent("any"), Value: ast.NewIdent("any")}}}
		}
		m := &runtime.Map{Pairs: map[runtime.Value]runtime.Value{}}
		for k, vv := range x {
			m.Insert(scriptVal(k), scriptVal(vv))
		}
		return m
	case runtime.Nil, *runtime.Tuple, *runtime.Cell, *runtime.Slice,
		*runtime.Map, *runtime.Struct, *runtime.Function, *runtime.Closure,
		*runtime.BoundMethod, *runtime.BuiltinFunc, *runtime.GoValue,
		*runtime.Chan, *runtime.TypeDef, *runtime.Iterator, *runtime.Package,
		*runtime.ImportRef, *runtime.Named, *runtime.TypedNil:
		return x
	default:
		return &runtime.GoValue{V: x}
	}
}

// goNative converts a runtime value to its Go-native counterpart for host
// calls: cells unwrap, slices/maps become []any / map[any]any, structs get a
// Stringer view so fmt prints them sensibly.
// float32Tag reports whether a Named tag's underlying type is float32 —
// the one builtin where a float64 box and the declared width disagree in
// formatting. `type F32 float32` tags count too (Anon names the builtin).
func float32Tag(td *runtime.TypeDef) bool {
	return runtime.BasicNameOf(td) == "float32"
}

// complex64Tag mirrors float32Tag for complex64 — the payload rides the
// float64/complex128 domain but formats at complex64 width per part.
func complex64Tag(td *runtime.TypeDef) bool {
	return runtime.BasicNameOf(td) == "complex64"
}

// complex64Value reads a payload as complex64 — either a complex128 box
// or the constant domain's complex literal.
func complex64Value(v runtime.Value) (complex64, bool) {
	switch pv := v.(type) {
	case complex128:
		return complex64(pv), true
	case *runtime.UConst:
		if nv, err := uconstNative(pv); err == nil {
			if cv, ok := nv.(complex128); ok {
				return complex64(cv), true
			}
		}
	}
	return 0, false
}

// uconstNative materializes an untyped constant to its host default —
// the same rule the VM applies when the constant crosses a value
// boundary. An overflowing constant reports like Go's compile error.
func uconstNative(u *runtime.UConst) (any, error) {
	return runtime.UConstNative(u)
}

// mustNativeConst is uconstNative for a builtin argument position that
// cannot report the error: it panics with Go's compile-error wording.
func mustNativeConst(u *runtime.UConst) any {
	nv, err := uconstNative(u)
	if err != nil {
		panic(&runtime.Panic{Value: &runtime.RuntimeError{Msg: err.Error()}})
	}
	return nv
}

func goNative(v runtime.Value) any {
	switch x := v.(type) {
	case runtime.Nil:
		return nil
	case *runtime.IfaceNil:
		// a nil interface value crosses to the host as nil (a typed
		// nil stays a TypedNil — the host has no box for it).
		return nil
	case *runtime.UConst:
		return mustNativeConst(x)
	case *runtime.Named:
		// a float32-tagged value crosses to fmt as a real float32 so
		// %v applies float32 formatting (0.1, not 0.10000000149011612);
		// %T is rewritten to scriptTypeString before this runs.
		if float32Tag(x.Typ) {
			if fv, ok := x.V.(float64); ok {
				return float32(fv)
			}
			// the payload may still ride the constant domain — a
			// `float32(c)` conversion keeps UConst so the value stays
			// arbitrary-precision; read it at float32 width the same
			// way the materialized float64 path above does.
			if uc, ok := x.V.(*runtime.UConst); ok {
				if nv, err := uconstNative(uc); err == nil {
					if fv, ok := nv.(float64); ok {
						return float32(fv)
					}
				}
			}
		}
		// a complex64-tagged value narrows each part to float32 — the
		// constant domain's widened digits must not reach the host.
		if complex64Tag(x.Typ) {
			if cv, ok := complex64Value(x.V); ok {
				return cv
			}
		}
		// a bound host scalar crossing to a host `any` parameter
		// materializes to the host type — a host call sees a real
		// time.Month, not the int64 or UConst the script shape keeps.
		if x.Typ != nil && x.Typ.HostScalar != nil {
			if hv, ok := runtime.HostScalarOf(x.Typ, x.V); ok {
				return hv
			}
		}
		return goNative(x.V)
	case *runtime.Cell:
		return goNative(x.Elem)
	case *runtime.Slice:
		out := make([]any, len(x.Elems))
		for i, e := range x.Elems {
			out[i] = goNative(e)
		}
		return out
	case *runtime.Map:
		out := make(map[any]any, x.Len())
		for i := 0; i < x.Len(); i++ {
			k, e := x.At(i)
			out[goNative(k)] = goNative(e)
		}
		return out
	case *runtime.Struct:
		return fmt.Sprintf("%s%+v", x.Def.Name, x.Fields)
	case *runtime.GoValue:
		return x.V
	default:
		return v
	}
}

// asErr coerces a marshalled script value to error for errors.* calls:
// GoValue-boxed errors unwrap to natives via goNative already, so v is
// either an error, nil, or a value that formats to one.
func asErr(v any) error {
	switch e := v.(type) {
	case nil:
		return nil
	case error:
		return e
	default:
		return fmt.Errorf("%v", v)
	}
}

func str(v any) string {
	if n, ok := v.(*runtime.Named); ok {
		return str(n.V)
	}
	if u, ok := v.(*runtime.UConst); ok {
		// a builtin argument position materializes an untyped
		// constant at its default type — the same fold intOf does.
		return str(mustNativeConst(u))
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func intOf(v any) int {
	switch x := v.(type) {
	case *runtime.Named:
		return intOf(x.V)
	case *runtime.UConst:
		// a named constant stays a UConst through declaration storage;
		// a builtin argument position materializes it at its default
		// type — Go folds these calls at compile time.
		return intOf(mustNativeConst(x))
	case int64:
		return int(x)
	case int:
		return x
	case float64:
		return int(x)
	case uint64:
		return int(x)
	case uint, uint8, uint16, uint32, uintptr:
		return int(reflect.ValueOf(x).Uint())
	case time.Duration:
		return int(x)
	case *runtime.GoValue:
		return intOf(x.V)
	}
	// a defined numeric type the switch above missed (e.g. fs.FileMode
	// from a host call's result) still carries its integer value — Go
	// would fold passing it to an int-typed parameter at compile time.
	switch rv := reflect.ValueOf(v); rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return int(rv.Int())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return int(rv.Uint())
	}
	return 0
}

func int64Of(v any) int64 { return int64(intOf(v)) }

func durOf(v any) time.Duration { return time.Duration(int64Of(v)) }

// fsOp2 applies a one-path os operation after resolving the script path
// through the virtual cwd + AllowedRoots check; the result goes back as a
// Go-style (value, err) tuple.
func fsOp2[T any](e *Engine, name string, a []any, op func(string) (T, error)) (any, error) {
	p, err := e.fsPath(str(a[0]))
	if err != nil {
		return nil, err
	}
	return retErr2(op(p))
}

// fsErrOp is fsOp2 for error-only results.
func fsErrOp(e *Engine, name string, a []any, op func(string) error) (any, error) {
	p, err := e.fsPath(str(a[0]))
	if err != nil {
		return nil, err
	}
	return errVal(op(p)), nil
}

// fsTempDir resolves the dir argument of MkdirTemp/CreateTemp: an empty
// dir means os.TempDir() — only allowed when the engine is unrestricted
// (a temp dir outside the roots could otherwise anchor escaped writes).
func (e *Engine) fsTempDir(dir string) (string, error) {
	if dir == "" {
		if len(e.cfg.AllowedRoots) == 0 {
			return "", nil
		}
		return "", errors.New("dir must name a directory inside the allowed roots")
	}
	return e.fsPath(dir)
}

// cwdAbs anchors a script path at the virtual cwd without a roots check —
// for pure path math (filepath.Abs/Rel) where no filesystem is touched.
func (e *Engine) cwdAbs(p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(e.cwd, p)
}

// borrowBytes marshals a script value to []byte for a host call that
// writes into the buffer: the returned commit writes the bytes back
// into the script slice's elements. It must see the arg before goNative
// flattens it — an []any has lost the script slice it came from. Values
// already sharing their backing (a host []byte inside a GoValue) need
// no commit; read-only consumers keep using byteSlice.
func borrowBytes(v runtime.Value) ([]byte, func()) {
	u := v
	for {
		if n, ok := u.(*runtime.Named); ok {
			u = n.V
			continue
		}
		if dv, ok := runtime.Deref(u); ok {
			u = dv
			continue
		}
		break
	}
	switch x := u.(type) {
	case *runtime.Slice:
		bs := byteSlice(x)
		return bs, func() {
			td := runtime.BasicTypedef("byte")
			for i := range bs {
				x.Elems[i] = runtime.Tag(td, int64(bs[i]))
			}
		}
	case *runtime.GoValue:
		if bs, ok := x.V.([]byte); ok {
			// shared backing — host writes are already visible
			return bs, func() {}
		}
	}
	return byteSlice(v), func() {}
}

// unsafeBytes copies str into a script []byte backing for StringData.
func unsafeBytes(str string) *runtime.Slice {
	el := make([]runtime.Value, len(str))
	for i := 0; i < len(str); i++ {
		el[i] = runtime.Tag(runtime.BasicTypedef("byte"), int64(str[i]))
	}
	return &runtime.Slice{Elems: el, Typ: &runtime.TypeDef{Kind: runtime.KindSlice, Anon: &ast.ArrayType{Elt: ast.NewIdent("byte")}}}
}

// unsafeElems resolves unsafe.String/Slice's (ptr, len) to the n
// elements starting at an &s[i] ref, sharing the slice's backing.
// sliceTypFromPtr maps a pointer/array/slice typedef to the unnamed
// []E typedef unsafe.Slice's result carries — never the named slice or
// the [N]E array the pointer was taken from.
func sliceTypFromPtr(td *runtime.TypeDef) *runtime.TypeDef {
	if td == nil {
		return nil
	}
	switch x := typedefAst(td).(type) {
	case *ast.StarExpr:
		return &runtime.TypeDef{Kind: runtime.KindSlice, Anon: &ast.ArrayType{Elt: x.X}, Elem: td.Elem, Pkg: td.Pkg, File: td.File}
	case *ast.ArrayType:
		return &runtime.TypeDef{Kind: runtime.KindSlice, Anon: &ast.ArrayType{Elt: x.Elt}, Elem: td.Elem, Pkg: td.Pkg, File: td.File}
	}
	return td
}

func unsafeElems(p, nv runtime.Value) ([]runtime.Value, error) {
	n := int(int64Of(goNative(nv)))
	if p == nil || p == runtime.NIL {
		if n == 0 {
			return nil, nil
		}
		return nil, errors.New("unsafe: ptr is nil and len is not zero")
	}
	if tn, ok := runtime.Unwrap(p).(*runtime.TypedNil); ok {
		// a typed nil pointer follows the same nil rule — len 0 is a
		// legal no-op, anything else panics like an untyped nil.
		if tn.Typ != nil && tn.Typ.Kind == runtime.KindPointer {
			if n == 0 {
				return nil, nil
			}
			return nil, errors.New("unsafe: ptr is nil and len is not zero")
		}
		return nil, fmt.Errorf("unsafe: unsupported pointer %T (only element pointers)", p)
	}
	ref, ok := runtime.Unwrap(p).(*runtime.IndexRef)
	if !ok {
		return nil, fmt.Errorf("unsafe: unsupported pointer %T (only element pointers)", p)
	}
	sl := ref.Slice()
	i, _ := runtime.Unwrap(ref.Key).(int64)
	if sl == nil || int(i)+n > cap(sl.Elems) {
		return nil, errors.New("unsafe: pointer range out of bounds")
	}
	return sl.Elems[i : int(i)+n : int(i)+n], nil
}

// byteSlice unmarshals a script value to []byte for os.WriteFile & co:
// accepts strings, []byte natives and int64 element slices.
func byteSlice(v any) []byte {
	if n, ok := v.(*runtime.Named); ok {
		return byteSlice(n.V)
	}
	switch x := v.(type) {
	case string:
		return []byte(x)
	case []byte:
		return x
	case []any:
		out := make([]byte, len(x))
		for i, e := range x {
			out[i] = byte(int64Of(e))
		}
		return out
	case *runtime.Slice:
		out := make([]byte, len(x.Elems))
		for i, e := range x.Elems {
			out[i] = byte(int64Of(goNative(e)))
		}
		return out
	}
	return nil
}

func strSlice(v any) []string {
	if n, ok := v.(*runtime.Named); ok {
		return strSlice(n.V)
	}
	if s, ok := v.(*runtime.Slice); ok {
		out := make([]string, len(s.Elems))
		for i, e := range s.Elems {
			out[i] = str(goNative(e))
		}
		return out
	}
	if a, ok := v.([]any); ok {
		out := make([]string, len(a))
		for i, e := range a {
			out[i] = str(e)
		}
		return out
	}
	return nil
}

// nilSliceArg reports whether v is a typed nil whose type is a slice —
// sort's slice functions accept a nil slice (a no-op / sorted) but
// still reject nil pointers, maps, chans like Go's reflect-based check.
func nilSliceArg(v runtime.Value) bool {
	tn, ok := runtime.Unwrap(v).(*runtime.TypedNil)
	return ok && tn.Typ != nil && tn.Typ.Kind == runtime.KindSlice && !isArrayTyp(tn.Typ)
}

// sliceOf unwraps a script slice (cell/pointer derefed, Named-peeled,
// or bare); typed nils and non-slices report not-ok so callers can give
// their own error.
func sliceOf(v runtime.Value) (*runtime.Slice, bool) {
	if d, ok := runtime.Deref(v); ok {
		v = d
	}
	s, ok := runtime.Unwrap(v).(*runtime.Slice)
	return s, ok
}

// packSlice moves kept elements to the front of s's backing array and
// zeroes the vacated tail, then returns the len(kept) view sharing it —
// the same update Go's slices.DeleteFunc performs, so slices re-sliced
// from the original observe the deletion.
func (h *hostHelpers) packSlice(s *runtime.Slice, kept []runtime.Value) *runtime.Slice {
	copy(s.Elems, kept)
	var etd *runtime.TypeDef
	if s.Typ != nil && h.e != nil {
		etd, _ = h.e.elemOf(s.Typ)
	}
	for i := len(kept); i < len(s.Elems); i++ {
		s.Elems[i] = runtime.Zero(etd)
	}
	return &runtime.Slice{Elems: s.Elems[:len(kept)], Typ: s.Typ}
}

// seqElems collects a seq argument's elements: a *runtime.Slice reads
// verbatim, and an iter.Seq-shaped callable (seqOf's BuiltinFunc, or a
// script func used as a seq) is driven with a yield that gathers each
// element — the seq stops when yield reports false.
func seqElems(vc runtime.VMCaller, v runtime.Value) ([]runtime.Value, error) {
	if s, ok := sliceOf(v); ok {
		return append([]runtime.Value{}, s.Elems...), nil
	}
	var out []runtime.Value
	yield := &runtime.BuiltinFunc{Name: "seq yield", Fn: func(_ runtime.VMCaller, ya []runtime.Value) (runtime.Value, error) {
		out = append(out, ya...)
		return true, nil
	}}
	if _, err := vc.Call(v, []runtime.Value{yield}); err != nil {
		return nil, err
	}
	return out, nil
}

// strPieces boxes string pieces as script values for seqOf.
func strPieces(ss []string) []runtime.Value {
	out := make([]runtime.Value, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// seqOf builds an iter.Seq-shaped value: a callable the range-over-func
// loop drives through its yield builtin — the producer runs once and the
// loop body executes inside each yield call, so early exit just reports
// false back. Pieces are materialized up front; laziness is unobservable
// from a script.
func seqOf(name string, pieces []runtime.Value) runtime.Value {
	return &runtime.BuiltinFunc{Name: "iter.Seq(" + name + ")", Fn: func(vc runtime.VMCaller, ya []runtime.Value) (runtime.Value, error) {
		if len(ya) != 1 {
			return nil, fmt.Errorf("%s: Seq needs a yield func", name)
		}
		for _, p := range pieces {
			r, err := vc.Call(ya[0], []runtime.Value{p})
			if err != nil {
				return nil, err
			}
			if goon, ok := r.(bool); ok && !goon {
				return false, nil
			}
		}
		return true, nil
	}}
}

// nilish reports whether a value carries no concrete content — bare nil,
// a typed nil, or a named wrapper around one.
func nilish(v runtime.Value) bool {
	switch runtime.Unwrap(v).(type) {
	case runtime.Nil, *runtime.TypedNil, *runtime.IfaceNil:
		return true
	}
	return false
}

// sliceTypOf keeps a bound slice op's declared element type — a new
// []string built without the tag loses `[]string` reads downstream.
func sliceTypOf(v any) *runtime.TypeDef {
	if s, ok := runtime.Unwrap(v).(*runtime.Slice); ok {
		return s.Typ
	}
	return nil
}

func anySlice(v any) []any {
	if n, ok := v.(*runtime.Named); ok {
		return anySlice(n.V)
	}
	if s, ok := v.(*runtime.Slice); ok {
		out := make([]any, len(s.Elems))
		for i, e := range s.Elems {
			out[i] = goNative(e)
		}
		return out
	}
	if a, ok := v.([]any); ok {
		return a
	}
	return nil
}

func strsSlice(ss []string) runtime.Value {
	if ss == nil {
		// a nil []string result stays nil — SplitN(s, sep, 0) == nil.
		return &runtime.TypedNil{Typ: anonSliceTyp("string")}
	}
	el := make([]runtime.Value, len(ss))
	for i, s := range ss {
		el[i] = s
	}
	return &runtime.Slice{Elems: el, Typ: anonSliceTyp("string")}
}

// anonSliceTyp builds the anonymous []name typedef used to tag slices
// unboxed from host values (no package context — the name is a builtin).
func anonSliceTyp(name string) *runtime.TypeDef {
	return &runtime.TypeDef{Kind: runtime.KindSlice, Anon: &ast.ArrayType{Elt: ast.NewIdent(name)}}
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

// runeSlice lifts a []rune result into a script slice of rune-tagged
// int64s — the same element representation a []rune{...} literal makes.
func runeSlice(rs []rune) any {
	if rs == nil {
		return &runtime.TypedNil{Typ: anonSliceTyp("rune")}
	}
	out := make([]runtime.Value, len(rs))
	for i, r := range rs {
		out[i] = runtime.Tag(runtime.BasicTypedef("rune"), int64(r))
	}
	return &runtime.Slice{Elems: out, Typ: anonSliceTyp("rune")}
}

func floatOf(v any) float64 {
	switch x := v.(type) {
	case *runtime.Named:
		return floatOf(x.V)
	case float64:
		return x
	case float32:
		return float64(x)
	case int64:
		return float64(x)
	case int:
		return float64(x)
	}
	return 0
}

func runeOf(v any) rune { return rune(intOf(v)) }

// strArgs marshals trailing varargs (each already a script value) to strings.
func strArgs(a []any) []string {
	out := make([]string, len(a))
	for i, v := range a {
		out[i] = str(v)
	}
	return out
}

// callOrPanic invokes a script callback from inside a host call and
// re-raises any failure as a Go panic: v.Call delivers a script panic
// (or trap) as an error across its boundary, but returning that as the
// builtin's error would turn a recoverable panic into a runtime trap —
// in Go a failure inside strings.Map's callback just propagates as a
// panic, and a trap or process exit keeps its own kind through
// asScriptPanic.
func callOrPanic(v runtime.VMCaller, fn runtime.Value, args []runtime.Value) runtime.Value {
	r, err := v.Call(fn, args)
	if err != nil {
		panic(err)
	}
	return r
}

// runePred adapts a script `func(rune) bool` to the host signature for
// strings.*Func calls; a panic inside the callback propagates through
// the host call like Go's.
func runePred(v runtime.VMCaller, fn runtime.Value) func(rune) bool {
	return func(r rune) bool {
		res := callOrPanic(v, fn, []runtime.Value{int64(r)})
		b, _ := res.(bool)
		return b
	}
}

// asWriter pulls an io.Writer out of a bound stdio handle (os.Stdout,
// os.Stderr, an *os.File) for the fmt.Fprint* family.
func asWriter(v any) (io.Writer, error) {
	return asWriterVM(nil, v)
}

// asWriterVM is asWriter plus script-defined writers: when the VM is
// available and the value carries a Write method, it adapts into an
// io.Writer that calls back into the script.
func asWriterVM(vc runtime.VMCaller, v any) (io.Writer, error) {
	// args arrive fmtArg'd: a *runtime.Cell (a `&b` address-of) surfaces
	// as *fmtValue — unwrap back through the reference to the box.
	if fv, ok := v.(*fmtValue); ok {
		v = fv.x
	}
	// the method-set check runs on the box (&w keeps its pointer
	// receivers); the Write callback itself resolves on the unwrapped
	// value like before.
	orig := v
	if dv, ok := runtime.Deref(orig); ok {
		v = dv
	}
	if g, ok := v.(*runtime.GoValue); ok {
		v = g.V
	}
	if w, ok := v.(io.Writer); ok {
		return w, nil
	}
	if vc != nil && v != nil && v != runtime.NIL {
		if m, ok := runtime.IfaceMember(vc, orig, "Write"); ok && m != nil && m != runtime.NIL {
			return &scriptWriter{v: vc, write: m}, nil
		}
	}
	return nil, fmt.Errorf("not an io.Writer: %T", v)
}

// asReaderVM is asReader plus script-defined readers: when the VM is
// available, a script value advertising a Read method adapts to io.Reader
// through a callback proxy, like asWriterVM does for writers. When the
// value also advertises WriteTo the proxy implements it too — Go's
// NopCloser picks its concrete type off that interface, so the probe
// keeps `io.nopCloserWriterTo` vs `io.nopCloser` faithful. Advertisement
// is checked on the method set, not member selection: a method promoted
// from an embedded interface (net/http's nopCloser type probes) has no
// selectable member yet still declares the interface.
func (h *hostHelpers) asReaderVM(vc runtime.VMCaller, v any) (io.Reader, error) {
	if fv, ok := v.(*fmtValue); ok {
		v = fv.x
	}
	if v == nil || v == runtime.NIL {
		return nil, nil
	}
	// the method-set check runs on the box (&r keeps its pointer
	// receivers in the set, a bare value loses them — Go's rule for
	// io.Reader); the Read callback binds through the same box.
	orig := v
	if dv, ok := runtime.Deref(orig); ok {
		v = dv
	}
	if g, ok := v.(*runtime.GoValue); ok {
		v = g.V
	}
	if r, ok := v.(io.Reader); ok {
		return r, nil
	}
	if vc != nil && v != nil && v != runtime.NIL {
		set, _, _ := h.e.methodSetOfValue(orig)
		if set["Read"] {
			if set["WriteTo"] {
				return &scriptReaderWriterTo{v: vc, recv: orig}, nil
			}
			return &scriptReader{v: vc, recv: orig}, nil
		}
	}
	return nil, fmt.Errorf("not an io.Reader: %T", v)
}

// scriptReader adapts a script-side Read method to io.Reader so host
// calls read through a reader implemented in the script. The member is
// resolved at call time: promoted interface methods advertise in the
// method set without a selectable member (the embedded slot may be nil,
// which then errors like a Go nil-interface dispatch).
type scriptReader struct {
	v    runtime.VMCaller
	recv runtime.Value
}

func (s *scriptReader) Read(p []byte) (int, error) {
	m, ok := s.v.Member(s.recv, "Read")
	if !ok || m == nil || m == runtime.NIL {
		return 0, fmt.Errorf("runtime error: invalid memory address or nil pointer dereference")
	}
	pv := scriptVal(p)
	r, err := s.v.Call(m, []runtime.Value{pv})
	n, err2 := readResult(r)
	copyBackBytes(p, pv, n)
	if err != nil {
		return 0, err
	}
	return n, err2
}

// copyBackBytes mirrors a callee's writes to the script slice back into
// the host caller's buffer — a []byte param crossed to the script as a
// fresh element copy (see scriptVal), so writes the inner callReflectFunc
// already copied into the slice's elements would otherwise be lost (a
// reader that fills nothing visible makes every ReadLine loop forever).
func copyBackBytes(dst []byte, src runtime.Value, n int) {
	sl, ok := src.(*runtime.Slice)
	if !ok {
		return
	}
	for i := 0; i < n && i < len(sl.Elems) && i < len(dst); i++ {
		dst[i] = byte(int64Of(sl.Elems[i]))
	}
}

// scriptReaderWriterTo adds a WriteTo proxy: the pair of interfaces is
// what net/http's nopCloser type probes dispatch on.
type scriptReaderWriterTo struct {
	v    runtime.VMCaller
	recv runtime.Value
}

func (s *scriptReaderWriterTo) Read(p []byte) (int, error) {
	m, ok := s.v.Member(s.recv, "Read")
	if !ok || m == nil || m == runtime.NIL {
		return 0, fmt.Errorf("runtime error: invalid memory address or nil pointer dereference")
	}
	pv := scriptVal(p)
	r, err := s.v.Call(m, []runtime.Value{pv})
	n, err2 := readResult(r)
	copyBackBytes(p, pv, n)
	if err != nil {
		return 0, err
	}
	return n, err2
}

func (s *scriptReaderWriterTo) WriteTo(w io.Writer) (int64, error) {
	m, ok := s.v.Member(s.recv, "WriteTo")
	if !ok || m == nil || m == runtime.NIL {
		return 0, fmt.Errorf("runtime error: invalid memory address or nil pointer dereference")
	}
	r, err := s.v.Call(m, []runtime.Value{&runtime.GoValue{V: w}})
	if err != nil {
		return 0, err
	}
	return readResult64(r)
}

// readResult decodes a script (n int, err error) pair.
func readResult(r runtime.Value) (int, error) {
	n, err := readResult64(r)
	return int(n), err
}

func readResult64(r runtime.Value) (int64, error) {
	if t, ok := r.(*runtime.Tuple); ok {
		var n int64
		if len(t.Elems) > 0 {
			if i, ok := runtime.Unwrap(t.Elems[0]).(int64); ok {
				n = i
			}
		}
		var werr error
		if len(t.Elems) > 1 {
			werr = asErr(goNative(t.Elems[1]))
		}
		return n, werr
	}
	if i, ok := runtime.Unwrap(r).(int64); ok {
		return i, nil
	}
	return 0, nil
}

// scriptWriter adapts a script-side Write method to io.Writer so
// fmt.Fprint* writes through a writer implemented in the script.
type scriptWriter struct {
	v     runtime.VMCaller
	write runtime.Value
}

func (s *scriptWriter) Write(p []byte) (int, error) {
	r, err := s.v.Call(s.write, []runtime.Value{scriptVal(p)})
	if err != nil {
		return 0, err
	}
	switch t := r.(type) {
	case *runtime.Tuple:
		var n int
		if len(t.Elems) > 0 {
			if i, ok := runtime.Unwrap(t.Elems[0]).(int64); ok {
				n = int(i)
			}
		}
		if len(t.Elems) > 1 {
			return n, hostErrOf(s.v, t.Elems[1])
		}
		return n, nil
	default:
		if i, ok := runtime.Unwrap(r).(int64); ok {
			return int(i), nil
		}
		return 0, nil
	}
}

// asReader mirrors asWriter for io.Reader args: `strings.NewReader`'s
// GoValue unwraps to the real reader, `&r` cells deref through. A nil
// argument is a valid (nil) io.Reader — `io.NopCloser(nil)` is legal Go.
func asReader(v any) (io.Reader, error) {
	if fv, ok := v.(*fmtValue); ok {
		v = fv.x
	}
	if v == nil {
		return nil, nil
	}
	if dv, ok := runtime.Deref(v); ok {
		v = dv
	}
	if g, ok := v.(*runtime.GoValue); ok {
		v = g.V
	}
	if r, ok := v.(io.Reader); ok {
		return r, nil
	}
	return nil, fmt.Errorf("not an io.Reader: %T", v)
}

// goJSON marshals a script value into the shape encoding/json expects:
// structs become field-name maps, runtime maps/slices recurse, GoValue
// unwraps. c carries the engine's type-resolution hooks — embedded
// fields promote through them.
func goJSON(c runtime.VMCaller, v any) any {
	// a value whose method set offers MarshalJSON encodes itself —
	// gc's marshalerEncoder calls it instead of walking the value by
	// shape. A nil pointer/interface answers null below without a call
	// (gc's nil-pointer exception).
	switch v.(type) {
	case *runtime.TypedNil, *runtime.IfaceNil:
	case nil:
	default:
		if m, ok := runtime.IfaceMember(c, v, "MarshalJSON"); ok {
			if r, err := c.Call(m, nil); err == nil {
				if b, e, ok := jsonMarshalResult(r); ok {
					if e != nil {
						return &jsonScriptErr{sv: e}
					}
					return json.RawMessage(b)
				}
			}
		}
	}
	switch x := v.(type) {
	case *runtime.Named:
		return goJSON(c, x.V)
	case *runtime.Cell:
		return goJSON(c, x.Elem)
	case *runtime.TypedNil, *runtime.IfaceNil:
		// a nil pointer/interface field marshals as null — passing the
		// wrapper through would reflect on its bookkeeping fields.
		return nil
	case *runtime.Struct:
		// structs marshal in DECLARATION order (encoding/json never
		// sorts struct fields) — an ordered map keeps that visible.
		// Anonymous embeds flatten: promoted fields emit in place, a
		// name tag on the embed demotes it to a named member, and a
		// depth/tag tie for one key drops every candidate (gc emits
		// nothing — not even a null — for an ambiguous key).
		var o orderedObject
		ents := jsonMarshalLive(jsonStructFields(c, x.Def))
		for _, e := range ents {
			f, ok := jsonReadPath(x, e.path)
			if !ok {
				// a nil pointer embed's promoted fields are
				// unreachable — gc omits them entirely
				continue
			}
			if e.omit && jsonIsEmpty(f) {
				continue
			}
			fv := goJSON(c, f)
			// `,string` applies to the DECLARED kind: interface
			// fields marshal normally even when the tag asks.
			if e.asStr && !jsonPathIsIface(c, x.Def, e.path) {
				if lit, ok := jsonStringLit(f); ok {
					fv = lit
				}
			}
			o.keys = append(o.keys, e.key)
			o.vals = append(o.vals, fv)
		}
		return o
	case *runtime.Slice:
		out := make([]any, len(x.Elems))
		for i, e := range x.Elems {
			out[i] = goJSON(c, e)
		}
		return out
	case *runtime.Map:
		m := make(map[string]any, x.Len())
		for i := 0; i < x.Len(); i++ {
			k, e := x.At(i)
			m[str(goNative(k))] = goJSON(c, e)
		}
		return m
	case *runtime.GoValue:
		return x.V
	case map[any]any: // goNative's *runtime.Map output — keys stringify
		m := make(map[string]any, len(x))
		for k, e := range x {
			m[str(k)] = goJSON(c, e)
		}
		return m
	case []any: // goNative's *runtime.Slice output
		for i, e := range x {
			x[i] = goJSON(c, e)
		}
		return x
	default:
		return v
	}
}

// jsonFieldKey maps a struct field to its JSON object key: the `json`
// tag's name wins, a "-" tag skips the field, `omitempty` drops empty
// values on marshal, `,string` wraps scalar fields in quoted literals,
// and an absent tag falls back to the field name (matching
// encoding/json's defaulting).
func jsonFieldKey(def *runtime.TypeDef, name string) (key string, named, omitEmpty, asString, skip bool) {
	if def != nil && def.FTags != nil {
		if tag, ok := def.FTags[name]; ok {
			j := reflect.StructTag(tag).Get("json")
			if j == "-" {
				return "", false, false, false, true
			}
			omit := false
			asStr := false
			if i := strings.IndexByte(j, ','); i >= 0 {
				for _, opt := range strings.Split(j[i+1:], ",") {
					switch opt {
					case "omitempty":
						omit = true
					case "string":
						asStr = true
					}
				}
				j = j[:i]
			}
			if j != "" {
				return j, true, omit, asStr, false
			}
		}
	}
	return name, false, false, false, false
}

// jsonFieldIsIface reports whether struct field i is declared with an
// interface type — `,string` on an interface field is ignored on
// marshal (the option keys off the declared kind, not the value's).
// The declared field type comes from the caller's FieldTypes hook —
// the same resolution field stores use, so builtin any/error
// spellings, interface literals and named interface types all answer
// through the resolved typedef's Kind. A hole typedef for an
// unresolvable declaration keeps its AST: an interface literal there
// still counts.
func jsonFieldIsIface(c runtime.VMCaller, def *runtime.TypeDef, i int) bool {
	if c == nil {
		return false
	}
	fts, err := c.FieldTypes(def)
	if err != nil || i < 0 || i >= len(fts) {
		return false
	}
	ft := fts[i]
	if ft == nil {
		return false
	}
	if ft.Kind == runtime.KindInterface {
		return true
	}
	_, ok := typedefAst(ft).(*ast.InterfaceType)
	return ok
}

// jsonIsEmpty mirrors encoding/json's isEmptyValue for `omitempty`:
// false, 0, "", and empty/nil containers are dropped.
func jsonIsEmpty(v any) bool {
	switch x := v.(type) {
	case *runtime.Named:
		return jsonIsEmpty(x.V)
	case *runtime.Cell:
		return jsonIsEmpty(x.Elem)
	case bool:
		return !x
	case int64:
		return x == 0
	case float64:
		return x == 0
	case string:
		return x == ""
	case *runtime.Slice:
		return len(x.Elems) == 0
	case *runtime.Map:
		return len(x.Pairs) == 0
	case runtime.Nil, *runtime.IfaceNil, *runtime.TypedNil:
		return true
	}
	return false
}

// orderedObject marshals struct fields in declaration order —
// encoding/json emits struct fields in source order, unlike map keys
// which it sorts, so a plain map[string]any loses the field order.
type orderedObject struct {
	keys []string
	vals []any
}

func (o orderedObject) MarshalJSON() ([]byte, error) {
	var sb strings.Builder
	sb.WriteByte('{')
	for i, k := range o.keys {
		kb, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		vb, err := json.Marshal(o.vals[i])
		if err != nil {
			return nil, err
		}
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.Write(kb)
		sb.WriteByte(':')
		sb.Write(vb)
	}
	sb.WriteByte('}')
	return []byte(sb.String()), nil
}

// derefTyp peels the pointer level off a value's typedef — json.Unmarshal
// decodes into the pointee, so `&cfg` (*Config) shapes as Config.
func derefTyp(td *runtime.TypeDef) *runtime.TypeDef {
	if td != nil && td.Kind == runtime.KindPointer {
		if td.Elem == nil {
			// `&v` where the pointee's typedef could not be recovered
			// (e.g. `var v any` carries an untyped iface nil) — decode
			// as `any` rather than wrapping the value in a cell.
			return nil
		}
		return td.Elem
	}
	return td
}

// jsonErrCtx is the bound decoder's mirror of encoding/json's
// errorContext: strct is the bare name of the root struct target (empty
// for non-struct roots — go1.27 reports Struct only for the root's own
// type), path is the field/index/key chain from the root to the node
// currently being shaped, err keeps the first type error, and errs
// collects every recorded one with its path — the reported error is
// the earliest in input order like gc (a map decode has lost it).
type jsonErrCtx struct {
	strct string
	path  []string
	err   error
	errs  []jsonErrAt
	// callErr records a non-nil error a script UnmarshalJSON returned
	// — it is a script Value (not a *jsonUnmarshalTypeError) and wins
	// over the by-shape type errors the way gc reports the call's
	// error for the value it decoded.
	callErr runtime.Value
	// fatal records a failed METHOD CALL itself (a script panic
	// surfacing as a call error) — gc lets it propagate, so it must
	// not be swallowed into by-shape decoding.
	fatal error
}

// jsonScriptErr ferries a script MarshalJSON's error return through
// encoding/json's own error channel: the runtime value is not an
// error, so the wrapper presents it as one and the Marshal intrinsic
// unwraps it back out.
type jsonScriptErr struct{ sv runtime.Value }

func (e *jsonScriptErr) Error() string { return "json: script Marshaler error" }
func (e *jsonScriptErr) MarshalJSON() ([]byte, error) {
	return nil, e
}

// jsonMarshalerErr renders gc's MarshalerError text for a script
// MarshalJSON failure — `json: error calling MarshalJSON for type *T:
// <err>` (gc's encoder always invokes through an address, so the named
// type spells *T even for a value receiver).
func jsonMarshalerErr(c runtime.VMCaller, operand runtime.Value, sv runtime.Value) error {
	name := "unknown"
	if vt := c.TypeOf(operand); vt != nil {
		name = runtime.DisplayName(derefTyp(vt))
		if vt.Kind != runtime.KindPointer {
			name = "*" + name
		}
	}
	text := "script error"
	if s, ok := runtime.IfaceCallString(c, sv, "Error"); ok && s != "" {
		text = s
	}
	return fmt.Errorf("json: error calling MarshalJSON for type %s: %s", name, text)
}

// jsonMarshalResult extracts the ([]byte, error) a script MarshalJSON
// returns: ok=false when the call result is not the Marshaler shape
// (a same-named method with a different signature does not implement
// the interface, so the value falls back to by-shape encoding).
func jsonMarshalResult(r runtime.Value) (b []byte, e runtime.Value, ok bool) {
	t, isTuple := r.(*runtime.Tuple)
	if !isTuple || len(t.Elems) != 2 {
		return nil, nil, false
	}
	switch t.Elems[1].(type) {
	case runtime.Nil, *runtime.TypedNil, *runtime.IfaceNil:
	default:
		e = t.Elems[1]
	}
	b, ok = byteSlice(goNative(t.Elems[0])), true
	return b, e, ok
}

// jsonUnmarshalShape reports whether m spells func([]byte) error —
// the loose arity check applied before calling a script UnmarshalJSON:
// a same-named method with a different signature does not implement
// json.Unmarshaler, so its value decodes by shape. Host-callable
// methods carry no Decl and are assumed conforming.
func jsonUnmarshalShape(m runtime.Value) bool {
	b, ok := m.(*runtime.BoundMethod)
	if !ok {
		return true
	}
	if b.Fn == nil || b.Fn.Decl == nil || b.Fn.Decl.Type == nil {
		return false
	}
	ft := b.Fn.Decl.Type
	n := 0
	if ft.Params != nil {
		for _, f := range ft.Params.List {
			if len(f.Names) == 0 {
				n++
			} else {
				n += len(f.Names)
			}
		}
	}
	return n == 1 && ft.Results != nil && len(ft.Results.List) == 1
}

// jsonNilish reports a nil pointer/interface payload.
func jsonNilish(v runtime.Value) bool {
	switch v.(type) {
	case *runtime.TypedNil, *runtime.IfaceNil:
		return true
	}
	return false
}

// jsonErrResult unwraps the single `error` a script UnmarshalJSON
// returns: a nil-ish payload becomes NIL, anything else is the script
// error value itself.
func jsonErrResult(r runtime.Value) runtime.Value {
	if t, ok := r.(*runtime.Tuple); ok {
		if len(t.Elems) == 1 {
			r = t.Elems[0]
		}
	}
	switch r.(type) {
	case runtime.Nil, *runtime.TypedNil, *runtime.IfaceNil:
		return runtime.NIL
	}
	return r
}

// fail records the first type-mismatch error the way saveError does:
// later failures are ignored while decoding continues.
func (x *jsonErrCtx) fail(value string, td *runtime.TypeDef, cause error) {
	err := &jsonUnmarshalTypeError{
		value: value,
		strct: x.strct,
		field: strings.Join(x.path, "."),
		typ:   runtime.DisplayName(td),
		cause: cause,
	}
	x.errs = append(x.errs, jsonErrAt{err: err, path: append([]string{}, x.path...)})
	if x.err == nil {
		x.err = err
	}
}

// failErr records a ready-made error verbatim — for failures whose
// text is not an UnmarshalTypeError (e.g. the inner-literal syntax
// error of a `,string` decode into an interface field).
func (x *jsonErrCtx) failErr(err error) {
	x.errs = append(x.errs, jsonErrAt{err: err, path: append([]string{}, x.path...)})
	if x.err == nil {
		x.err = err
	}
}

// jsonErrAt pins a recorded decode error to the field path it fired
// at — the reported error is the earliest in INPUT order like gc's
// UnmarshalTypeError, and a map[string]any decode no longer carries
// that order.
type jsonErrAt struct {
	err  error
	path []string
}

// jsonErrOrder maps each object's member path ("\x00"-joined) to its
// stream position by walking the raw input's tokens once — position 0
// is the first pair gc's streaming decoder would decode.
func jsonErrOrder(data []byte) map[string]int {
	pos := map[string]int{}
	n := 0
	var walk func(d *json.Decoder, path []string)
	walk = func(d *json.Decoder, path []string) {
		t, err := d.Token()
		if err != nil {
			return
		}
		switch t {
		case json.Delim('{'):
			for d.More() {
				kt, err := d.Token()
				if err != nil {
					return
				}
				key, _ := kt.(string)
				p := append(append(make([]string, 0, len(path)+1), path...), key)
				pos[strings.Join(p, "\x00")] = n
				n++
				walk(d, p)
			}
			d.Token() // '}'
		case json.Delim('['):
			for i := 0; d.More(); i++ {
				p := append(append(make([]string, 0, len(path)+1), path...), strconv.Itoa(i))
				pos[strings.Join(p, "\x00")] = n
				n++
				walk(d, p)
			}
			d.Token() // ']'
		}
	}
	walk(json.NewDecoder(bytes.NewReader(data)), nil)
	return pos
}

// jsonInputErr picks the error whose path is earliest in the raw
// input, falling back to the first recorded when a path never matched
// a member position (e.g. a root-level failure).
func jsonInputErr(data []byte, errs []jsonErrAt) error {
	if len(errs) == 0 {
		return nil
	}
	if len(errs) == 1 {
		return errs[0].err
	}
	pos := jsonErrOrder(data)
	best, pick := -1, errs[0]
	for _, e := range errs {
		p := e.path
		pp, ok := pos[strings.Join(p, "\x00")]
		for !ok && len(p) > 0 {
			p = p[:len(p)-1]
			pp, ok = pos[strings.Join(p, "\x00")]
		}
		if ok && (best == -1 || pp < best) {
			best, pick = pp, e
		}
	}
	return pick.err
}

// jsonUnmarshalTypeError renders encoding/json's UnmarshalTypeError text.
// The bound decoder cannot build a reflect.Type, so the message is the
// contract — including go1.27's heuristic that elides "Go struct field"
// when the path's last element is a numeric index.
type jsonUnmarshalTypeError struct {
	value string // "bool", "number 1.5", "string", "array", "object"
	strct string // root struct's bare name, "" otherwise
	field string // dotted path from the root
	typ   string // display spelling of the failing typedef
	cause error  // underlying error (e.g. illegal base64 data)
}

func (e *jsonUnmarshalTypeError) Error() string {
	var s string
	if e.strct != "" || e.field != "" {
		intoWhat := "Go struct field "
		i := strings.LastIndexByte(e.field, '.') + 1
		if len(e.field[i:]) > 0 && strings.TrimRight(e.field[i:], "0123456789") == "" {
			intoWhat = "" // likely a Go slice or array
		}
		s = "json: cannot unmarshal " + e.value + " into " + intoWhat + e.strct + "." + e.field + " of type " + e.typ
	} else {
		s = "json: cannot unmarshal " + e.value + " into Go value of type " + e.typ
	}
	if e.cause != nil {
		s += ": " + e.cause.Error()
	}
	return s
}

// jsonValueName is the Value half of UnmarshalTypeError — the JSON kind
// that failed to decode ("bool", "array", ...).
func jsonValueName(dec any) string {
	switch dec.(type) {
	case bool:
		return "bool"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return "number"
	}
}

// jsonNumLit spells a decoded float64 like the literal text encoding/json
// reports in "cannot unmarshal number <lit>": 1.5 stays 1.5, 1e30 keeps
// the lowercase-e form (FormatFloat's "1e+30" loses the plus gc omits).
func jsonNumLit(f float64) string {
	return strings.Replace(strconv.FormatFloat(f, 'g', -1, 64), "e+", "e", 1)
}

// jsonBareName is the bare declared name used for errorContext.Struct —
// "BoolSchema" for main.BoolSchema, "" for anonymous typedefs.
func jsonBareName(td *runtime.TypeDef) string {
	if td == nil {
		return ""
	}
	if i := strings.LastIndex(td.Name, "."); i >= 0 {
		return td.Name[i+1:]
	}
	return td.Name
}

// priorOr keeps the previous value on a decode failure, like gc leaving
// the destination untouched at a type mismatch; without a prior the
// typedef's zero is the fallback.
func priorOr(prior runtime.Value, c runtime.VMCaller, td *runtime.TypeDef) runtime.Value {
	if prior != nil {
		return prior
	}
	return c.Zero(td)
}

// jsonFieldEntry is one JSON-visible struct field: a declared field or
// a field promoted through anonymous embeds, plus the index path that
// reaches it through the struct's nested Fields slices
// (Fields[path[0]].Fields[path[1]]...).
type jsonFieldEntry struct {
	key    string // effective JSON key (tag name or field name)
	path   []int  // index chain into nested Fields
	tagged bool   // the key came from an explicit `json:"name"`
	depth  int    // embed hops — 0 for declared fields
	omit   bool   // `,omitempty`
	asStr  bool   // `,string`
}

// jsonStructFields flattens a struct typedef into its JSON-visible
// field entries — declared fields plus fields promoted through
// anonymous embeds, in declaration order with each embed expanded in
// place. A `json:"name"`-tagged embed is a NAMED member rather than
// promoted, and `,omitempty`/`,string` options on an anonymous embed
// are ignored (the promoted fields' own tags govern). A non-struct
// embed (e.g. a named int) behaves like a named field.
func jsonStructFields(c runtime.VMCaller, td *runtime.TypeDef) []jsonFieldEntry {
	var out []jsonFieldEntry
	// Cycle guard on type IDENTITY, not the typedef pointer: embed
	// resolution (jsonResolveTypName) can rebuild a fresh typedef per
	// hop, so `type Node struct{ *Node }` would walk forever keyed on
	// *TypeDef. The declaring TypeSpec is shared across rebuilds and
	// pins the type.
	seenTd := map[*runtime.TypeDef]bool{}
	seenTs := map[*ast.TypeSpec]bool{}
	var walk func(st *runtime.TypeDef, prefix []int, depth int)
	walk = func(st *runtime.TypeDef, prefix []int, depth int) {
		if st == nil {
			return
		}
		if st.Spec != nil {
			if seenTs[st.Spec] {
				return
			}
			seenTs[st.Spec] = true
			defer delete(seenTs, st.Spec)
		} else {
			if seenTd[st] {
				return
			}
			seenTd[st] = true
			defer delete(seenTd, st)
		}
		for i, name := range st.Fields {
			key, named, omit, asStr, skip := jsonFieldKey(st, name)
			if skip {
				continue
			}
			p := append(append(make([]int, 0, len(prefix)+1), prefix...), i)
			if jsonEmbedded(st, i) && !named {
				if et := jsonPeelPtr(c, jsonEmbedTyp(c, st, i)); et != nil {
					if _, ok := typedefAst(et).(*ast.StructType); ok {
						walk(et, p, depth+1)
						continue
					}
				}
			}
			out = append(out, jsonFieldEntry{
				key: key, path: p, tagged: named,
				depth: depth, omit: omit, asStr: asStr,
			})
		}
	}
	walk(td, nil, 0)
	return out
}

// jsonEmbedded reports whether Fields[i] is an embedded (anonymous)
// field — embedded specs are indexed through EmbedIdx.
func jsonEmbedded(st *runtime.TypeDef, i int) bool {
	for _, x := range st.EmbedIdx {
		if x == i {
			return true
		}
	}
	return false
}

// jsonEmbedTyp resolves the typedef of the i'th embedded field — the
// lazily-populated Embeds table first, then the spec's type AST
// through the caller's ResolveType hook (package index, LocalTypes,
// generic binds, import selectors), falling back to the package-level
// elemTyp approximation.
func jsonEmbedTyp(c runtime.VMCaller, st *runtime.TypeDef, i int) *runtime.TypeDef {
	for k, ei := range st.EmbedIdx {
		if ei != i {
			continue
		}
		if k < len(st.Embeds) && st.Embeds[k] != nil {
			return st.Embeds[k]
		}
		if k < len(st.EmbedSpecs) {
			return jsonResolveTypName(c, st, st.EmbedSpecs[k])
		}
	}
	return nil
}

// jsonResolveTypName resolves a type expression to a typedef detailed
// enough to walk fields over, via the caller's ResolveType hook —
// generic binds, function-local decls, package-level decls and import
// selectors all resolve the way embedded-field specs do elsewhere in
// the VM. An unresolvable expression degrades to the elemTyp
// approximation rather than dropping the field.
func jsonResolveTypName(c runtime.VMCaller, st *runtime.TypeDef, e ast.Expr) *runtime.TypeDef {
	// A type embedding itself names the decl's own name — the decl-time
	// LocalTypes snapshot predates it and the package index only holds
	// package-level types — so peel stars/parens and compare against
	// the enclosing typedef before the hook runs.
	x := e
	for {
		if sx, ok := x.(*ast.StarExpr); ok {
			x = sx.X
			continue
		}
		if px, ok := x.(*ast.ParenExpr); ok {
			x = px.X
			continue
		}
		break
	}
	if id, ok := x.(*ast.Ident); ok && st != nil && st.Spec != nil && st.Name == id.Name {
		return st
	}
	if c != nil && st != nil {
		if td, err := c.ResolveType(st, e); err == nil && td != nil {
			return td
		}
	}
	var pkg *runtime.Package
	if st != nil {
		pkg = st.Pkg
	}
	return elemTyp(e, pkg)
}

// jsonPeelPtr removes pointer levels from a typedef through the
// caller's ElemOf hook — the runtime Elem link, the declared *T AST
// and named pointer types all peel the way the engine's member walks
// do. When the hook can't resolve (nil caller, missing import) the
// declared AST's immediate pointee keeps the walk terminating.
func jsonPeelPtr(c runtime.VMCaller, td *runtime.TypeDef) *runtime.TypeDef {
	for td != nil {
		if td.Kind != runtime.KindPointer {
			if _, ok := typedefAst(td).(*ast.StarExpr); !ok {
				return td
			}
		}
		var et *runtime.TypeDef
		if c != nil {
			if r, err := c.ElemOf(td); err == nil {
				et = r
			}
		}
		if et == nil || et == td {
			if td.Elem != nil {
				et = td.Elem
			} else if st, ok := typedefAst(td).(*ast.StarExpr); ok {
				et = elemTyp(st.X, td.Pkg)
			} else {
				return td
			}
		}
		td = et
	}
	return nil
}

// jsonEmbedStruct steps into the embedded field at index idx of s —
// following a live embedded struct, or allocating a nil pointer
// embed's pointee the way gc allocates on decode. ok=false when the
// field holds no struct value.
func jsonEmbedStruct(c runtime.VMCaller, s *runtime.Struct, idx int, ftd *runtime.TypeDef) (*runtime.Struct, bool) {
	f := s.Fields[idx]
	if st := runtime.StructOf(f); st != nil {
		return st, true
	}
	et := jsonPeelPtr(c, ftd)
	if et == nil {
		return nil, false
	}
	nv, ok := c.Zero(et).(*runtime.Struct)
	if !ok {
		return nil, false
	}
	if pc, ok := f.(*runtime.Cell); ok {
		pc.Elem = nv
		return nv, true
	}
	s.Fields[idx] = &runtime.Cell{Elem: nv}
	return nv, true
}

// jsonReadPath follows an entry's index path for marshal — a nil
// pointer embed (or any non-struct hop) makes the promoted fields
// unreachable, and gc omits them from the output entirely.
func jsonReadPath(s *runtime.Struct, path []int) (runtime.Value, bool) {
	cur := s
	for h := 0; h < len(path)-1; h++ {
		st := runtime.StructOf(cur.Fields[path[h]])
		if st == nil {
			return nil, false
		}
		cur = st
	}
	return cur.Fields[path[len(path)-1]], true
}

// jsonPathIsIface reports whether the leaf field reached by path is
// declared as an interface type — needed for `,string` marshal, which
// gc applies by declared kind.
func jsonPathIsIface(c runtime.VMCaller, td *runtime.TypeDef, path []int) bool {
	cur := td
	for h := 0; h < len(path)-1; h++ {
		cur = jsonPeelPtr(c, jsonEmbedTyp(c, cur, path[h]))
	}
	return jsonFieldIsIface(c, cur, path[len(path)-1])
}

// jsonDominant applies gc's dominantField reduction per JSON key:
// within the group sharing one exact key, the shallowest depth wins,
// a tagged field beats an untagged one at that depth, and a tie on
// both kills EVERY candidate in the group — the key then decodes
// nothing and marshals nothing.
func jsonDominant(ents []jsonFieldEntry) []bool {
	live := make([]bool, len(ents))
	byKey := map[string][]int{}
	for i := range ents {
		byKey[ents[i].key] = append(byKey[ents[i].key], i)
	}
	for _, cand := range byKey {
		minD := ents[cand[0]].depth
		for _, i := range cand[1:] {
			if ents[i].depth < minD {
				minD = ents[i].depth
			}
		}
		var keep []int
		for _, i := range cand {
			if ents[i].depth == minD {
				keep = append(keep, i)
			}
		}
		var tg []int
		for _, i := range keep {
			if ents[i].tagged {
				tg = append(tg, i)
			}
		}
		if len(tg) > 0 {
			keep = tg
		}
		if len(keep) == 1 {
			live[keep[0]] = true
		}
	}
	return live
}

// jsonMarshalLive keeps only the entries surviving jsonDominant — gc's
// ambiguous-key drop applies to marshaling too.
func jsonMarshalLive(ents []jsonFieldEntry) []jsonFieldEntry {
	live := jsonDominant(ents)
	out := make([]jsonFieldEntry, 0, len(ents))
	for i := range ents {
		if live[i] {
			out = append(out, ents[i])
		}
	}
	return out
}

// jsonShape converts a decoded JSON tree (map[string]any / []any /
// scalars from encoding/json) into the runtime shape a declared typedef
// expects: structs get their declared fields by json tag, numeric fields
// land as int64/float64 per the declared scalar, and slices/maps keep
// their typedef tags. A decoded value whose kind cannot go into the
// declared type records an UnmarshalTypeError-shaped failure on ectx and
// keeps prior (gc decodes past errors and preserves what was there);
// null nils pointer/slice/map/interface targets and is a no-op elsewhere.
func jsonShape(c runtime.VMCaller, dec any, td *runtime.TypeDef, prior runtime.Value, ectx *jsonErrCtx) runtime.Value {
	if td == nil {
		return scriptVal(jsonDeep(dec))
	}
	if dec == nil {
		switch td.Kind {
		case runtime.KindPointer, runtime.KindSlice, runtime.KindMap, runtime.KindInterface:
			if td.Kind == runtime.KindSlice && isArrayTyp(td) {
				// gc ignores null for fixed arrays: the value stays.
				return priorOr(prior, c, td)
			}
			return c.Zero(td)
		}
		return priorOr(prior, c, td)
	}
	// gc calls UnmarshalJSON on the addressable decode target: a
	// non-nil *T through its own pointer, a named value through &v,
	// and a fresh slot — new field, slice element, map value — through
	// the addressable value it decodes into before storing. The
	// receiver's cell is returned rather than the prior so a
	// whole-value `*u = ...` assignment in the method reaches the
	// field. The literal handed over is the re-encoded subtree — the
	// original byte span is gone by the time decode reaches here. The
	// probe is gated to NAMED types: methods attach to named types
	// only, so an unnamed container's member walk must not borrow the
	// element type's methods.
	if td.Name != "" {
		var recv runtime.Value
		var cell *runtime.Cell
		switch prior.(type) {
		case *runtime.Named:
			cell = &runtime.Cell{Elem: prior}
			recv = cell
		case nil:
			cell = &runtime.Cell{Elem: c.Zero(td)}
			recv = cell
		default:
			recv = prior
		}
		if m, ok := runtime.IfaceMember(c, recv, "UnmarshalJSON"); ok && jsonUnmarshalShape(m) {
			if lit, lerr := json.Marshal(dec); lerr == nil {
				r, cerr := c.Call(m, []runtime.Value{scriptVal(lit)})
				if cerr != nil {
					if ectx.fatal == nil {
						ectx.fatal = cerr
					}
					return prior
				}
				if ev := jsonErrResult(r); ev != runtime.NIL {
					ectx.callErr = ev
				}
				if cell != nil {
					return cell.Elem
				}
				return prior
			}
		}
	}
	switch td.Kind {
	case runtime.KindStruct:
		m, ok := dec.(map[string]any)
		if !ok {
			ectx.fail(jsonValueName(dec), td, nil)
			return priorOr(prior, c, td)
		}
		z, ok := c.Zero(td).(*runtime.Struct)
		if !ok {
			ectx.fail("object", td, nil)
			return priorOr(prior, c, td)
		}
		var pf []runtime.Value
		if ps, ok := prior.(*runtime.Struct); ok && ps.Def == td {
			pf = ps.Fields
		}
		ents := jsonStructFields(c, td)
		// gc reduces each exact-name group by dominantField (dead
		// groups serve neither exact nor folded lookup), then decodes:
		// an exact-key hit wins, else the FIRST surviving entry in
		// declaration order whose name case-folds to the input key.
		live := jsonDominant(ents)
		exact := map[string]int{}
		for i := range ents {
			if live[i] {
				exact[ents[i].key] = i
			}
		}
		foldWin := map[string]int{}
		for k := range m {
			if _, ok := exact[k]; ok {
				continue
			}
			for i := range ents {
				if live[i] && strings.EqualFold(ents[i].key, k) {
					foldWin[k] = i
					break
				}
			}
		}
		for i := range ents {
			e := &ents[i]
			if !live[i] {
				continue
			}
			// the JSON key this entry decodes: its exact key when the
			// object carries it, else the folded key it won first
			fv, kok := m[e.key]
			if !kok {
				for k, w := range foldWin {
					if w == i {
						fv, kok = m[k], true
						break
					}
				}
			}
			if !kok {
				continue
			}
			ectx.path = append(ectx.path, e.key)
			cur, curTd, curPf := z, td, pf
			reach := true
			for h := 0; h < len(e.path)-1 && reach; h++ {
				// walk into the embedded field at path[h], allocating a
				// nil pointer embed the way gc allocates on decode.
				idx := e.path[h]
				etd := jsonEmbedTyp(c, curTd, idx)
				if etd == nil {
					etd = fieldTypOf(curTd, idx)
				}
				var pf2 runtime.Value
				if curPf != nil && idx < len(curPf) {
					pf2 = curPf[idx]
				}
				next, ok := jsonEmbedStruct(c, cur, idx, etd)
				if !ok {
					reach = false
					break
				}
				cur, curTd = next, jsonPeelPtr(c, etd)
				if ps2, ok := runtime.Unwrap(pf2).(*runtime.Struct); ok {
					curPf = ps2.Fields
				} else if pc, ok := runtime.Unwrap(pf2).(*runtime.Cell); ok {
					if ps3, ok3 := runtime.Unwrap(pc.Elem).(*runtime.Struct); ok3 {
						curPf = ps3.Fields
					} else {
						curPf = nil
					}
				} else {
					curPf = nil
				}
			}
			if reach {
				leaf := e.path[len(e.path)-1]
				var lpf runtime.Value
				if curPf != nil && leaf < len(curPf) {
					lpf = curPf[leaf]
				}
				ltd := c.TypeOf(cur.Fields[leaf])
				if e.asStr {
					cur.Fields[leaf] = jsonStringOpt(c, fv, ltd, lpf, ectx)
				} else {
					cur.Fields[leaf] = jsonShape(c, fv, ltd, lpf, ectx)
				}
			}
			ectx.path = ectx.path[:len(ectx.path)-1]
		}
		return z
	case runtime.KindSlice:
		// A string decodes as base64 only for a byte SLICE — a [N]byte
		// array is a number list like any other array (gc has no
		// base64 special case for arrays).
		if s, ok := dec.(string); ok && byteSliceTyp(td) && !isArrayTyp(td) {
			b, err := base64.StdEncoding.DecodeString(s)
			if err != nil {
				ectx.fail("string", td, err)
				return priorOr(prior, c, td)
			}
			el := make([]runtime.Value, len(b))
			for i, by := range b {
				el[i] = runtime.Tag(runtime.BasicTypedef("byte"), int64(by))
			}
			return &runtime.Slice{Elems: el, Typ: td}
		}
		arr, ok := dec.([]any)
		if !ok {
			ectx.fail(jsonValueName(dec), td, nil)
			return priorOr(prior, c, td)
		}
		et := c.TypeOf(c.ElemZero(td))
		if isArrayTyp(td) {
			// gc's fixed-array semantics: JSON positions decode into
			// the element in place (a failed position keeps the prior
			// value), positions past the JSON array reset to zero, and
			// extra JSON elements are skipped without error.
			n, ok := c.ArrayLenOf(td)
			if !ok {
				var pe []runtime.Value
				if ps, ok := prior.(*runtime.Slice); ok {
					pe = ps.Elems
				}
				if len(pe) > len(arr) {
					n = int64(len(pe))
				} else {
					n = int64(len(arr))
				}
			}
			el := make([]runtime.Value, n)
			var pe []runtime.Value
			if ps, ok := prior.(*runtime.Slice); ok {
				pe = ps.Elems
			}
			for i := range el {
				if i < len(arr) {
					ectx.path = append(ectx.path, strconv.Itoa(i))
					el[i] = jsonShape(c, arr[i], et, elemAt(pe, i), ectx)
					ectx.path = ectx.path[:len(ectx.path)-1]
				} else {
					el[i] = c.ElemZero(td)
				}
			}
			return &runtime.Slice{Elems: el, Typ: td}
		}
		el := make([]runtime.Value, len(arr))
		var pe []runtime.Value
		if ps, ok := prior.(*runtime.Slice); ok {
			pe = ps.Elems
		}
		for i := range arr {
			ectx.path = append(ectx.path, strconv.Itoa(i))
			el[i] = jsonShape(c, arr[i], et, elemAt(pe, i), ectx)
			ectx.path = ectx.path[:len(ectx.path)-1]
		}
		return &runtime.Slice{Elems: el, Typ: td}
	case runtime.KindMap:
		m, ok := dec.(map[string]any)
		if !ok {
			ectx.fail(jsonValueName(dec), td, nil)
			return priorOr(prior, c, td)
		}
		et := c.TypeOf(c.ElemZero(td))
		rm := &runtime.Map{Pairs: map[runtime.Value]runtime.Value{}, Typ: td}
		if pm, ok := prior.(*runtime.Map); ok {
			// gc decodes into the existing map: prior pairs stay unless a
			// decoded key overwrites them.
			for _, k := range pm.Order {
				if e, ok := pm.Pairs[k]; ok {
					rm.Insert(k, e)
				}
			}
		}
		kt := jsonMapKeyTyp(td)
		for k, e := range m {
			// gc parses the key first: a bad key drops the whole pair and
			// wins the error slot, while a bad element still inserts the
			// fresh zero it decoded into (gc's mapElem), so the element
			// shape runs only after the key is known to fit.
			ectx.path = append(ectx.path, k)
			if kv, kok := jsonMapKey(c, k, kt, ectx); kok {
				rm.Insert(kv, jsonShape(c, e, et, nil, ectx))
			}
			ectx.path = ectx.path[:len(ectx.path)-1]
		}
		return rm
	case runtime.KindPointer:
		var ep runtime.Value
		var pc *runtime.Cell
		if c2, ok := prior.(*runtime.Cell); ok {
			pc, ep = c2, c2.Elem
		}
		// *T implementing json.Unmarshaler decodes itself — gc
		// allocates a nil pointer first, then calls the method on it.
		if pc == nil {
			pc = &runtime.Cell{Elem: c.ElemZero(td)}
		}
		if m, ok := runtime.IfaceMember(c, pc, "UnmarshalJSON"); ok && jsonUnmarshalShape(m) {
			if lit, lerr := json.Marshal(dec); lerr == nil {
				r, cerr := c.Call(m, []runtime.Value{scriptVal(lit)})
				if cerr != nil {
					if ectx.fatal == nil {
						ectx.fatal = cerr
					}
					return pc
				}
				if ev := jsonErrResult(r); ev != runtime.NIL {
					ectx.callErr = ev
				}
				return pc
			}
		}
		return &runtime.Cell{Elem: jsonShape(c, dec, c.TypeOf(c.ElemZero(td)), ep, ectx)}
	case runtime.KindInterface:
		return scriptVal(jsonDeep(dec))
	}
	return jsonScalar(c, dec, td, prior, ectx)
}

// jsonMapKeyTyp resolves a map typedef's key typedef — LocalTypes
// first so a function-local `type NK int` key peels to int, then the
// package-level approximation in mapElemTyps.
func jsonMapKeyTyp(td *runtime.TypeDef) *runtime.TypeDef {
	mt, ok := typedefAst(td).(*ast.MapType)
	if !ok {
		return nil
	}
	if id, ok := mt.Key.(*ast.Ident); ok && td.LocalTypes != nil {
		if lt := td.LocalTypes[id.Name]; lt != nil {
			return lt
		}
	}
	kt, _ := mapElemTyps(td)
	return kt
}

// jsonMapKey parses one JSON object key text into the map's key type.
// gc accepts string/int/uint/float spellings plus named and pointer
// variants (a `*K` key lands in a fresh cell holding the parsed K);
// every other kind reports `cannot unmarshal string` for the key type,
// and a bad literal reports `number <key>`. Interface keys keep the
// string itself. ok=false drops the pair entirely.
func jsonMapKey(c runtime.VMCaller, k string, kt *runtime.TypeDef, ectx *jsonErrCtx) (runtime.Value, bool) {
	if kt == nil {
		return runtime.Value(k), true
	}
	// a pointer key decodes as the pointee, then addresses
	if pt, ok := typedefAst(kt).(*ast.StarExpr); ok {
		v, ok := jsonMapKey(c, k, bindTyp(kt, elemTyp(pt.X, kt.Pkg)), ectx)
		if !ok {
			return nil, false
		}
		return &runtime.Cell{Elem: v}, true
	}
	u := jsonUnderlyingName(kt)
	if kt.Kind == runtime.KindInterface || u == "any" || u == "interface{}" {
		return runtime.Value(k), true
	}
	conv := func(x any) (runtime.Value, bool) {
		v, err := c.Convert(kt, scriptVal(x))
		if err != nil {
			ectx.fail("string", kt, nil)
			return nil, false
		}
		return v, true
	}
	switch {
	case u == "string":
		return conv(k)
	case jsonIntName(u):
		bits, signed := jsonIntWidth(u)
		var err error
		var n int64
		var u64 uint64
		if signed {
			n, err = strconv.ParseInt(k, 10, bits)
		} else {
			u64, err = strconv.ParseUint(k, 10, bits)
		}
		if err != nil {
			ectx.fail("number "+k, kt, nil)
			return nil, false
		}
		if signed {
			return conv(n)
		}
		return conv(int64(u64))
	case u == "float64" || u == "float32":
		f, err := strconv.ParseFloat(k, 64)
		if err != nil {
			ectx.fail("number "+k, kt, nil)
			return nil, false
		}
		return conv(f)
	}
	// bool, struct, and other unsupported key kinds: gc reports the
	// raw string failing for the key type.
	ectx.fail("string", kt, nil)
	return nil, false
}

// elemAt indexes a prior container's stored values without failing — a
// prior field/element exists only when the old value matches the new
// shape's position.
func elemAt(vs []runtime.Value, i int) runtime.Value {
	if i < len(vs) {
		return vs[i]
	}
	return nil
}

// jsonScalar coerces a decoded JSON scalar into a declared scalar type.
// JSON numbers always arrive as float64, so int-typed targets convert
// (integrals only, like encoding/json's ParseInt of the literal). A
// kind mismatch is an UnmarshalTypeError, not a silent zero.
func jsonScalar(c runtime.VMCaller, dec any, td *runtime.TypeDef, prior runtime.Value, ectx *jsonErrCtx) runtime.Value {
	fail := func() runtime.Value {
		ectx.fail(jsonValueName(dec), td, nil)
		return priorOr(prior, c, td)
	}
	conv := func(x any) runtime.Value {
		v, err := c.Convert(td, scriptVal(x))
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
		f, ok := dec.(float64)
		if !ok {
			return fail()
		}
		i, ok := jsonIntOf(f, jsonUnderlyingName(td))
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
func jsonStringOpt(c runtime.VMCaller, dec any, td *runtime.TypeDef, prior runtime.Value, ectx *jsonErrCtx) runtime.Value {
	if dec == nil {
		return jsonShape(c, nil, td, prior, ectx)
	}
	s, sok := dec.(string)
	if td == nil {
		if sok {
			return scriptVal(s)
		}
		return scriptVal(jsonDeep(dec))
	}
	// A quoted "null" is the null literal, not the word: it follows
	// normal null semantics (nils nilable kinds, leaves the rest).
	if sok && s == "null" {
		return jsonShape(c, nil, td, prior, ectx)
	}
	if td.Kind == runtime.KindPointer {
		var ep runtime.Value
		if pc, ok := prior.(*runtime.Cell); ok {
			ep = pc.Elem
		}
		return &runtime.Cell{Elem: jsonStringOpt(c, dec, c.TypeOf(c.ElemZero(td)), ep, ectx)}
	}
	conv := func(x any) runtime.Value {
		v, err := c.Convert(td, scriptVal(x))
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
		return jsonShape(c, dec, td, prior, ectx)
	}
	if !sok {
		ectx.fail(jsonValueName(dec), td, nil)
		return priorOr(prior, c, td)
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
		return conv(int64(u64))
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
	bits, signed := jsonIntWidth(name)
	if bits >= 64 {
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
// into the map[any]any shape scriptVal already handles.
func jsonDeep(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[any]any, len(x))
		for k, e := range x {
			out[k] = jsonDeep(e)
		}
		return out
	case []any:
		for i, e := range x {
			x[i] = jsonDeep(e)
		}
		return x
	default:
		return v
	}
}

func mapKeys(v any) *runtime.Slice {
	if n, ok := v.(*runtime.Named); ok {
		return mapKeys(n.V)
	}
	if m, ok := v.(*runtime.Map); ok {
		return &runtime.Slice{Elems: append([]runtime.Value{}, m.Order...)}
	}
	if m, ok := v.(map[any]any); ok {
		return &runtime.Slice{Elems: slices.Collect(maps.Keys(m))}
	}
	return &runtime.Slice{}
}

func mapValues(v any) *runtime.Slice {
	if n, ok := v.(*runtime.Named); ok {
		return mapValues(n.V)
	}
	if m, ok := v.(*runtime.Map); ok {
		el := make([]runtime.Value, m.Len())
		for i := range el {
			_, el[i] = m.At(i)
		}
		return &runtime.Slice{Elems: el}
	}
	if m, ok := v.(map[any]any); ok {
		return &runtime.Slice{Elems: slices.Collect(maps.Values(m))}
	}
	return &runtime.Slice{}
}

// ffn is the fmt-package variant of fn: script values reach host fmt as
// fmtValue wrappers so composites render like Go's %v — a *runtime.Slice
// prints [1 2], a struct {x y}, a pointer &{...}, and a String()/Error()/
// GoString() method declared on the type is honored.
// scriptError boxes a script error value (a struct carrying an Error
// method) as a host error, so fmt.Errorf's %w chains and errors.Is/As
// walk it through host errors.Unwrap.
type scriptError struct {
	c runtime.VMCaller
	v runtime.Value
}

func (e *scriptError) Error() string {
	s, ok := runtime.IfaceCallString(e.c, e.v, "Error")
	if !ok {
		return fmt.Sprintf("%v", goNative(e.v))
	}
	return s
}

// Unwrap lets a script-declared `Unwrap() error` method join the host
// errors chain — errors.Unwrap/Is/As walk through it like Go's. A
// script `Unwrap() []error` reports no single cause — like
// errors.Join, the type has no Unwrap() error.
func (e *scriptError) Unwrap() error {
	kids := e.errKids()
	if len(kids) != 1 {
		return nil
	}
	return kids[0]
}

// errKids returns the error node's children: a script `Unwrap()
// []error` fans out like errors.Join's elements (a shape Unwrap()
// error cannot express alongside it on one Go type), a plain
// `Unwrap() error` is the single cause.
func (e *scriptError) errKids() []error {
	m, ok := runtime.IfaceMember(e.c, e.v, "Unwrap")
	if !ok {
		return nil
	}
	r, err := e.c.Call(m, nil)
	if err != nil {
		return nil
	}
	switch x := runtime.Unwrap(r).(type) {
	case *runtime.Slice:
		kids := make([]error, 0, len(x.Elems))
		for _, elem := range x.Elems {
			if ke := hostErrOf(e.c, elem); ke != nil {
				kids = append(kids, ke)
			}
		}
		return kids
	case *runtime.TypedNil:
		if x.Typ != nil && x.Typ.Kind == runtime.KindSlice {
			return nil // `Unwrap() []error` returned a nil slice
		}
	}
	if ke := hostErrOf(e.c, r); ke != nil {
		return []error{ke}
	}
	return nil
}

// Is lets a script-declared `Is(error) bool` method join host
// errors.Is — the error's own equality override like Go's custom-Is
// rule. The host error target maps back to its script value the way
// hostErrOf boxed it, so the script side compares what it sees.
func (e *scriptError) Is(target error) bool {
	m, ok := runtime.IfaceMember(e.c, e.v, "Is")
	if !ok {
		return false
	}
	r, err := e.c.Call(m, []runtime.Value{scriptErrUnbox(target)})
	if err != nil {
		return false
	}
	b, _ := r.(bool)
	return b
}

// As lets a script-declared `As(any) bool` method join host
// errors.As/AsType — the error's own As assigns the target like Go's
// custom-As rule. A non-pointer target has nothing to write to and
// simply fails the script side's assert.
func (e *scriptError) As(target any) bool {
	m, ok := runtime.IfaceMember(e.c, e.v, "As")
	if !ok {
		return false
	}
	r, err := e.c.Call(m, []runtime.Value{target})
	if err != nil {
		return false
	}
	b, _ := r.(bool)
	return b
}

// wrapError is fmt.Errorf's %w product: message plus one cause.
type wrapError struct {
	msg string
	err error
}

func (e *wrapError) Error() string { return e.msg }
func (e *wrapError) Unwrap() error { return e.err }

// hostErrOf converts a script error value to a host error: boxed Go
// errors pass through, script values get a scriptError that calls back
// into the VM for Error().
func hostErrOf(c runtime.VMCaller, v runtime.Value) error {
	switch x := v.(type) {
	case *runtime.GoValue:
		if e, ok := x.V.(error); ok {
			return e
		}
		return fmt.Errorf("%v", x.V)
	case *runtime.Named:
		return hostErrOf(c, x.V)
	case runtime.Nil, *runtime.IfaceNil:
		// an untyped nil error unwraps to nothing — but a boxed typed
		// nil (`var err error = (*E)(nil)`) is a real error value: its
		// concrete tag unwraps to the TypedNil As/Is match on.
		if td := runtime.BoxedNilTyp(v); td != nil {
			return &scriptError{c: c, v: &runtime.TypedNil{Typ: td}}
		}
		return nil
	default:
		return &scriptError{c: c, v: v}
	}
}

// scriptErrUnbox maps a chain element back to the script-visible error
// value — a scriptError's inner script value, anything else boxed.
func scriptErrUnbox(e error) runtime.Value {
	if se, ok := e.(*scriptError); ok {
		return se.v
	}
	return errVal(e)
}

// cellElemTyp reads the declared type held by a cell (the target of
// errors.As's &x): its TypedNil/IfaceNil/Struct tag.
func cellElemTyp(v runtime.Value) *runtime.TypeDef {
	c, ok := v.(*runtime.Cell)
	if !ok {
		return nil
	}
	switch e := c.Elem.(type) {
	case *runtime.TypedNil:
		return e.Typ
	case *runtime.IfaceNil:
		return e.Typ
	case *runtime.Struct:
		return e.Def
	case *runtime.Named:
		return e.Typ
	case *runtime.Cell:
		// the variable's VALUE is itself a pointer (a cell): `&p` where
		// p is declared `*T` — the assignable target type is T.
		if e.Typ != nil {
			return e.Typ
		}
		if s, ok := e.Elem.(*runtime.Struct); ok {
			return s.Def
		}
		return nil
	}
	return nil
}

// findAsTarget walks an error's tree the way errors.As/AsType do —
// the node itself, its own As(any) bool, then Unwrap() error and
// Unwrap() []error children depth-first — and reports the first
// element satisfying the target. want is the target's element typedef
// (nil accepts anything); reqs is want's required method set for an
// interface target; target is the destination pointer a node's As
// method writes through (errors.As's user cell, AsType's result cell).
func findAsTarget(v runtime.VMCaller, err error, want *runtime.TypeDef, reqs map[string]bool, target runtime.Value) (runtime.Value, bool) {
	for err != nil {
		sv := scriptErrUnbox(err)
		if matchAsTarget(v, sv, want, reqs) {
			return sv, true
		}
		if xa, ok := err.(interface{ As(any) bool }); ok && xa.As(target) {
			// the node's own As wrote the answer through the target
			// pointer — read the cell back for the result value.
			if c, ok := target.(*runtime.Cell); ok {
				return c.Elem, true
			}
			return sv, true
		}
		switch kids := errKids(err); {
		case len(kids) == 0:
			return nil, false
		case len(kids) == 1:
			err = kids[0]
		default:
			for _, kid := range kids {
				if rv, ok := findAsTarget(v, kid, want, reqs, target); ok {
					return rv, true
				}
			}
			return nil, false
		}
	}
	return nil, false
}

// errIs walks an error tree the way gc's errors.Is does — each node's
// `err == target` equality on a comparable target, its own Is(error)
// bool, then Unwrap() error / Unwrap() []error children depth-first —
// sharing As's errKids so a script `Unwrap() []error` fans out like
// errors.Join's elements.
func errIs(err, target error) bool {
	targetComparable := errComparable(target)
	for {
		if targetComparable && errEqual(err, target) {
			return true
		}
		if x, ok := err.(interface{ Is(error) bool }); ok && x.Is(target) {
			return true
		}
		switch kids := errKids(err); {
		case len(kids) == 0:
			return false
		case len(kids) == 1:
			err = kids[0]
		default:
			for _, kid := range kids {
				if errIs(kid, target) {
					return true
				}
			}
			return false
		}
	}
}

// errComparable is gc's targetComparable: the target's dynamic type is
// comparable — a script value asks TryOpaqueKey (a struct containing a
// slice field is not), a host error its reflect type.
func errComparable(err error) bool {
	if se, ok := err.(*scriptError); ok {
		_, ok := runtime.TryOpaqueKey(se.v)
		return ok
	}
	return reflect.TypeOf(err).Comparable()
}

// errEqual is gc's `err == target` for a chain that may hold script
// errors: two scriptError boxes compare their script values canonically
// (OpaqueKey — the same mapKey equality host containers use), a script
// error never equals a native one, and two natives compare as errors.
func errEqual(err, target error) bool {
	s0, ok0 := err.(*scriptError)
	s1, ok1 := target.(*scriptError)
	if ok0 || ok1 {
		if !ok0 || !ok1 {
			return false
		}
		k0, g0 := runtime.TryOpaqueKey(s0.v)
		k1, g1 := runtime.TryOpaqueKey(s1.v)
		return g0 && g1 && k0 == k1
	}
	return err == target
}

// errKids returns an error node's children — Unwrap() error's single
// cause or Unwrap() []error's several, checked in Go's order.
// scriptError answers through the script Unwrap member itself so a
// script `Unwrap() []error` still fans out.
func errKids(err error) []error {
	if se, ok := err.(*scriptError); ok {
		return se.errKids()
	}
	if u, ok := err.(interface{ Unwrap() error }); ok {
		if e := u.Unwrap(); e != nil {
			return []error{e}
		}
		return nil
	}
	if u, ok := err.(interface{ Unwrap() []error }); ok {
		return u.Unwrap()
	}
	return nil
}

// matchAsTarget reports whether a chain element satisfies an errors.As
// target's element typedef: an interface target (`var e error; &e`)
// accepts an element implementing its full required method set, a
// concrete target matches the element's declared type exactly — name +
// package, the way reflect.TypeOf(err) == elem(target) works — and a
// GoValue-boxed host error, which carries no typedef of its own,
// matches on its reflect type (*strconv.NumError).
func matchAsTarget(v runtime.VMCaller, sv runtime.Value, want *runtime.TypeDef, reqs map[string]bool) bool {
	if want == nil {
		return true
	}
	if want.Kind == runtime.KindInterface {
		// interface targets check the element's method set —
		// AsType[Extra](E{}) where E offers only Error() misses, the
		// way gc's AssignableTo rejects it.
		for m := range reqs {
			if _, ok := runtime.IfaceMember(v, sv, m); !ok {
				return false
			}
		}
		return true
	}
	if sameErrTyp(v, v.TypeOf(sv), want) {
		return true
	}
	if gv, ok := sv.(*runtime.GoValue); ok {
		return sameErrTypHost(v, reflect.TypeOf(gv.V), want)
	}
	return false
}

// sameErrTypHost is the GoValue side of sameErrTyp: it compares a chain
// element's concrete host type with the As target's element typedef.
// Pointer depth must match first; the leaf compares a bound typedef's
// "pkg/path.Name" spelling with reflect's PkgPath+Name, which cannot
// collide with a script-declared type's short name.
func sameErrTypHost(v runtime.VMCaller, rt reflect.Type, want *runtime.TypeDef) bool {
	if rt == nil || want == nil {
		return false
	}
	wt := want
	for rt.Kind() == reflect.Pointer {
		if wt == nil || wt.Kind != runtime.KindPointer {
			return false
		}
		pt := wt
		wt = wt.Elem
		if wt == nil {
			// a typedef may carry the pointee only in Anon — let the VM
			// resolve the element zero instead of walking the AST here.
			wt = v.TypeOf(v.ElemZero(pt))
			if wt == nil {
				return false
			}
		}
		rt = rt.Elem()
	}
	if wt == nil || wt.Kind == runtime.KindPointer {
		return false
	}
	if wt.Name == "" || rt.Name() == "" {
		return false
	}
	return wt.Name == rt.PkgPath()+"."+rt.Name()
}

// sameErrTyp compares a chain element's dynamic typedef with the As
// target's element typedef: pointer depth must match (Go requires
// reflect.TypeOf(err) == reflect.TypeOf(target).Elem()), and leaf
// identity is name + package path — leaf spelling alone lets two
// packages declaring the same error type name match each other.
func sameErrTyp(v runtime.VMCaller, st, want *runtime.TypeDef) bool {
	if st == nil || want == nil || st.Kind != want.Kind {
		return false
	}
	if st == want {
		return true
	}
	stl, wl := st, want
	if st.Kind == runtime.KindPointer {
		stl, wl = st.Elem, want.Elem
		// a typedef may carry the pointee only in Anon — let the VM
		// resolve the element zero instead of walking the AST here.
		if stl == nil {
			stl = v.TypeOf(v.ElemZero(st))
		}
		if wl == nil {
			wl = v.TypeOf(v.ElemZero(want))
		}
	}
	if stl == nil || wl == nil {
		return false
	}
	if stl == wl {
		return true
	}
	if stl.Name == "" || stl.Name != wl.Name {
		return false
	}
	sp, wp := "", ""
	if stl.Pkg != nil {
		sp = stl.Pkg.Path
	}
	if wl.Pkg != nil {
		wp = wl.Pkg.Path
	}
	return sp == wp
}

// rewriteWrapVerbs rewrites %w verbs to %v and reports the LAST %w's
// argument position (-1 when absent); Go keeps the last %w's arg as the
// wrapError's cause.
func rewriteWrapVerbs(spec string) (string, int) {
	if !strings.Contains(spec, "%w") {
		return spec, -1
	}
	var sb strings.Builder
	wrapPos := -1
	seq := 0
	for i := 0; i < len(spec); {
		j := strings.IndexByte(spec[i:], '%')
		if j < 0 {
			sb.WriteString(spec[i:])
			break
		}
		sb.WriteString(spec[i : i+j])
		i += j
		pct := i
		i++
		for i < len(spec) && (spec[i] == '#' || spec[i] == '+' || spec[i] == '-' || spec[i] == ' ' || spec[i] == '.' || (spec[i] >= '0' && spec[i] <= '9') || spec[i] == '[' || spec[i] == ']' || spec[i] == '*') {
			i++
		}
		if i >= len(spec) {
			sb.WriteString(spec[pct:])
			break
		}
		verb := spec[i]
		i++
		switch verb {
		case '%':
			sb.WriteString("%%")
		case 'w':
			sb.WriteString("%v")
			wrapPos = seq
			seq++
		default:
			sb.WriteString(spec[pct:i])
			seq++
		}
	}
	return sb.String(), wrapPos
}

// ffn wraps a host fmt-style function: each arg becomes fmtArg so host
// fmt sees the script-shaped wrapper. formatAt is the index of the
// format-string arg (-1 when none); %T verbs there are rewritten to %s
// over the value's script type spelling because host fmt never calls
// Formatter.Format for %T/%p.
func (h *hostHelpers) ffn(name string, formatAt, minArgs int, f func([]any) (any, error), target ...any) *runtime.BuiltinFunc {
	return h.vffn(name, formatAt, minArgs, func(_ runtime.VMCaller, a []any) (any, error) {
		return f(a)
	}, target...)
}

// pffn is vffn without a format string whose inner fn also receives the
// flat script args — the Print family's operand spacing keys on the
// script-level string kind, which fmtArgs hides inside fmtValue.
func (h *hostHelpers) pffn(name string, minArgs int, f func(runtime.VMCaller, []any, []runtime.Value) (any, error), target ...any) *runtime.BuiltinFunc {
	return h.wffn(name, -1, minArgs, f, target...)
}

// vffn is ffn whose inner fn also receives the VM caller — needed when
// an argument must call back into the script (a script-defined
// io.Writer for fmt.Fprintf, say).
func (h *hostHelpers) vffn(name string, formatAt, minArgs int, f func(runtime.VMCaller, []any) (any, error), target ...any) *runtime.BuiltinFunc {
	return h.wffn(name, formatAt, minArgs, func(v runtime.VMCaller, a []any, _ []runtime.Value) (any, error) {
		return f(v, a)
	}, target...)
}

// wffn is the shared fmt-call wrapper: the inner fn receives both the
// host-shaped args and the flat script args (before fmtArg unboxing).
func (h *hostHelpers) wffn(name string, formatAt, minArgs int, f func(runtime.VMCaller, []any, []runtime.Value) (any, error), target ...any) *runtime.BuiltinFunc {
	bf := &runtime.BuiltinFunc{Name: name, Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) < minArgs {
			return nil, fmt.Errorf("%s needs %d args, got %d", name, minArgs, len(args))
		}
		a, flat := fmtArgs(v, args)
		if formatAt >= 0 && formatAt < len(a) {
			spec, ok := a[formatAt].(string)
			if !ok && formatAt < len(flat) {
				// the spec may reach the call as a script value — a
				// named string, a cell holding one — that fmtArg boxed
				// for tag-aware rendering. The spec itself must be a
				// plain string for host fmt and for %T/%p rewriting.
				spec, ok = formatString(flat[formatAt])
			}
			if ok {
				ns, tail := rewriteTypeVerbs(spec, a, flat, formatAt, v)
				a[formatAt] = ns
				a = append(a[:formatAt+1], tail...)
			}
		}
		r, err := f(v, a, flat)
		if err != nil {
			return nil, err
		}
		return scriptVal(r), nil
	}}
	if len(target) > 0 {
		bf.Target = target[0]
	}
	return bf
}

// fmtValue wraps one script value for host fmt: Format renders %v/%+v/%#v
// with Go's composite layout and honors script String/Error/GoString.
type fmtValue struct {
	c     runtime.VMCaller
	x     runtime.Value
	et    *runtime.TypeDef // declared element/field typedef, for bad-verb naming
	depth int
	// nilSyntax spells a nil slice or map in %#v's T(nil) form under %v
	// too, so REPL output tells it apart from an empty one ([] / map[]).
	nilSyntax bool
}

func (s *fmtValue) Format(f fmt.State, verb rune) {
	io.WriteString(f, s.render(verb, f))
}

// zeroState is a flag-less fmt.State for rendering values outside a
// live Format call — %p substitution happens before host fmt runs.
type zeroState struct{}

func (zeroState) Write(b []byte) (int, error) { return len(b), nil }
func (zeroState) Width() (int, bool)          { return 0, false }
func (zeroState) Precision() (int, bool)      { return 0, false }
func (zeroState) Flag(int) bool               { return false }

// captureState delegates formatting flags to the live state but
// records writes: an element inside a composite returns its Format
// output as the element's string for the parent's assembled output —
// writing to the live state would emit the fragment ahead of the
// surrounding brackets (gc renders elements through a sub-buffer).
type captureState struct {
	f   fmt.State
	buf bytes.Buffer
}

func (s *captureState) Write(b []byte) (int, error) { return s.buf.Write(b) }
func (s *captureState) Width() (int, bool)          { return s.f.Width() }
func (s *captureState) Precision() (int, bool)      { return s.f.Precision() }
func (s *captureState) Flag(ch int) bool            { return s.f.Flag(ch) }

// nested returns the state a value inside a composite renders against —
// a capture of the live state; internal stand-ins pass through.
func nested(f fmt.State) fmt.State {
	switch f.(type) {
	case zeroState, *captureState:
		return f
	}
	return &captureState{f: f}
}

// ptrSpelling renders a value for %p — host fmt never calls Format on
// %p so rewriteTypeVerbs substitutes this string. Typed nils print 0x0
// like Go; slices, maps, chans and script pointers print an address;
// anything else produces Go's %!p(type=value) marker.
func ptrSpelling(c runtime.VMCaller, x runtime.Value) string {
	zs := zeroState{}
	switch t := x.(type) {
	case runtime.Nil:
		return badVerb('p', "", "<nil>")
	case *runtime.TypedNil:
		if t.Typ != nil {
			switch t.Typ.Kind {
			case runtime.KindSlice, runtime.KindMap, runtime.KindChan,
				runtime.KindFunc, runtime.KindPointer:
				return "0x0"
			}
		}
		return badVerb('p', "", "<nil>")
	case *runtime.IfaceNil:
		// a non-nil interface holding a nil pointer still prints 0x0
		return "0x0"
	case *runtime.GoValue:
		if rv, ok := t.V.(*minireflect.RValue); ok {
			// %p never unwraps the Value — Go emits the
			// %!p(reflect.Value=<underlying %v>) bad-verb form.
			return badVerb('p', "reflect.Value", fmt.Sprintf("%v", fmtRValue(c, rv)))
		}
		return fmt.Sprintf("%p", t.V)
	case *runtime.Slice:
		if isArrayTyp(t.Typ) {
			fv := &fmtValue{c: c, x: t}
			return badVerb('p', typedefSpelling(t.Typ), fv.renderList(t, 'v', zs))
		}
		if len(t.Elems) == 0 {
			return "0x0"
		}
		return fmt.Sprintf("%p", t)
	case *runtime.Map, *runtime.Chan,
		*runtime.Cell, *runtime.FieldRef, *runtime.IndexRef,
		*runtime.Function, *runtime.Closure, *runtime.BoundMethod, *runtime.BuiltinFunc:
		return fmt.Sprintf("%p", t)
	case *runtime.Struct:
		fv := &fmtValue{c: c, x: t}
		return badVerb('p', typedefSpelling(t.Def), fv.render('v', zs))
	case *runtime.Named:
		return ptrSpelling(c, t.V)
	case *runtime.UConst:
		if nv, err := uconstNative(t); err == nil {
			return ptrSpelling(c, nv)
		}
	}
	fv := &fmtValue{c: c, x: x}
	return badVerb('p', scriptScalarName(x), fv.render('v', zs))
}

func (s *fmtValue) render(verb rune, f fmt.State) string {
	// fmt consults Formatter before the Stringer family, for every verb
	// but %T/%p — a script Formatter writes to the live state itself, so
	// a successful call already emitted its output. zeroState is the
	// flag-less stand-in for nested renderings (bad-verb markers, %p)
	// where Go invokes no Formatter; nothing real writes to it.
	if s.c != nil && verb != 'T' && verb != 'p' {
		if cs, isCap := f.(*captureState); isCap {
			if callFormat(s.c, s.x, cs, verb) {
				return cs.buf.String()
			}
		} else if _, isZero := f.(zeroState); !isZero && callFormat(s.c, s.x, f, verb) {
			return ""
		}
	}
	// Stringer family first, like fmt does.
	if s.c != nil {
		switch verb {
		case 'v':
			if f.Flag('#') {
				// %#v consults GoString, not String/Error.
				if str, ok := runtime.IfaceCallString(s.c, s.x, "GoString"); ok {
					return str
				}
				break
			}
			// Go's fmt consults error before Stringer — a value
			// implementing both prints its Error().
			if str, ok := runtime.IfaceCallString(s.c, s.x, "Error"); ok {
				return withWidth(f, str)
			}
			if str, ok := runtime.IfaceCallString(s.c, s.x, "String"); ok {
				return withWidth(f, str)
			}
		case 's':
			if str, ok := runtime.IfaceCallString(s.c, s.x, "Error"); ok {
				return withWidth(f, str)
			}
			if str, ok := runtime.IfaceCallString(s.c, s.x, "String"); ok {
				return withWidth(f, str)
			}
		case 'q':
			if str, ok := runtime.IfaceCallString(s.c, s.x, "Error"); ok {
				return strconv.Quote(str)
			}
			if str, ok := runtime.IfaceCallString(s.c, s.x, "String"); ok {
				return strconv.Quote(str)
			}
		case 'x', 'X':
			if str, ok := runtime.IfaceCallString(s.c, s.x, "Error"); ok {
				return fmt.Sprintf("%"+string(verb), str)
			}
			if str, ok := runtime.IfaceCallString(s.c, s.x, "String"); ok {
				return fmt.Sprintf("%"+string(verb), str)
			}
		}
	}
	return s.renderValue(s.x, verb, f)
}

func (s *fmtValue) renderValue(x runtime.Value, verb rune, f fmt.State) string {
	// Go's fmt has no render-depth cap — a deeply nested composite
	// prints every level (issue29264), and a cyclic one overflows the
	// host stack just as it does under go run.
	switch v := x.(type) {
	case *runtime.Named:
		// %T keeps the declared name; other verbs render through.
		if verb == 'T' {
			if runtime.IfaceTaggedNil(v.V) != nil {
				return "<nil>" // a nil interface has no dynamic type
			}
			return typedefSpelling(v.Typ)
		}
		// a bound host scalar (time.Month/time.Duration) whose payload is
		// still script-shaped — a stored int64 or a const-domain UConst —
		// renders through the host scalar so String() and every verb
		// apply like gc (%v → "January", %d → 1).
		if td := v.Typ; td != nil && td.HostScalar != nil {
			if hv, ok := runtime.HostScalarOf(td, v.V); ok {
				return fmt.Sprintf(formatOf(f, verb), hv)
			}
		}
		// an int64 carrying a uint64/uintptr tag must format its bits as
		// unsigned — host fmt would read the int64 as signed otherwise.
		if unsignedIntTyp(v.Typ) {
			if iv, ok := v.V.(int64); ok {
				return fmt.Sprintf(formatOf(f, verb), uint64(iv))
			}
		}
		// a float64 carrying a float32 tag formats in float32 — the
		// rounded value, not the wider box's digits. A payload still
		// riding the constant domain reads at float32 width the same
		// way (`float32(c)` conversions keep UConst).
		if float32Tag(v.Typ) {
			if fv, ok := v.V.(float64); ok {
				return fmt.Sprintf(formatOf(f, verb), float32(fv))
			}
			if uc, ok := v.V.(*runtime.UConst); ok {
				if nv, err := uconstNative(uc); err == nil {
					if fv, ok := nv.(float64); ok {
						return fmt.Sprintf(formatOf(f, verb), float32(fv))
					}
				}
			}
		}
		// a complex128 carrying a complex64 tag rounds each half to
		// float32 — same gap as float32, one level wider.
		if complex64Tag(v.Typ) {
			if cv, ok := complex64Value(v.V); ok {
				return fmt.Sprintf(formatOf(f, verb), cv)
			}
		}
		return (&fmtValue{c: s.c, nilSyntax: s.nilSyntax, x: v.V, et: v.Typ, depth: s.depth + 1}).render(verb, f)
	case *runtime.Cell, *runtime.FieldRef, *runtime.IndexRef:
		dv, ok := runtime.Deref(v)
		if !ok {
			return "<nil>"
		}
		// Go's printPtr descends one level only when the pointee is a
		// composite — struct, array, slice, or map — spelling `&[...]`/
		// `&{...}`/`&map[...]`; every other pointee (scalars, other
		// pointers, chans, funcs) reads as the address. The descent only
		// happens at the top level: a pointer nested inside a composite
		// prints 0x... — []*S{p} renders [0xADDR], and under %#v
		// [(*S)(0xADDR)].
		composite := func(x runtime.Value) bool {
			switch t := x.(type) {
			case *runtime.Struct, *runtime.Slice, *runtime.Map:
				return true
			case *runtime.TypedNil:
				// &ns for a nil slice/map spells &[]/&map[]
				return t.Typ != nil &&
					(t.Typ.Kind == runtime.KindSlice || t.Typ.Kind == runtime.KindMap)
			}
			return false
		}
		if s.depth == 0 && composite(dv) {
			return "&" + (&fmtValue{c: s.c, nilSyntax: s.nilSyntax, x: dv, depth: s.depth + 1}).render(verb, nested(f))
		}
		if s.depth == 0 {
			if n, isNamed := dv.(*runtime.Named); isNamed && composite(n.V) {
				return "&" + (&fmtValue{c: s.c, nilSyntax: s.nilSyntax, x: n.V, depth: s.depth + 1}).render(verb, nested(f))
			}
		}
		// scalar pointer: Go prints the address — a host pointer repr
		// is the closest readable stand-in; %#v wraps it as (*T)(0xADDR).
		if verb == 'v' && f.Flag('#') {
			return fmt.Sprintf("(*%s)(%p)", scriptTypeString(dv), v)
		}
		return fmt.Sprintf("%p", v)
	case *runtime.Struct:
		switch verb {
		case 'T':
			return typedefSpelling(v.Def)
		case 'q':
			return strconv.Quote(s.renderValue(x, 'v', f))
		}
		parts := make([]string, len(v.Fields))
		for i, e := range v.Fields {
			fv := (&fmtValue{c: s.c, nilSyntax: s.nilSyntax, x: e, et: fieldTypOf(v.Def, i), depth: s.depth + 1}).render(elemVerb(verb), nested(f))
			if f.Flag('+') && i < len(v.Def.Fields) {
				fv = v.Def.Fields[i] + ":" + fv
			}
			if f.Flag('#') && i < len(v.Def.Fields) {
				fv = v.Def.Fields[i] + ":" + fv
			}
			parts[i] = fv
		}
		sep := " "
		if f.Flag('#') {
			sep = ", "
		}
		body := strings.Join(parts, sep)
		if f.Flag('#') {
			return typedefSpelling(v.Def) + "{" + body + "}"
		}
		return "{" + body + "}"
	case *runtime.Slice:
		switch verb {
		case 'T':
			return typedefSpelling(v.Typ)
		case 'p':
			if isArrayTyp(v.Typ) {
				return badVerb(verb, typedefSpelling(v.Typ), s.renderList(v, 'v', f))
			}
			return fmt.Sprintf("%p", v)
		case 's', 'q', 'x', 'X':
			// a byte-wise slice formats as text like Go's fmt does
			if bs, ok := sliceBytes(v); ok {
				switch verb {
				case 's':
					return string(bs)
				case 'q':
					return strconv.Quote(string(bs))
				default:
					return fmt.Sprintf(formatOf(f, verb), bs)
				}
			}
		case 'v':
			// %#v renders a byte slice as []byte{0xNN, ...} — the byte
			// spelling applies only at the top level (reflect cannot
			// distinguish byte from uint8 below it).
			if f.Flag('#') && byteSliceTyp(v.Typ) {
				if bs, ok := sliceBytes(v); ok {
					spell := typedefSpelling(v.Typ)
					if v.Typ.Name == "" && s.depth == 0 && !isArrayTyp(v.Typ) {
						spell = "[]byte"
					}
					parts := make([]string, len(bs))
					for i, b := range bs {
						parts[i] = fmt.Sprintf("0x%x", b)
					}
					return spell + "{" + strings.Join(parts, ", ") + "}"
				}
			}
		}
		return s.renderList(v, verb, f)
	case *runtime.Map:
		switch verb {
		case 'T':
			return typedefSpelling(v.Typ)
		case 'p':
			return fmt.Sprintf("%p", v)
		}
		// Go's fmt prints maps in sorted-key order (fmtsort), not
		// insertion order — sort a copy of Order the same way.
		order := append([]runtime.Value{}, v.Order...)
		sort.SliceStable(order, func(i, j int) bool {
			a, b := order[i], order[j]
			if lessScript(a, b) {
				return true
			}
			if lessScript(b, a) {
				return false
			}
			// unordered kinds (bool, composites, mixed): order by the
			// rendered key like fmtsort's fallback.
			ka := (&fmtValue{c: s.c, nilSyntax: s.nilSyntax, x: a, depth: s.depth + 1}).render('v', nested(f))
			kb := (&fmtValue{c: s.c, nilSyntax: s.nilSyntax, x: b, depth: s.depth + 1}).render('v', nested(f))
			return ka < kb
		})
		kt, vt := mapElemTyps(v.Typ)
		ev := elemVerb(verb)
		parts := make([]string, 0, len(order))
		for _, k := range order {
			e, _ := v.Get(k)
			kr := (&fmtValue{c: s.c, nilSyntax: s.nilSyntax, x: k, et: kt, depth: s.depth + 1}).render(ev, nested(f))
			vr := (&fmtValue{c: s.c, nilSyntax: s.nilSyntax, x: e, et: vt, depth: s.depth + 1}).render(ev, nested(f))
			parts = append(parts, kr+":"+vr)
		}
		if f.Flag('#') {
			return typedefSpelling(v.Typ) + "{" + strings.Join(parts, ", ") + "}"
		}
		return "map[" + strings.Join(parts, " ") + "]"
	case *runtime.Tuple:
		parts := make([]string, len(v.Elems))
		for i, e := range v.Elems {
			parts[i] = (&fmtValue{c: s.c, nilSyntax: s.nilSyntax, x: e, depth: s.depth + 1}).render(verb, nested(f))
		}
		return strings.Join(parts, " ")
	case *runtime.TypedNil:
		if verb == 'T' {
			return typedefSpelling(v.Typ)
		}
		return s.nilTyp(verb, f, v.Typ)
	case *runtime.IfaceNil:
		if v.Typ != nil && v.Typ.Kind == runtime.KindInterface {
			// a nil interface value prints like a bare nil — <nil> at
			// top level, the slot's static type inside %#v composites.
			if verb == 'v' && f.Flag('#') && s.et != nil {
				return nilGoSyntax(s.et)
			}
			if verb == 'T' || verb == 'v' || s.depth > 0 || s.et != nil {
				return "<nil>"
			}
			return badVerb(verb, "", "<nil>")
		}
		if verb == 'T' {
			return typedefSpelling(v.Typ)
		}
		return s.nilTyp(verb, f, v.Typ)
	case runtime.Nil:
		// %#v spells a nil sitting in an interface-typed field or
		// element with the slot's static type — error(nil),
		// interface {}(nil) — the way reflect sees the declared type.
		// Every other verb (and an untyped top-level nil) keeps <nil>.
		if verb == 'v' && f.Flag('#') && s.et != nil {
			return nilGoSyntax(s.et)
		}
		// a nil interface element inside a composite renders <nil>
		// under every verb; top-level non-%v verbs get the marker.
		if verb == 'T' || verb == 'v' || s.depth > 0 || s.et != nil {
			return "<nil>"
		}
		return badVerb(verb, "", "<nil>")
	case *runtime.Chan:
		switch verb {
		case 'T':
			return typedefSpelling(v.Typ)
		case 'v':
			// %#v spells pointer-shaped values as (T)(0xADDR).
			if f.Flag('#') {
				return fmt.Sprintf("(%s)(%p)", chanTypSpelling(v.Typ), v)
			}
			return fmt.Sprintf("%p", v)
		case 'p':
			return fmt.Sprintf("%p", v)
		}
		return badVerb(verb, typedefSpelling(v.Typ), fmt.Sprintf("%p", v))
	case *runtime.Function, *runtime.Closure, *runtime.BoundMethod, *runtime.BuiltinFunc:
		switch verb {
		case 'T':
			return funcSigSpelling(v)
		case 'v':
			if f.Flag('#') {
				return fmt.Sprintf("(%s)(%p)", funcSigSpelling(v), v)
			}
			return fmt.Sprintf("%p", v)
		case 'p':
			return fmt.Sprintf("%p", v)
		}
		return badVerb(verb, funcSigSpelling(v), fmt.Sprintf("%p", v))
	case *runtime.GoValue:
		if verb == 'T' {
			// a proxy box spells its API type, not the box's name.
			if rt := proxyHostTypeOf(v.V); rt != nil {
				return rt.String()
			}
		}
		return fmt.Sprintf(formatOf(f, verb), v.V)
	case *runtime.TypeDef:
		if verb == 'T' {
			return "type"
		}
		return typedefSpelling(v)
	}
	return s.leaf(x, verb, f)
}

// renderList renders a slice's bracketed element list — every verb
// descends elementwise like Go's fmt ([]int under %q quotes each element
// as a char, under %s each becomes a %!s marker).
func (s *fmtValue) renderList(v *runtime.Slice, verb rune, f fmt.State) string {
	et := sliceElemTyp(v.Typ)
	ev := elemVerb(verb)
	parts := make([]string, len(v.Elems))
	for i, e := range v.Elems {
		parts[i] = (&fmtValue{c: s.c, nilSyntax: s.nilSyntax, x: e, et: et, depth: s.depth + 1}).render(ev, nested(f))
	}
	if f.Flag('#') && verb == 'v' {
		return typedefSpelling(v.Typ) + "{" + strings.Join(parts, ", ") + "}"
	}
	return "[" + strings.Join(parts, " ") + "]"
}

// leaf formats a scalar value: compatible verbs go to host fmt after
// normalizing to the script-canonical Go type (script ints are int64 but
// spell int), incompatible verbs produce Go's %!verb(type=value) marker
// spelled with the declared element type when the leaf has one.
func (s *fmtValue) leaf(x runtime.Value, verb rune, f fmt.State) string {
	if u, ok := x.(*runtime.UConst); ok {
		x = mustNativeConst(u)
	}
	if leafBadVerb(x, verb) {
		name := ""
		// an interface-typed element (incl. any) names the value's
		// dynamic type, not the interface — []any{1} gives %!v(int=1)
		if s.et != nil && s.et.Name != "any" {
			if _, isIface := typedefAst(s.et).(*ast.InterfaceType); !isIface {
				name = typedefSpelling(s.et)
			}
		}
		if name == "" {
			name = scriptScalarName(x)
		}
		return badVerb(verb, name, (&fmtValue{c: s.c, nilSyntax: s.nilSyntax, x: x, depth: s.depth + 1}).render('v', nested(f)))
	}
	if i, ok := x.(int64); ok {
		// script ints store int64 but spell int — including inside
		// composites, where %#v elements stay decimal (the 0x-hex int
		// spelling is a top-level-only form).
		if verb == 'v' && f.Flag('#') {
			return fmt.Sprintf(formatOfNoHash(f, verb), int(i))
		}
		return fmt.Sprintf(formatOf(f, verb), int(i))
	}
	return fmt.Sprintf(formatOf(f, verb), x)
}

// nilTyp renders a typed nil like Go's fmt: a nil slice or map prints its
// empty composite ([] or map[]) under every verb — a nil []byte renders
// like the empty string — while a nil pointer, chan or func prints
// <nil>/0x0/0 like the nil address it is. %#v uses the Go-syntax
// conversion form T(nil): []string(nil), map[string]int(nil), (*int)(nil).
func (s *fmtValue) nilTyp(verb rune, f fmt.State, td *runtime.TypeDef) string {
	if verb == 'v' && f.Flag('#') {
		return nilGoSyntax(td)
	}
	if verb == 'v' && s.nilSyntax && td != nil &&
		(td.Kind == runtime.KindSlice || td.Kind == runtime.KindMap) {
		return nilGoSyntax(td)
	}
	if td == nil {
		return "<nil>"
	}
	switch td.Kind {
	case runtime.KindSlice:
		if byteSliceTyp(td) {
			switch verb {
			case 's', 'x', 'X':
				return ""
			case 'q':
				return strconv.Quote("")
			}
		}
		if verb == 'p' {
			return "0x0"
		}
		return "[]"
	case runtime.KindMap:
		if verb == 'p' {
			return "0x0"
		}
		return "map[]"
	case runtime.KindPointer, runtime.KindChan, runtime.KindFunc:
		switch verb {
		case 'v':
			return "<nil>"
		case 'p':
			return "0x0"
		case 'd', 'o', 'b', 'x', 'X', 'U', 'c':
			// a nil address formats as 0 under the numeric verbs
			return fmt.Sprintf(formatOf(f, verb), 0)
		}
		return badVerb(verb, typedefSpelling(td), "<nil>")
	}
	return "<nil>"
}

// nilGoSyntax spells a typed nil the way Go's %#v does — T(nil), with
// parens around the anonymous pointer/chan/func spellings that need them
// and the top-level []byte special case for byte slices.
func nilGoSyntax(td *runtime.TypeDef) string {
	if td == nil {
		return "<nil>"
	}
	if td.Name == "" {
		switch td.Kind {
		case runtime.KindPointer, runtime.KindChan, runtime.KindFunc:
			return "(" + typedefSpelling(td) + ")(nil)"
		case runtime.KindSlice:
			if byteSliceTyp(td) {
				return "[]byte(nil)"
			}
		}
	}
	return typedefSpelling(td) + "(nil)"
}

// badVerb emits Go's %!verb(type=value) marker for an incompatible verb.
func badVerb(verb rune, typ, val string) string {
	if typ == "" {
		return fmt.Sprintf("%%!%c(%s)", verb, val)
	}
	return fmt.Sprintf("%%!%c(%s=%s)", verb, typ, val)
}

// leafBadVerb reports whether verb is incompatible with a scalar leaf —
// Go's fmt then emits %!verb(type=value) instead of the value.
func leafBadVerb(x runtime.Value, verb rune) bool {
	switch x.(type) {
	case string:
		return !strings.ContainsRune("sqxXv", verb)
	case bool:
		return !strings.ContainsRune("tv", verb)
	case float64:
		return !strings.ContainsRune("eEfFgGxXbv", verb)
	case int64:
		return !strings.ContainsRune("dobcxXUqcv", verb)
	}
	return false
}

// scriptScalarName names a scalar leaf the way Go's fmt spells its type
// inside a bad-verb marker.
func scriptScalarName(x runtime.Value) string {
	switch t := x.(type) {
	case string:
		return "string"
	case bool:
		return "bool"
	case float64:
		return "float64"
	case int64:
		return "int"
	case *runtime.UConst:
		if nv, err := uconstNative(t); err == nil {
			return scriptScalarName(nv)
		}
	case *runtime.Named:
		return typedefSpelling(t.Typ)
	case *runtime.GoValue:
		return scriptTypeString(t)
	}
	return fmt.Sprintf("%T", x)
}

// typedefAst returns the underlying type AST of a typedef — Anon for
// anonymous types, Spec.Type for declared ones.
func typedefAst(td *runtime.TypeDef) ast.Expr {
	if td == nil {
		return nil
	}
	if td.Anon != nil {
		return td.Anon
	}
	if td.Spec != nil {
		return td.Spec.Type
	}
	return nil
}

// isArrayTyp reports whether td denotes a fixed-length array ([N]T) —
// arrays have no addressable backing pointer under %p.
func isArrayTyp(td *runtime.TypeDef) bool {
	at, ok := typedefAst(td).(*ast.ArrayType)
	return ok && at.Len != nil
}

// byteSliceTyp reports whether td's element type is byte/uint8 — the one
// slice fmt renders as text rather than a bracketed list.
func byteSliceTyp(td *runtime.TypeDef) bool {
	if td == nil {
		return false
	}
	if td.Elem != nil {
		return typedefSpelling(td.Elem) == "uint8"
	}
	at, ok := typedefAst(td).(*ast.ArrayType)
	if !ok {
		return false
	}
	id, ok := at.Elt.(*ast.Ident)
	return ok && (id.Name == "byte" || id.Name == "uint8")
}

// elemTyp wraps a composite element's type AST so leaf rendering can
// spell bad-verb markers with the declared element type. Only idents
// naming a declared type carry the package — builtin type names spell
// unqualified like Go.
func elemTyp(e ast.Expr, pkg *runtime.Package) *runtime.TypeDef {
	if e == nil {
		return nil
	}
	if id, ok := e.(*ast.Ident); ok {
		td := &runtime.TypeDef{Name: id.Name, Kind: runtime.KindNamedBasic}
		if pkg != nil && pkg.Index != nil {
			if _, ok := pkg.Index.Types[id.Name]; ok {
				td.Pkg = pkg
			}
		}
		return td
	}
	return &runtime.TypeDef{Pkg: pkg, Anon: e}
}

// sliceElemTyp resolves a slice/array typedef's element typedef.
func sliceElemTyp(td *runtime.TypeDef) *runtime.TypeDef {
	if td == nil {
		return nil
	}
	if td.Elem != nil {
		return td.Elem
	}
	if at, ok := typedefAst(td).(*ast.ArrayType); ok {
		return bindTyp(td, elemTyp(at.Elt, td.Pkg))
	}
	return nil
}

// mapElemTyps resolves a map typedef's key and value typedefs.
func mapElemTyps(td *runtime.TypeDef) (key, val *runtime.TypeDef) {
	if mt, ok := typedefAst(td).(*ast.MapType); ok && td != nil {
		return bindTyp(td, elemTyp(mt.Key, td.Pkg)), bindTyp(td, elemTyp(mt.Value, td.Pkg))
	}
	return nil, nil
}

// fieldTypOf resolves the i'th field's typedef from a struct typedef's
// declaration — embedded fields count once and multi-name decls expand.
func fieldTypOf(td *runtime.TypeDef, i int) *runtime.TypeDef {
	st, ok := typedefAst(td).(*ast.StructType)
	if !ok {
		return nil
	}
	n := 0
	for _, fd := range st.Fields.List {
		cnt := len(fd.Names)
		if cnt == 0 {
			cnt = 1
		}
		if i < n+cnt {
			return bindTyp(td, elemTyp(fd.Type, td.Pkg))
		}
		n += cnt
	}
	return nil
}

// bindTyp resolves a declared element typedef through the enclosing
// typedef's instantiation binds — a `Pair[error, any]` `Key K` field's
// declared type is `K`, but a value stored there statically has the
// bound argument's type. Composite field types keep the binds so a
// nested type-parameter name (a `[]K` field's element) resolves the
// same way one level down.
func bindTyp(td *runtime.TypeDef, et *runtime.TypeDef) *runtime.TypeDef {
	if td == nil || et == nil || len(td.Binds) == 0 {
		return et
	}
	if et.Anon == nil && et.Spec == nil {
		if bv, ok := td.Binds[et.Name]; ok {
			if btd, ok := bv.(*runtime.TypeDef); ok {
				return btd
			}
		}
	}
	if len(et.Binds) == 0 {
		cp := *et
		cp.ResetCaches()
		cp.Binds = td.Binds
		return &cp
	}
	return et
}

// formatOfNoHash is formatOf without the '#' flag — for scalar leaves
// inside composites, where %#v keeps decimal ints (the 0x spelling is a
// top-level-only form).
func formatOfNoHash(f fmt.State, verb rune) string {
	var sb strings.Builder
	sb.WriteByte('%')
	for _, c := range "+- 0" {
		if f.Flag(int(c)) {
			sb.WriteByte(byte(c))
		}
	}
	if w, ok := f.Width(); ok {
		sb.WriteString(strconv.Itoa(w))
	}
	if p, ok := f.Precision(); ok {
		sb.WriteString("." + strconv.Itoa(p))
	}
	sb.WriteRune(verb)
	return sb.String()
}

// unsignedIntTyp reports whether td denotes an unsigned integer — the
// declared name or its underlying ident (a `type U uint64` decl).
// Unsigned kinds format their int64-carried bits as unsigned, and %#v
// spells them in hex like Go.
func unsignedIntTyp(td *runtime.TypeDef) bool {
	if td == nil {
		return false
	}
	switch td.Name {
	case "uint", "uint8", "uint16", "uint32", "uint64", "uintptr", "byte":
		return true
	}
	x := td.Anon
	if x == nil && td.Spec != nil {
		x = td.Spec.Type
	}
	if id, ok := x.(*ast.Ident); ok {
		switch id.Name {
		case "uint", "uint8", "uint16", "uint32", "uint64", "uintptr", "byte":
			return true
		}
	}
	return false
}

// elemVerb picks the verb applied to elements inside a composite: every
// verb descends elementwise like Go's fmt — []int under %q quotes each
// element as a char, under %s each becomes a bad-verb marker — except
// %T which describes the composite itself, not the elements.
func elemVerb(verb rune) rune {
	if verb == 'T' {
		return 'v'
	}
	return verb
}

// sliceBytes reports the slice as bytes when its element type is
// byte/uint8 and every element is an int in byte range — the one slice
// fmt renders as text, not a bracketed list.
func sliceBytes(v *runtime.Slice) ([]byte, bool) {
	if !byteSliceTyp(v.Typ) {
		return nil, false
	}
	bs := make([]byte, len(v.Elems))
	for i, e := range v.Elems {
		n, ok := runtime.Unwrap(e).(int64)
		if !ok || n < 0 || n > 255 {
			return nil, false
		}
		bs[i] = byte(n)
	}
	return bs, true
}

// funcSigSpelling renders a function value's signature for %T and the
// %#v pointer wrapper — `func(int) int`, `func(...int)` — falling back
// to `func()` when the declaration isn't reachable (plain builtins).
func funcSigSpelling(v runtime.Value) string {
	if s, ok := runtime.FuncGoSpelling(v); ok {
		return s
	}
	if bf, ok := v.(*runtime.BuiltinFunc); ok && bf.Target != nil {
		// a bound host func spells its real signature — host fmt's
		// %T of a func IS the signature (func(...interface {}) (int, error))
		return fmt.Sprintf("%T", bf.Target)
	}
	return "func()"
}

// chanTypSpelling spells a channel's typedef for the %#v wrapper —
// `chan int`/`chan<- T`/`<-chan T` — `chan interface{}` when unknown.
func chanTypSpelling(td *runtime.TypeDef) string {
	if td == nil {
		return "chan interface{}"
	}
	return typedefSpelling(td)
}

// nilIfaceTyp reports the interface typedef when x is a nil interface
// value — an iface-kind IfaceNil, possibly Named-wrapped — and nil for
// any other value. A nil interface has no dynamic type: %T spells <nil>
// at top level, while under a pointer the declared typedef still shows.
func nilIfaceTyp(x runtime.Value) *runtime.TypeDef {
	if n, ok := x.(*runtime.Named); ok {
		x = n.V
	}
	return runtime.IfaceTaggedNil(x)
}

// scriptTypeString spells a value's type the way Go's %T does —
// "[]int", "main.Point" — using the typedef, not the Go wrapper type.
func scriptTypeString(x runtime.Value) string {
	switch t := x.(type) {
	case nil, runtime.Nil:
		// a nil interface has no dynamic type — %T spells <nil>.
		return "<nil>"
	case *runtime.UConst:
		return t.DefaultName()
	case *runtime.Named:
		if nilIfaceTyp(t) != nil {
			return "<nil>" // a nil interface has no dynamic type
		}
		return typedefSpelling(t.Typ)
	case *runtime.Struct:
		return typedefSpelling(t.Def)
	case *runtime.Slice:
		return typedefSpelling(t.Typ)
	case *runtime.Map:
		return typedefSpelling(t.Typ)
	case *runtime.Chan:
		return typedefSpelling(t.Typ)
	case *runtime.TypedNil:
		return typedefSpelling(t.Typ)
	case *runtime.IfaceNil:
		if nilIfaceTyp(t) != nil {
			return "<nil>" // a nil interface has no dynamic type
		}
		return typedefSpelling(t.Typ)
	case *runtime.PanicNilError:
		return "*runtime.PanicNilError"
	case *runtime.Cell, *runtime.FieldRef, *runtime.IndexRef:
		if dv, ok := runtime.Deref(t); ok {
			// the pointee slot's stamped declared type names the
			// pointee's static type — `&i` on an interface-typed var
			// spells *main.I, not *main.S of its stored value.
			for _, c := range []*runtime.Cell{cellOf(t), cellOf(dv)} {
				if c != nil && c.Typ != nil {
					return "*" + typedefSpelling(c.Typ)
				}
			}
			// *interface{} / *error keeps the pointee's declared name —
			// the pointer itself is the dynamic type, not <nil>.
			if td := nilIfaceTyp(dv); td != nil {
				return "*" + typedefSpelling(td)
			}
			return "*" + scriptTypeString(dv)
		}
		return "unsafe.Pointer"
	case *runtime.GoValue:
		if se, ok := t.V.(*scriptError); ok {
			return scriptTypeString(se.v)
		}
		switch t.V.(type) {
		case *minireflect.RValue:
			return "reflect.Value"
		case *minireflect.RType:
			return "*reflect.rtype"
		case *minireflect.MapIter:
			return "*reflect.MapIter"
		case *minireflect.StructField:
			return "reflect.StructField"
		case *minireflect.Method:
			return "reflect.Method"
		}
		if rt := proxyHostTypeOf(t.V); rt != nil {
			// a proxy box spells its API type, not the box's name.
			return rt.String()
		}
		return fmt.Sprintf("%T", t.V)
	case int64:
		return "int"
	case float64:
		return "float64"
	case string:
		return "string"
	case bool:
		return "bool"
	case *runtime.Function, *runtime.Closure, *runtime.BoundMethod, *runtime.BuiltinFunc:
		return funcSigSpelling(t)
	default:
		return fmt.Sprintf("%T", x)
	}
}

// formatString recovers a plain string from a script value that reached
// a fmt call's format slot as a named string or a cell holding one.
func formatString(x runtime.Value) (string, bool) {
	for {
		switch v := x.(type) {
		case string:
			return v, true
		case *runtime.Named:
			x = v.V
		case *fmtValue:
			// a bound-fmt result boxed for rendering can land in a
			// named string slot — the spec is the boxed string.
			x = v.x
		default:
			if dv, ok := runtime.Deref(x); ok {
				x = dv
				continue
			}
			return "", false
		}
	}
}

// cellOf peels Named tags to reach the addressable cell underneath —
// the pointee slot whose Typ is the declared (static) type.
func cellOf(v runtime.Value) *runtime.Cell {
	for {
		switch x := v.(type) {
		case *runtime.Cell:
			return x
		case *runtime.Named:
			v = x.V
		default:
			return nil
		}
	}
}

// rewriteTypeVerbs substitutes args for the verbs host fmt handles
// without calling Formatter — %T (type spelling) and %p (address) —
// rewriting each spec verb to %s over a pre-rendered string. Positional
// indexes %[n] count arguments after the format string, as does
// implicit order. The rewrite renumbers every directive to sequential
// order and rebuilds the argument list to match, so a shared operand —
// "%v %[1]p" — keeps its original value for %v while %p sees the
// address string; mutating the operand slot in place would corrupt
// both — the rebuilt list can outgrow the original tail, so callers
// replace a[formatAt+1:] with the returned tail. Exotic mixes of %[n]
// and * in one directive, or operands past the argument list, pass
// through untouched.
func rewriteTypeVerbs(spec string, a []any, rawArgs []runtime.Value, formatAt int, c runtime.VMCaller) (string, []any) {
	vals := rawArgs[formatAt+1:]
	off := formatAt + 1
	type dir struct {
		start, end int   // the directive's span in spec
		pos        int   // resolved operand index, 0-based
		verb       byte  // final verb letter
		stars      []int // operand positions each * consumed, in order
		starIdx    bool  // mixes * and %[n] — bail out below
	}
	var dirs []dir
	argNum, maxArg := 0, 0
	reordered := false
	for i := 0; i < len(spec); {
		j := strings.IndexByte(spec[i:], '%')
		if j < 0 {
			break
		}
		i += j
		pct := i
		i++ // past '%'
		if i >= len(spec) {
			break
		}
		pos := -1
		var stars []int
		var star, indexed bool
	scan:
		for i < len(spec) {
			ch := spec[i]
			switch {
			case ch == '[':
				k := i + 1
				for k < len(spec) && spec[k] != ']' {
					k++
				}
				if n, err := strconv.Atoi(spec[i+1 : k]); err == nil {
					pos = n - 1
					// after %[n] the next implicit argument is n (Go
					// fmt: "subsequent verbs will use arguments
					// n+1, n+2", 1-based).
					argNum = n
					indexed = true
					reordered = true
				}
				i = k + 1
			case ch == '*':
				if pos < 0 {
					stars = append(stars, argNum)
					argNum++
					if argNum > maxArg {
						maxArg = argNum
					}
				}
				star = true
				i++
			case ch == '#' || ch == '+' || ch == '-' || ch == ' ' || ch == '.' || (ch >= '0' && ch <= '9'):
				i++
			default:
				break scan
			}
		}
		if i >= len(spec) {
			break
		}
		verb := spec[i]
		i++
		if verb == '%' {
			continue
		}
		if pos < 0 {
			pos = argNum
			argNum++
		}
		if pos+1 > maxArg {
			maxArg = pos + 1
		}
		dirs = append(dirs, dir{start: pct, end: i, pos: pos, verb: verb, stars: stars, starIdx: star && indexed})
	}
	// rebuild only when every operand resolves inside the arg list and
	// no directive mixes * with an index — anything else keeps the
	// original spec (and operands) so host fmt reports it as before.
	for _, d := range dirs {
		if d.starIdx || d.pos < 0 || d.pos >= len(vals) || off+d.pos >= len(a) {
			return spec, a[off:]
		}
		for _, sp := range d.stars {
			if sp < 0 || sp >= len(vals) || off+sp >= len(a) {
				return spec, a[off:]
			}
		}
	}
	var sb strings.Builder
	var na []any
	prev := 0
	for _, d := range dirs {
		sb.WriteString(spec[prev:d.start])
		sb.WriteByte('%')
		// flags/width survive; the [n] index does not — newArgs is
		// emitted in directive order, so every operand is implicit.
		tail := spec[d.start+1 : d.end-1]
		for k := 0; k < len(tail); {
			if tail[k] == '[' {
				for k < len(tail) && tail[k] != ']' {
					k++
				}
				k++
				continue
			}
			sb.WriteByte(tail[k])
			k++
		}
		for _, sp := range d.stars {
			na = append(na, a[off+sp])
		}
		switch d.verb {
		case 'T':
			sb.WriteByte('s')
			na = append(na, scriptTypeString(vals[d.pos]))
		case 'p':
			sb.WriteByte('s')
			na = append(na, ptrSpelling(c, vals[d.pos]))
		default:
			sb.WriteByte(d.verb)
			na = append(na, a[off+d.pos])
		}
		prev = d.end
	}
	sb.WriteString(spec[prev:])
	// args past the highest consumed position print as %!(EXTRA ...) —
	// carry them across untouched. Go suppresses the EXTRA report once
	// any %[n] index reordered the arg list.
	if !reordered {
		na = append(na, a[off+maxArg:]...)
	}
	return sb.String(), na
}

// formatOf rebuilds a fmt directive from the verb and the flags/width a
// host call set on the state — for scalar leaf values the host formatter
// does the real work.
func formatOf(f fmt.State, verb rune) string {
	var sb strings.Builder
	sb.WriteByte('%')
	for _, c := range "+-# 0" {
		if f.Flag(int(c)) {
			sb.WriteByte(byte(c))
		}
	}
	if w, ok := f.Width(); ok {
		sb.WriteString(strconv.Itoa(w))
	}
	if p, ok := f.Precision(); ok {
		sb.WriteString("." + strconv.Itoa(p))
	}
	sb.WriteRune(verb)
	return sb.String()
}

func withWidth(f fmt.State, s string) string {
	return fmt.Sprintf(formatOf(f, 's'), s)
}

// callFormat invokes a script fmt.Formatter — Format(fmt.State, rune) —
// handing the live printer through as a host value so the method's
// writes land on the real output (math/big's %d). An absent or
// panicking method reports false, like IfaceCallString.
func callFormat(c runtime.VMCaller, x runtime.Value, f fmt.State, verb rune) bool {
	m, ok := runtime.IfaceMember(c, x, "Format")
	if !ok {
		return false
	}
	_, err := c.Call(m, []runtime.Value{
		&runtime.GoValue{V: f},
		&runtime.UConst{V: constant.MakeInt64(int64(verb)), Rune: true},
	})
	return err == nil
}

// fprintOperands renders the fmt.Print family's operands with gc's
// doPrint spacing: a space between operands only when neither is a
// string — where "string" is the reflect Kind, so a named type over
// string (json.Number) also suppresses it. The host-shaped args are
// *fmtValue wrappers (reflect sees Ptr), so the named-string check
// reads the flat script values.
func fprintOperands(w io.Writer, a []any, flat []runtime.Value) (int, error) {
	var buf bytes.Buffer
	prevStr := false
	for i, arg := range a {
		isStr := arg != nil && reflect.TypeOf(arg).Kind() == reflect.String
		if !isStr && i < len(flat) {
			isStr = isNamedString(flat[i])
		}
		if i > 0 && !isStr && !prevStr {
			buf.WriteByte(' ')
		}
		fmt.Fprint(&buf, arg) // bytes.Buffer writes never fail
		prevStr = isStr
	}
	return w.Write(buf.Bytes())
}

// isNamedString reports whether a script value is a named type whose
// underlying kind is string: Named layers unwrap to the stored scalar.
func isNamedString(x runtime.Value) bool {
	for {
		n, ok := x.(*runtime.Named)
		if !ok {
			_, isStr := x.(string)
			return isStr
		}
		x = n.V
	}
}

// fmtArgs maps script call args to host fmt args: a multi-value call
// result (Tuple) spreads into individual operands — Go treats
// `fmt.Sprint(f())` as `Sprint(v0, v1, ...)` so its space-between-
// operands rule (only when neither is a string) sees the real scalars.
// Returns the host args plus the flattened script args for spec
// rewriting (%T/%p positional lookup).
func fmtArgs(v runtime.VMCaller, args []runtime.Value) ([]any, []runtime.Value) {
	a := make([]any, 0, len(args))
	flat := make([]runtime.Value, 0, len(args))
	for _, x := range args {
		if tup, ok := x.(*runtime.Tuple); ok {
			for _, e := range tup.Elems {
				a = append(a, fmtArg(v, e))
				flat = append(flat, e)
			}
			continue
		}
		a = append(a, fmtArg(v, x))
		flat = append(flat, x)
	}
	return a, flat
}

// fmtArg routes a script value into a host fmt call: scalars unbox to
// Go natives; composites keep their script shape inside a fmtValue.
func fmtArg(v runtime.VMCaller, x runtime.Value) any {
	switch x := x.(type) {
	case *runtime.Named:
		// a fmtValue box inside the payload is already the argument
		// box — re-wrapping hides the inner value from %T rewriting
		// and %v alike (a Sprintf-accumulated named string slot can
		// carry one). Recurse on the boxed value so it renders and
		// types as itself.
		if fv, ok := x.V.(*fmtValue); ok {
			return fmtArg(v, fv.x)
		}
		// the tag only names the declared type — a host-boxed payload
		// still unwraps to fmtRValue, while other payloads keep
		// fmtValue's tag-aware rendering (float32 tags, named scalars).
		if gv, ok := x.V.(*runtime.GoValue); ok {
			return fmtArg(v, gv)
		}
		// a still-constant payload materializes at the declared width
		// first: `fmt.Println(uint64(1<<64 - 1))` is max-uint where the
		// untyped default reading overflows int inside fmtValue.
		if u, ok := x.V.(*runtime.UConst); ok {
			if mc, ok2 := v.(interface {
				MaterializeConstErr(*runtime.UConst, *runtime.TypeDef) (runtime.Value, error)
			}); ok2 {
				if mv, err := mc.MaterializeConstErr(u, x.Typ); err == nil {
					return fmtArg(v, runtime.Tag(x.Typ, mv))
				}
			}
		}
		return &fmtValue{c: v, x: x}
	case int64:
		// script ints store int64 but spell int — bad-verb markers
		// (%!s(int=1)) and %T-adjacent spellings need the real width.
		return int(x)
	case float64, string, bool:
		return x
	case *runtime.UConst:
		nv := mustNativeConst(x)
		if i, ok := nv.(int64); ok {
			return int(i)
		}
		return nv
	case *runtime.GoValue:
		if rv, ok := x.V.(*minireflect.RValue); ok {
			return fmtRValue(v, rv)
		}
		return goNative(x)
	default:
		return &fmtValue{c: v, x: x}
	}
}

// fmtRValue routes a facade reflect.Value to host fmt the way Go routes
// a reflect.Value: the argument unwraps exactly once — an invalid Value
// prints `<invalid reflect.Value>` and a payload that is itself a Value
// stays wrapped so its String still yields Go's nested `<T Value>` form
// — while every other payload formats as the viewed value under any
// verb. %T/%p are rewritten before this runs (rewriteTypeVerbs).
func fmtRValue(c runtime.VMCaller, rv *minireflect.RValue) any {
	if !rv.IsValid() {
		return "<invalid reflect.Value>"
	}
	switch u := rv.Payload().(type) {
	case *minireflect.RValue:
		return u
	case minireflect.RValue:
		return &u
	case *runtime.GoValue:
		if inner, ok := u.V.(*minireflect.RValue); ok {
			return inner
		}
		return goNative(u)
	case runtime.Value:
		return fmtArg(c, u)
	default:
		return u
	}
}

// typedefSpelling renders a typedef for %T/#v output — it delegates to
// the canonical display speller.
func typedefSpelling(td *runtime.TypeDef) string {
	if td == nil {
		return "interface{}"
	}
	return runtime.DisplayName(td)
}

// callerFrames is the script-side *runtime.Frames: it iterates the
// call sites a runtime.Callers snapshot captured.
type callerFrames struct {
	sites []runtime.CallSite
	i     int
}

// Next implements (*runtime.Frames).Next — the bool reports whether a
// further call would yield another frame, so the last real frame
// returns false and `for { f, more := frames.Next(); ...; if !more {
// break } }` does not process an extra empty frame.
func (cf *callerFrames) Next() (callerFrame, bool) {
	if cf.i >= len(cf.sites) {
		return callerFrame{}, false
	}
	s := cf.sites[cf.i]
	cf.i++
	return callerFrame{
		PC:       uintptr(cf.i),
		Func:     &callerFunc{name: s.Name},
		Function: s.Name,
		File:     s.File,
		Line:     s.Line,
	}, cf.i < len(cf.sites)
}

// callerFrame is the script-side runtime.Frame.
type callerFrame struct {
	PC       uintptr
	Func     *callerFunc
	Function string
	File     string
	Line     int
	Entry    uintptr
}

// callerFunc is the script-side *runtime.Func (only Name() is used).
type callerFunc struct{ name string }

// Name implements (*runtime.Func).Name.
func (f *callerFunc) Name() string { return f.name }

// devisit marks a pair of composite values already under comparison.
// A reference cycle (a map or slice containing itself, two structures
// pointing at each other) reaches the same pair again, which Go's
// DeepEqual treats coinductively as equal — recording the pair both
// terminates the walk and answers the recurrence.
type devisit struct{ a, b runtime.Value }

// deepEql implements reflect.DeepEqual over script values: nil-ness and
// type identity are honored, composites compare recursively, and
// leaf/host values compare as marshaled natives. Named tags and
// pointer/ref layers peel on both sides in lockstep — a depth or named
// type mismatch is a type mismatch, so *T never equals T and a named
// type never equals a different name for the same underlying type.
func deepEql(a, b runtime.Value) bool {
	return deepEqlSeen(a, b, map[devisit]bool{})
}

func deepEqlSeen(a, b runtime.Value, seen map[devisit]bool) bool {
	for {
		an, aNamed := a.(*runtime.Named)
		bn, bNamed := b.(*runtime.Named)
		if aNamed != bNamed {
			return false
		}
		if aNamed {
			if !runtime.TypIdenticalStrict(an.Typ, bn.Typ) {
				return false
			}
			a, b = an.V, bn.V
			continue
		}
		ad, aRef := runtime.Deref(a)
		bd, bRef := runtime.Deref(b)
		if aRef != bRef {
			return false
		}
		if aRef {
			a, b = ad, bd
			continue
		}
		break
	}
	if an, bn := deepNilish(a), deepNilish(b); an || bn {
		if !(an && bn) {
			return false
		}
		// an untyped nil and an empty-interface nil are just nil —
		// equal to each other; a typed nil carries a dynamic type so
		// only another typed nil of the same typedef compares equal
		// (kind + spelling — anonymous typedefs all spell "" by name).
		at, aTyped := a.(*runtime.TypedNil)
		bt, bTyped := b.(*runtime.TypedNil)
		if aTyped != bTyped {
			return false
		}
		if aTyped {
			return runtime.TypIdenticalStrict(at.Typ, bt.Typ)
		}
		return true
	}
	switch av := a.(type) {
	case *runtime.Slice:
		bs, ok := b.(*runtime.Slice)
		// both arrays and slices are runtime.Slice — the typedef's
		// spelling carries the kind and element type ([]T != [N]T).
		if !ok || len(av.Elems) != len(bs.Elems) || !runtime.TypIdenticalStrict(av.Typ, bs.Typ) {
			return false
		}
		if av == bs {
			return true
		}
		v := devisit{av, bs}
		if seen[v] {
			return true
		}
		seen[v] = true
		for i := range av.Elems {
			if !deepEqlSeen(av.Elems[i], bs.Elems[i], seen) {
				return false
			}
		}
		return true
	case *runtime.Struct:
		bs, ok := b.(*runtime.Struct)
		// struct defs need full type identity — field names alone let
		// struct{ X int } equal struct{ X any }. Named types match
		// only the same declaration; anonymous ones compare spelling
		// (field types, tags, order).
		if !ok || !runtime.TypIdenticalStrict(av.Def, bs.Def) || len(av.Fields) != len(bs.Fields) {
			return false
		}
		if av == bs {
			return true
		}
		v := devisit{av, bs}
		if seen[v] {
			return true
		}
		seen[v] = true
		for i := range av.Fields {
			if !deepEqlSeen(av.Fields[i], bs.Fields[i], seen) {
				return false
			}
		}
		return true
	case *runtime.Map:
		bm, ok := b.(*runtime.Map)
		if !ok || len(av.Pairs) != len(bm.Pairs) || !runtime.TypIdenticalStrict(av.Typ, bm.Typ) {
			return false
		}
		if av == bm {
			return true
		}
		v := devisit{av, bm}
		if seen[v] {
			return true
		}
		seen[v] = true
		// keys match by the map's own equality — the canonical key in
		// Pairs — not by deep equality: two distinct pointer keys with
		// equal pointees are different keys in Go. Only the values
		// compare recursively.
		for ak, aval := range av.Pairs {
			bval, found := bm.LookupCanonical(ak)
			if !found {
				return false
			}
			if !deepEqlSeen(aval, bval, seen) {
				return false
			}
		}
		return true
	}
	return reflect.DeepEqual(goNative(a), goNative(b))
}

// deepNilish reports whether v is any nil flavor (typed nil, iface nil,
// or the untyped nil).
func deepNilish(v runtime.Value) bool {
	switch v.(type) {
	case *runtime.TypedNil, *runtime.IfaceNil, runtime.Nil:
		return true
	}
	return v == nil || v == runtime.NIL
}

// godebugSetting stands in for internal/godebug.Setting (which cannot be
// imported outside GOROOT): it answers the methods stdlib sources call
// on their godebug knobs. The real implementation syncs a cache through
// a runtime hook on env changes; reading $GODEBUG on each Value() call
// reports the same current value — "" when the knob is unset, which is
// the enabled-by-default answer every consumer relies on.
type godebugSetting struct{ name string }

func (s *godebugSetting) Value() string {
	// a name may repeat in GODEBUG — gc's runtime parse keeps the LAST
	// entry, which is also what makes testenv.SetGODEBUG's appended
	// override win over an earlier setting.
	var val string
	for _, kv := range strings.Split(os.Getenv("GODEBUG"), ",") {
		if n, v, ok := strings.Cut(kv, "="); ok && n == s.Name() {
			val = v
		}
	}
	return val
}
func (s *godebugSetting) Name() string {
	if s.name != "" && s.name[0] == '#' {
		return s.name[1:]
	}
	return s.name
}
func (s *godebugSetting) Undocumented() bool { return s.name != "" && s.name[0] == '#' }
func (s *godebugSetting) String() string     { return s.Name() + "=" + s.Value() }
func (s *godebugSetting) IncNonDefault()     {}
