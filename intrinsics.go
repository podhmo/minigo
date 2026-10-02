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
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"html"
	"io"
	"io/fs"
	"maps"
	"math"
	"net/url"
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
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/podhmo/minigo/runtime"
)

// installStdlib binds the intrinsic packages onto the engine's import-path
// table; Bound packages win over source resolution (loadPath checks pkgs).
func (e *Engine) installStdlib() {
	h := &hostHelpers{e: e}
	e.Bind("fmt", map[string]runtime.Value{
		"Print":   h.ffn("fmt.Print", -1, 0, func(a []any) (any, error) { return retErr(fmt.Fprint(h.out(), a...)) }),
		"Println": h.ffn("fmt.Println", -1, 0, func(a []any) (any, error) { return retErr(fmt.Fprintln(h.out(), a...)) }),
		"Printf": h.ffn("fmt.Printf", 0, 1, func(a []any) (any, error) {
			return retErr(fmt.Fprintf(h.out(), str(a[0]), a[1:]...))
		}),
		// Fprint* take an explicit writer — os.Stdout/os.Stderr arrive as
		// GoValue (unwrapped by fmtArg to the native *os.File).
		"Fprint": h.ffn("fmt.Fprint", -1, 1, func(a []any) (any, error) {
			w, err := asWriter(a[0])
			if err != nil {
				return nil, err
			}
			return retErr(fmt.Fprint(w, a[1:]...))
		}),
		"Fprintf": h.ffn("fmt.Fprintf", 1, 2, func(a []any) (any, error) {
			w, err := asWriter(a[0])
			if err != nil {
				return nil, err
			}
			return retErr(fmt.Fprintf(w, str(a[1]), a[2:]...))
		}),
		"Fprintln": h.ffn("fmt.Fprintln", -1, 1, func(a []any) (any, error) {
			w, err := asWriter(a[0])
			if err != nil {
				return nil, err
			}
			return retErr(fmt.Fprintln(w, a[1:]...))
		}),
		"Sprint":   h.ffn("fmt.Sprint", -1, 0, func(a []any) (any, error) { return fmt.Sprint(a...), nil }, fmt.Sprint),
		"Sprintln": h.ffn("fmt.Sprintln", -1, 0, func(a []any) (any, error) { return fmt.Sprintln(a...), nil }, fmt.Sprintln),
		"Sprintf": h.ffn("fmt.Sprintf", 0, 1, func(a []any) (any, error) {
			return fmt.Sprintf(str(a[0]), a[1:]...), nil
		}),
		// Errorf is hand-bound: %w verbs wrap the cause like Go's
		// fmt.wrapError so errors.Unwrap/Is/As see the chain.
		"Errorf": &runtime.BuiltinFunc{Name: "fmt.Errorf", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) < 1 {
				return nil, fmt.Errorf("fmt.Errorf needs 1+ args")
			}
			a := make([]any, len(args))
			for i, x := range args {
				a[i] = fmtArg(v, x)
			}
			spec := str(a[0])
			spec = rewriteTypeVerbs(spec, a, args, 0)
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
			if s0, ok := e0.(*scriptError); ok {
				// Go's `err == target` fast path: two scriptError boxes
				// around the same script value are one error.
				if s1, ok := e1.(*scriptError); ok && s0.v == s1.v {
					return true, nil
				}
			}
			return errors.Is(e0, e1), nil
		}},
		"Unwrap": &runtime.BuiltinFunc{Name: "errors.Unwrap", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("errors.Unwrap needs 1 arg")
			}
			return errVal(errors.Unwrap(hostErrOf(v, args[0]))), nil
		}},
		// As walks the Unwrap chain and assigns the first cause whose type
		// name matches the target cell's declared type.
		"As": &runtime.BuiltinFunc{Name: "errors.As", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("errors.As needs 2 args")
			}
			want := cellElemTyp(args[1])
			for err := hostErrOf(v, args[0]); err != nil; err = errors.Unwrap(err) {
				sv := scriptErrUnbox(err)
				st := v.TypeOf(sv)
				// spellings disagree on pkg/`*` prefixes between the two
				// paths — compare the final identifier; an interface
				// target (`var e error; &e`) accepts any error value.
				if want == nil || want.Kind == runtime.KindInterface || shortTypName(st) == shortTypName(want) {
					if runtime.SetRef(args[1], sv) {
						return true, nil
					}
				}
			}
			return false, nil
		}},
	})
	e.Bind("strings", map[string]runtime.Value{
		"Contains":    h.fn2("strings.Contains", func(a []any) (any, error) { return strings.Contains(str(a[0]), str(a[1])), nil }, strings.Contains),
		"ContainsAny": h.fn2("strings.ContainsAny", func(a []any) (any, error) { return strings.ContainsAny(str(a[0]), str(a[1])), nil }, strings.ContainsAny),
		"Compare":     h.fn2("strings.Compare", func(a []any) (any, error) { return int64(strings.Compare(str(a[0]), str(a[1]))), nil }),
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
		"Index":      h.fn2("strings.Index", func(a []any) (any, error) { return int64(strings.Index(str(a[0]), str(a[1]))), nil }),
		"Join":       h.fn2("strings.Join", func(a []any) (any, error) { return strings.Join(strSlice(a[0]), str(a[1])), nil }, strings.Join),
		"Split":      h.fn2("strings.Split", func(a []any) (any, error) { return strsSlice(strings.Split(str(a[0]), str(a[1]))), nil }),
		"ToUpper":    h.fn("strings.ToUpper", func(a []any) (any, error) { return strings.ToUpper(str(a[0])), nil }, strings.ToUpper),
		"ToLower":    h.fn("strings.ToLower", func(a []any) (any, error) { return strings.ToLower(str(a[0])), nil }, strings.ToLower),
		"TrimSpace":  h.fn("strings.TrimSpace", func(a []any) (any, error) { return strings.TrimSpace(str(a[0])), nil }, strings.TrimSpace),
		"ReplaceAll": h.fn3("strings.ReplaceAll", func(a []any) (any, error) { return strings.ReplaceAll(str(a[0]), str(a[1]), str(a[2])), nil }, strings.ReplaceAll),
		"Repeat":     h.fn2("strings.Repeat", func(a []any) (any, error) { return strings.Repeat(str(a[0]), intOf(a[1])), nil }, strings.Repeat),
		// a Builder's zero is the host *strings.Builder so Write*/String
		// methods dispatch through reflection like sync.Mutex's.
		"Builder":     hostType("strings.Builder", func() any { return &strings.Builder{} }),
		"NewReplacer": h.fn("strings.NewReplacer", func(a []any) (any, error) { return strings.NewReplacer(strArgs(a)...), nil }, strings.NewReplacer),
		"Fields":      h.fn("strings.Fields", func(a []any) (any, error) { return strsSlice(strings.Fields(str(a[0]))), nil }),
		"EqualFold":   h.fn2("strings.EqualFold", func(a []any) (any, error) { return strings.EqualFold(str(a[0]), str(a[1])), nil }, strings.EqualFold),
		"Count":       h.fn2("strings.Count", func(a []any) (any, error) { return int64(strings.Count(str(a[0]), str(a[1]))), nil }),
		"SplitN":      h.fn3("strings.SplitN", func(a []any) (any, error) { return strsSlice(strings.SplitN(str(a[0]), str(a[1]), intOf(a[2]))), nil }),
		"SplitAfter":  h.fn2("strings.SplitAfter", func(a []any) (any, error) { return strsSlice(strings.SplitAfter(str(a[0]), str(a[1]))), nil }),
		"SplitAfterN": h.fn3("strings.SplitAfterN", func(a []any) (any, error) {
			return strsSlice(strings.SplitAfterN(str(a[0]), str(a[1]), intOf(a[2]))), nil
		}),
		"Trim":         h.fn2("strings.Trim", func(a []any) (any, error) { return strings.Trim(str(a[0]), str(a[1])), nil }, strings.Trim),
		"TrimPrefix":   h.fn2("strings.TrimPrefix", func(a []any) (any, error) { return strings.TrimPrefix(str(a[0]), str(a[1])), nil }, strings.TrimPrefix),
		"TrimSuffix":   h.fn2("strings.TrimSuffix", func(a []any) (any, error) { return strings.TrimSuffix(str(a[0]), str(a[1])), nil }, strings.TrimSuffix),
		"TrimLeft":     h.fn2("strings.TrimLeft", func(a []any) (any, error) { return strings.TrimLeft(str(a[0]), str(a[1])), nil }, strings.TrimLeft),
		"TrimRight":    h.fn2("strings.TrimRight", func(a []any) (any, error) { return strings.TrimRight(str(a[0]), str(a[1])), nil }, strings.TrimRight),
		"LastIndex":    h.fn2("strings.LastIndex", func(a []any) (any, error) { return int64(strings.LastIndex(str(a[0]), str(a[1]))), nil }),
		"LastIndexAny": h.fn2("strings.LastIndexAny", func(a []any) (any, error) { return int64(strings.LastIndexAny(str(a[0]), str(a[1]))), nil }),
		"IndexAny":     h.fn2("strings.IndexAny", func(a []any) (any, error) { return int64(strings.IndexAny(str(a[0]), str(a[1]))), nil }),
		"IndexRune":    h.fn2("strings.IndexRune", func(a []any) (any, error) { return int64(strings.IndexRune(str(a[0]), runeOf(a[1]))), nil }),
		"ContainsRune": h.fn2("strings.ContainsRune", func(a []any) (any, error) { return strings.ContainsRune(str(a[0]), runeOf(a[1])), nil }, strings.ContainsRune),
		"ToTitle":      h.fn("strings.ToTitle", func(a []any) (any, error) { return strings.ToTitle(str(a[0])), nil }, strings.ToTitle),
		//lint:ignore SA1019 mirrors the deprecated stdlib symbol for script parity
		"Title":     h.fn("strings.Title", func(a []any) (any, error) { return strings.Title(str(a[0])), nil }, strings.Title),
		"NewReader": h.fn("strings.NewReader", func(a []any) (any, error) { return strings.NewReader(str(a[0])), nil }, strings.NewReader),
		// func-taking variants call the script callback back through the VM.
		"TrimFunc": &runtime.BuiltinFunc{Name: "strings.TrimFunc", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("strings.TrimFunc needs 2 args")
			}
			pred, cerr := runePred(v, args[1])
			out := strings.TrimFunc(str(args[0]), pred)
			if *cerr != nil {
				return nil, *cerr
			}
			return out, nil
		}},
		"TrimLeftFunc": &runtime.BuiltinFunc{Name: "strings.TrimLeftFunc", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("strings.TrimLeftFunc needs 2 args")
			}
			pred, cerr := runePred(v, args[1])
			out := strings.TrimLeftFunc(str(args[0]), pred)
			if *cerr != nil {
				return nil, *cerr
			}
			return out, nil
		}},
		"TrimRightFunc": &runtime.BuiltinFunc{Name: "strings.TrimRightFunc", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("strings.TrimRightFunc needs 2 args")
			}
			pred, cerr := runePred(v, args[1])
			out := strings.TrimRightFunc(str(args[0]), pred)
			if *cerr != nil {
				return nil, *cerr
			}
			return out, nil
		}},
		"IndexFunc": &runtime.BuiltinFunc{Name: "strings.IndexFunc", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("strings.IndexFunc needs 2 args")
			}
			pred, cerr := runePred(v, args[1])
			out := strings.IndexFunc(str(args[0]), pred)
			if *cerr != nil {
				return nil, *cerr
			}
			return int64(out), nil
		}},
		"LastIndexFunc": &runtime.BuiltinFunc{Name: "strings.LastIndexFunc", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("strings.LastIndexFunc needs 2 args")
			}
			pred, cerr := runePred(v, args[1])
			out := strings.LastIndexFunc(str(args[0]), pred)
			if *cerr != nil {
				return nil, *cerr
			}
			return int64(out), nil
		}},
		"Map": &runtime.BuiltinFunc{Name: "strings.Map", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("strings.Map needs 2 args")
			}
			var cerr error
			mapping := func(r rune) rune {
				if cerr != nil {
					return r
				}
				res, err := v.Call(args[0], []runtime.Value{int64(r)})
				if err != nil {
					cerr = err
					return r
				}
				// a negative result drops the rune in Go
				return rune(int64Of(goNative(res)))
			}
			out := strings.Map(mapping, str(args[1]))
			if cerr != nil {
				return nil, cerr
			}
			return out, nil
		}},
	})
	e.Bind("strconv", map[string]runtime.Value{
		"Atoi":    h.fn("strconv.Atoi", func(a []any) (any, error) { return retErr2(strconv.Atoi(str(a[0]))) }),
		"Itoa":    h.fn("strconv.Itoa", func(a []any) (any, error) { return strconv.Itoa(intOf(a[0])), nil }, strconv.Itoa),
		"Quote":   h.fn("strconv.Quote", func(a []any) (any, error) { return strconv.Quote(str(a[0])), nil }, strconv.Quote),
		"Unquote": h.fn("strconv.Unquote", func(a []any) (any, error) { return retErr2(strconv.Unquote(str(a[0]))) }),
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
	})
	e.Bind("bytes", map[string]runtime.Value{
		"NewBuffer":       h.fn("bytes.NewBuffer", func(a []any) (any, error) { return bytes.NewBuffer(byteSlice(a[0])), nil }, bytes.NewBuffer),
		"NewBufferString": h.fn("bytes.NewBufferString", func(a []any) (any, error) { return bytes.NewBufferString(str(a[0])), nil }, bytes.NewBufferString),
		"Contains":        h.fn2("bytes.Contains", func(a []any) (any, error) { return bytes.Contains(byteSlice(a[0]), byteSlice(a[1])), nil }, bytes.Contains),
		"Index":           h.fn2("bytes.Index", func(a []any) (any, error) { return int64(bytes.Index(byteSlice(a[0]), byteSlice(a[1]))), nil }),
		"LastIndex":       h.fn2("bytes.LastIndex", func(a []any) (any, error) { return int64(bytes.LastIndex(byteSlice(a[0]), byteSlice(a[1]))), nil }),
		"Count":           h.fn2("bytes.Count", func(a []any) (any, error) { return int64(bytes.Count(byteSlice(a[0]), byteSlice(a[1]))), nil }),
		"Equal":           h.fn2("bytes.Equal", func(a []any) (any, error) { return bytes.Equal(byteSlice(a[0]), byteSlice(a[1])), nil }, bytes.Equal),
		"Compare":         h.fn2("bytes.Compare", func(a []any) (any, error) { return int64(bytes.Compare(byteSlice(a[0]), byteSlice(a[1]))), nil }),
		"HasPrefix":       h.fn2("bytes.HasPrefix", func(a []any) (any, error) { return bytes.HasPrefix(byteSlice(a[0]), byteSlice(a[1])), nil }, bytes.HasPrefix),
		"HasSuffix":       h.fn2("bytes.HasSuffix", func(a []any) (any, error) { return bytes.HasSuffix(byteSlice(a[0]), byteSlice(a[1])), nil }, bytes.HasSuffix),
		"Fields":          h.fn("bytes.Fields", func(a []any) (any, error) { return bytesSliceOf(bytes.Fields(byteSlice(a[0]))), nil }),
		"Join":            h.fn2("bytes.Join", func(a []any) (any, error) { return bytes.Join(bytesSlices(a[0]), byteSlice(a[1])), nil }, bytes.Join),
		"Split":           h.fn2("bytes.Split", func(a []any) (any, error) { return bytesSliceOf(bytes.Split(byteSlice(a[0]), byteSlice(a[1]))), nil }),
		"SplitN": h.fn3("bytes.SplitN", func(a []any) (any, error) {
			return bytesSliceOf(bytes.SplitN(byteSlice(a[0]), byteSlice(a[1]), intOf(a[2]))), nil
		}),
		"Repeat":    h.fn2("bytes.Repeat", func(a []any) (any, error) { return bytes.Repeat(byteSlice(a[0]), intOf(a[1])), nil }, bytes.Repeat),
		"Trim":      h.fn2("bytes.Trim", func(a []any) (any, error) { return bytes.Trim(byteSlice(a[0]), str(a[1])), nil }, bytes.Trim),
		"TrimSpace": h.fn("bytes.TrimSpace", func(a []any) (any, error) { return bytes.TrimSpace(byteSlice(a[0])), nil }, bytes.TrimSpace),
		"ToUpper":   h.fn("bytes.ToUpper", func(a []any) (any, error) { return bytes.ToUpper(byteSlice(a[0])), nil }, bytes.ToUpper),
		"ToLower":   h.fn("bytes.ToLower", func(a []any) (any, error) { return bytes.ToLower(byteSlice(a[0])), nil }, bytes.ToLower),
		"ToTitle":   h.fn("bytes.ToTitle", func(a []any) (any, error) { return bytes.ToTitle(byteSlice(a[0])), nil }, bytes.ToTitle),
		"Runes":     h.fn("bytes.Runes", func(a []any) (any, error) { return runeSlice(bytes.Runes(byteSlice(a[0]))), nil }),
		"IndexByte": h.fn2("bytes.IndexByte", func(a []any) (any, error) { return int64(bytes.IndexByte(byteSlice(a[0]), byte(intOf(a[1])))), nil }),
		"IndexRune": h.fn2("bytes.IndexRune", func(a []any) (any, error) { return int64(bytes.IndexRune(byteSlice(a[0]), runeOf(a[1]))), nil }),
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
		"ToLower":   h.fn("unicode.ToLower", func(a []any) (any, error) { return int64(unicode.ToLower(runeOf(a[0]))), nil }),
		"ToUpper":   h.fn("unicode.ToUpper", func(a []any) (any, error) { return int64(unicode.ToUpper(runeOf(a[0]))), nil }),
		"ToTitle":   h.fn("unicode.ToTitle", func(a []any) (any, error) { return int64(unicode.ToTitle(runeOf(a[0]))), nil }),
		"To":        h.fn2("unicode.To", func(a []any) (any, error) { return int64(unicode.To(intOf(a[0]), runeOf(a[1]))), nil }),
		"UpperCase": int64(unicode.UpperCase), "LowerCase": int64(unicode.LowerCase), "TitleCase": int64(unicode.TitleCase),
		"MaxRune": int64(unicode.MaxRune), "MaxASCII": int64(unicode.MaxASCII), "ReplacementChar": int64(unicode.ReplacementChar),
	})
	e.Bind("unicode/utf8", map[string]runtime.Value{
		"RuneCountInString": h.fn("utf8.RuneCountInString", func(a []any) (any, error) { return int64(utf8.RuneCountInString(str(a[0]))), nil }),
		"RuneCount":         h.fn("utf8.RuneCount", func(a []any) (any, error) { return int64(utf8.RuneCount(byteSlice(a[0]))), nil }),
		"RuneLen":           h.fn("utf8.RuneLen", func(a []any) (any, error) { return int64(utf8.RuneLen(runeOf(a[0]))), nil }),
		"RuneStart":         h.fn("utf8.RuneStart", func(a []any) (any, error) { return utf8.RuneStart(byte(intOf(a[0]))), nil }, utf8.RuneStart),
		"Valid":             h.fn("utf8.Valid", func(a []any) (any, error) { return utf8.Valid(byteSlice(a[0])), nil }, utf8.Valid),
		"ValidString":       h.fn("utf8.ValidString", func(a []any) (any, error) { return utf8.ValidString(str(a[0])), nil }, utf8.ValidString),
		"ValidRune":         h.fn("utf8.ValidRune", func(a []any) (any, error) { return utf8.ValidRune(runeOf(a[0])), nil }, utf8.ValidRune),
		"FullRune":          h.fn("utf8.FullRune", func(a []any) (any, error) { return utf8.FullRune(byteSlice(a[0])), nil }, utf8.FullRune),
		"FullRuneInString":  h.fn("utf8.FullRuneInString", func(a []any) (any, error) { return utf8.FullRuneInString(str(a[0])), nil }, utf8.FullRuneInString),
		"DecodeRuneInString": h.fn("utf8.DecodeRuneInString", func(a []any) (any, error) {
			r, n := utf8.DecodeRuneInString(str(a[0]))
			return &runtime.Tuple{Elems: []runtime.Value{int64(r), int64(n)}}, nil
		}),
		"DecodeRune": h.fn("utf8.DecodeRune", func(a []any) (any, error) {
			r, n := utf8.DecodeRune(byteSlice(a[0]))
			return &runtime.Tuple{Elems: []runtime.Value{int64(r), int64(n)}}, nil
		}),
		"DecodeLastRuneInString": h.fn("utf8.DecodeLastRuneInString", func(a []any) (any, error) {
			r, n := utf8.DecodeLastRuneInString(str(a[0]))
			return &runtime.Tuple{Elems: []runtime.Value{int64(r), int64(n)}}, nil
		}),
		// Script-shaped: stdlib EncodeRune writes into a caller []byte;
		// here it returns the encoded rune as a string.
		"EncodeRune": h.fn("utf8.EncodeRune", func(a []any) (any, error) {
			var buf [utf8.UTFMax]byte
			n := utf8.EncodeRune(buf[:], runeOf(a[0]))
			return string(buf[:n]), nil
		}),
		"UTFMax":    int64(utf8.UTFMax),
		"RuneError": int64(utf8.RuneError),
		"RuneSelf":  int64(utf8.RuneSelf),
	})
	e.Bind("math", map[string]runtime.Value{
		"Pi": math.Pi, "E": math.E, "Phi": math.Phi,
		"Sqrt2": math.Sqrt2, "SqrtE": math.SqrtE, "SqrtPi": math.SqrtPi, "SqrtPhi": math.SqrtPhi,
		"Ln2": math.Ln2, "Log2E": math.Log2E, "Ln10": math.Ln10, "Log10E": math.Log10E,
		"MaxInt": int64(math.MaxInt), "MinInt": int64(math.MinInt),
		"MaxFloat32": float64(math.MaxFloat32), "MaxFloat64": math.MaxFloat64,
		"SmallestNonzeroFloat32": float64(math.SmallestNonzeroFloat32),
		"SmallestNonzeroFloat64": math.SmallestNonzeroFloat64,
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
		"Log2":                   h.fn("math.Log2", func(a []any) (any, error) { return math.Log2(floatOf(a[0])), nil }, math.Log2),
		"Log10":                  h.fn("math.Log10", func(a []any) (any, error) { return math.Log10(floatOf(a[0])), nil }, math.Log10),
		"Log1p":                  h.fn("math.Log1p", func(a []any) (any, error) { return math.Log1p(floatOf(a[0])), nil }, math.Log1p),
		"Mod":                    h.fn2("math.Mod", func(a []any) (any, error) { return math.Mod(floatOf(a[0]), floatOf(a[1])), nil }, math.Mod),
		"Remainder":              h.fn2("math.Remainder", func(a []any) (any, error) { return math.Remainder(floatOf(a[0]), floatOf(a[1])), nil }, math.Remainder),
		"Max":                    h.fn2("math.Max", func(a []any) (any, error) { return math.Max(floatOf(a[0]), floatOf(a[1])), nil }, math.Max),
		"Min":                    h.fn2("math.Min", func(a []any) (any, error) { return math.Min(floatOf(a[0]), floatOf(a[1])), nil }, math.Min),
		"Dim":                    h.fn2("math.Dim", func(a []any) (any, error) { return math.Dim(floatOf(a[0]), floatOf(a[1])), nil }, math.Dim),
		"Sin":                    h.fn("math.Sin", func(a []any) (any, error) { return math.Sin(floatOf(a[0])), nil }, math.Sin),
		"Cos":                    h.fn("math.Cos", func(a []any) (any, error) { return math.Cos(floatOf(a[0])), nil }, math.Cos),
		"Tan":                    h.fn("math.Tan", func(a []any) (any, error) { return math.Tan(floatOf(a[0])), nil }, math.Tan),
		"Asin":                   h.fn("math.Asin", func(a []any) (any, error) { return math.Asin(floatOf(a[0])), nil }, math.Asin),
		"Acos":                   h.fn("math.Acos", func(a []any) (any, error) { return math.Acos(floatOf(a[0])), nil }, math.Acos),
		"Atan":                   h.fn("math.Atan", func(a []any) (any, error) { return math.Atan(floatOf(a[0])), nil }, math.Atan),
		"Atan2":                  h.fn2("math.Atan2", func(a []any) (any, error) { return math.Atan2(floatOf(a[0]), floatOf(a[1])), nil }, math.Atan2),
		"Sinh":                   h.fn("math.Sinh", func(a []any) (any, error) { return math.Sinh(floatOf(a[0])), nil }, math.Sinh),
		"Cosh":                   h.fn("math.Cosh", func(a []any) (any, error) { return math.Cosh(floatOf(a[0])), nil }, math.Cosh),
		"Tanh":                   h.fn("math.Tanh", func(a []any) (any, error) { return math.Tanh(floatOf(a[0])), nil }, math.Tanh),
		"Erf":                    h.fn("math.Erf", func(a []any) (any, error) { return math.Erf(floatOf(a[0])), nil }, math.Erf),
		"Erfc":                   h.fn("math.Erfc", func(a []any) (any, error) { return math.Erfc(floatOf(a[0])), nil }, math.Erfc),
		"Gamma":                  h.fn("math.Gamma", func(a []any) (any, error) { return math.Gamma(floatOf(a[0])), nil }, math.Gamma),
		"Ldexp":                  h.fn2("math.Ldexp", func(a []any) (any, error) { return math.Ldexp(floatOf(a[0]), intOf(a[1])), nil }, math.Ldexp),
		"Nextafter":              h.fn2("math.Nextafter", func(a []any) (any, error) { return math.Nextafter(floatOf(a[0]), floatOf(a[1])), nil }, math.Nextafter),
		"Copysign":               h.fn2("math.Copysign", func(a []any) (any, error) { return math.Copysign(floatOf(a[0]), floatOf(a[1])), nil }, math.Copysign),
		"Signbit":                h.fn("math.Signbit", func(a []any) (any, error) { return math.Signbit(floatOf(a[0])), nil }, math.Signbit),
		"IsNaN":                  h.fn("math.IsNaN", func(a []any) (any, error) { return math.IsNaN(floatOf(a[0])), nil }, math.IsNaN),
		"IsInf":                  h.fn2("math.IsInf", func(a []any) (any, error) { return math.IsInf(floatOf(a[0]), intOf(a[1])), nil }, math.IsInf),
		"NaN":                    h.fn("math.NaN", func(a []any) (any, error) { return math.NaN(), nil }, math.NaN),
		"Inf":                    h.fn("math.Inf", func(a []any) (any, error) { return math.Inf(intOf(a[0])), nil }, math.Inf),
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
		"EncodedLen":     h.fn("hex.EncodedLen", func(a []any) (any, error) { return int64(hex.EncodedLen(intOf(a[0]))), nil }),
		"DecodedLen":     h.fn("hex.DecodedLen", func(a []any) (any, error) { return int64(hex.DecodedLen(intOf(a[0]))), nil }),
	})
	e.Bind("encoding/json", map[string]runtime.Value{
		// Marshal/MarshalIndent take raw runtime args — h.fn's goNative
		// would stringify *runtime.Struct before goJSON can field-map it.
		"Marshal": &runtime.BuiltinFunc{Name: "json.Marshal", Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("json.Marshal needs 1 arg, got %d", len(args))
			}
			b, err := json.Marshal(goJSON(args[0]))
			return &runtime.Tuple{Elems: []runtime.Value{scriptVal(b), errVal(err)}}, nil
		}},
		"MarshalIndent": &runtime.BuiltinFunc{Name: "json.MarshalIndent", Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 3 {
				return nil, fmt.Errorf("json.MarshalIndent needs 3 args, got %d", len(args))
			}
			b, err := json.MarshalIndent(goJSON(args[0]), str(goNative(args[1])), str(goNative(args[2])))
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
			var dec any
			if err := json.Unmarshal(byteSlice(goNative(args[0])), &dec); err != nil {
				return errVal(err), nil
			}
			sv := jsonShape(v, dec, derefTyp(v.TypeOf(args[1])))
			if !runtime.SetRef(args[1], sv) {
				return nil, fmt.Errorf("json.Unmarshal: cannot assign to %T", args[1])
			}
			return errVal(nil), nil
		}},
		"Valid": h.fn("json.Valid", func(a []any) (any, error) { return json.Valid(byteSlice(a[0])), nil }, json.Valid),
	})
	e.Bind("net/url", map[string]runtime.Value{
		"QueryEscape":   h.fn("url.QueryEscape", func(a []any) (any, error) { return url.QueryEscape(str(a[0])), nil }, url.QueryEscape),
		"PathEscape":    h.fn("url.PathEscape", func(a []any) (any, error) { return url.PathEscape(str(a[0])), nil }, url.PathEscape),
		"QueryUnescape": h.fn("url.QueryUnescape", func(a []any) (any, error) { return retErr2(url.QueryUnescape(str(a[0]))) }),
		"PathUnescape":  h.fn("url.PathUnescape", func(a []any) (any, error) { return retErr2(url.PathUnescape(str(a[0]))) }),
		"JoinPath": h.fn("url.JoinPath", func(a []any) (any, error) {
			return retErr2(url.JoinPath(str(a[0]), strArgs(a[1:])...))
		}),
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
	e.Bind("sort", map[string]runtime.Value{
		"Ints":     h.sortInPlace("sort.Ints"),
		"Float64s": h.sortInPlace("sort.Float64s"),
		"Strings":  h.sortInPlace("sort.Strings"),
		"Slice":    &runtime.BuiltinFunc{Name: "sort.Slice", Fn: h.sortSlice},
		"SliceIsSorted": &runtime.BuiltinFunc{Name: "sort.SliceIsSorted", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			s, ok := args[0].(*runtime.Slice)
			if !ok {
				return nil, fmt.Errorf("sort.SliceIsSorted: first arg must be a slice")
			}
			less := args[1]
			for i := len(s.Elems) - 1; i > 0; i-- {
				r, err := v.Call(less, []runtime.Value{int64(i), int64(i - 1)})
				if err != nil {
					return nil, err
				}
				if b, _ := r.(bool); b {
					return false, nil
				}
			}
			return true, nil
		}},
		"Search": &runtime.BuiltinFunc{Name: "sort.Search", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			n, _ := args[0].(int64)
			f := args[1]
			i, j := int64(0), n
			for i < j {
				m := int64(uint64(i+j) >> 1)
				r, err := v.Call(f, []runtime.Value{m})
				if err != nil {
					return nil, err
				}
				if b, _ := r.(bool); b {
					j = m
				} else {
					i = m + 1
				}
			}
			return i, nil
		}},
		"SliceStable": &runtime.BuiltinFunc{Name: "sort.SliceStable", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			s, ok := args[0].(*runtime.Slice)
			if !ok {
				return nil, fmt.Errorf("sort.SliceStable: first arg must be a slice")
			}
			less := args[1]
			var cerr error
			sort.SliceStable(s.Elems, func(i, j int) bool {
				if cerr != nil {
					return false
				}
				r, err := v.Call(less, []runtime.Value{int64(i), int64(j)})
				if err != nil {
					cerr = err
					return false
				}
				b, _ := r.(bool)
				return b
			})
			return runtime.NIL, cerr
		}},
	})
	e.Bind("slices", map[string]runtime.Value{
		"Sort": h.sortInPlace("slices.Sort"),
		"Contains": h.fn2("slices.Contains", func(a []any) (any, error) {
			return slices.Contains(anySlice(a[0]), a[1]), nil
		}),
		"Index": h.fn2("slices.Index", func(a []any) (any, error) {
			return int64(slices.Index(anySlice(a[0]), a[1])), nil
		}),
		"Clone": h.fn("slices.Clone", func(a []any) (any, error) {
			return slices.Clone(anySlice(a[0])), nil
		}),
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
		"BinarySearchFunc": &runtime.BuiltinFunc{Name: "slices.BinarySearchFunc", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			s, ok := args[0].(*runtime.Slice)
			if !ok {
				return nil, fmt.Errorf("slices.BinarySearchFunc: first arg must be a slice")
			}
			target := args[1]
			cf := args[2]
			cmpAt := func(i int) (int64, error) {
				r, err := v.Call(cf, []runtime.Value{s.Elems[i], target})
				if err != nil {
					return 0, err
				}
				n, _ := r.(int64)
				return n, nil
			}
			i, j := 0, len(s.Elems)
			for i < j {
				m := int(uint(i+j) >> 1)
				n, err := cmpAt(m)
				if err != nil {
					return nil, err
				}
				if n < 0 {
					i = m + 1
				} else {
					j = m
				}
			}
			found := false
			if i < len(s.Elems) {
				n, err := cmpAt(i)
				if err != nil {
					return nil, err
				}
				found = n == 0
			}
			return &runtime.Tuple{Elems: []runtime.Value{int64(i), found}}, nil
		}},
		"EqualFunc": &runtime.BuiltinFunc{Name: "slices.EqualFunc", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
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
				r, err := v.Call(eq, []runtime.Value{a.Elems[i], b.Elems[i]})
				if err != nil {
					return nil, err
				}
				if ok, _ := r.(bool); !ok {
					return false, nil
				}
			}
			return true, nil
		}},
		"IndexFunc": &runtime.BuiltinFunc{Name: "slices.IndexFunc", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			s, ok := args[0].(*runtime.Slice)
			if !ok {
				return nil, fmt.Errorf("slices.IndexFunc: first arg must be a slice")
			}
			for i, el := range s.Elems {
				r, err := v.Call(args[1], []runtime.Value{el})
				if err != nil {
					return nil, err
				}
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
			i, _ := args[1].(int64)
			el := slices.Insert(s.Elems, int(i), args[2:]...)
			return &runtime.Slice{Elems: el}, nil
		}},
		"Delete": &runtime.BuiltinFunc{Name: "slices.Delete", Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			s, ok := args[0].(*runtime.Slice)
			if !ok || len(args) != 3 {
				return nil, fmt.Errorf("slices.Delete(slice, i, j)")
			}
			i, _ := args[1].(int64)
			j, _ := args[2].(int64)
			el := slices.Delete(s.Elems, int(i), int(j))
			return &runtime.Slice{Elems: el}, nil
		}},
	})
	e.Bind("maps", map[string]runtime.Value{
		"Keys":   h.fn("maps.Keys", func(a []any) (any, error) { return mapKeys(a[0]), nil }),
		"Values": h.fn("maps.Values", func(a []any) (any, error) { return mapValues(a[0]), nil }),
		"Clone": h.fn("maps.Clone", func(a []any) (any, error) {
			if m, ok := a[0].(map[any]any); ok {
				return maps.Clone(m), nil
			}
			return nil, fmt.Errorf("maps.Clone: arg must be a map")
		}),
		"Copy": &runtime.BuiltinFunc{Name: "maps.Copy", Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			dst, _ := args[0].(*runtime.Map)
			src, _ := args[1].(*runtime.Map)
			if dst == nil || src == nil {
				return nil, fmt.Errorf("maps.Copy: args must be maps")
			}
			for _, k := range src.Order {
				v := src.Pairs[runtime.CanonicalKey(k)]
				ck := runtime.CanonicalKey(k)
				if _, ok := dst.Pairs[ck]; !ok {
					dst.Order = append(dst.Order, k)
				}
				dst.Pairs[ck] = v
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
				bv, ok := b.Pairs[k]
				if !ok || !equalScript(av, bv) {
					return false, nil
				}
			}
			return true, nil
		}},
	})
	// os: an interpreted program must never observe or terminate the host
	// process — Exit is always a trap; the environment/argv surface is only
	// bound when the engine is unrestricted (no AllowedRoots). File-system
	// operations are always bound: each path argument resolves through
	// e.fsPath, which anchors relative paths at the engine's virtual cwd
	// and enforces AllowedRoots per call — that is the restricted-mode
	// file policy (host-surface gating stays per-symbol via WithHostPolicy).
	ospkg := map[string]runtime.Value{
		"Exit": h.fn("os.Exit", func(a []any) (any, error) {
			return nil, errors.New("os.Exit is not supported: an interpreted program cannot terminate the host process")
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
		"IsNotExist":   h.fn1("os.IsNotExist", func(a []any) (any, error) { return os.IsNotExist(asErr(a[0])), nil }, os.IsNotExist),
		"IsExist":      h.fn1("os.IsExist", func(a []any) (any, error) { return os.IsExist(asErr(a[0])), nil }, os.IsExist),
		"IsPermission": h.fn1("os.IsPermission", func(a []any) (any, error) { return os.IsPermission(asErr(a[0])), nil }, os.IsPermission),
		"IsTimeout":    h.fn1("os.IsTimeout", func(a []any) (any, error) { return os.IsTimeout(asErr(a[0])), nil }, os.IsTimeout),
		// error sentinels for errors.Is on the script side
		"ErrNotExist":   &runtime.GoValue{V: fs.ErrNotExist},
		"ErrExist":      &runtime.GoValue{V: fs.ErrExist},
		"ErrPermission": &runtime.GoValue{V: fs.ErrPermission},
		"ErrClosed":     &runtime.GoValue{V: fs.ErrClosed},
		"ErrInvalid":    &runtime.GoValue{V: fs.ErrInvalid},
		"ErrNoDeadline": &runtime.GoValue{V: os.ErrNoDeadline},
		// consts
		"PathSeparator":     int64(os.PathSeparator),
		"PathListSeparator": int64(os.PathListSeparator),
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
		"SeekStart":         int64(io.SeekStart),
		"SeekCurrent":       int64(io.SeekCurrent),
		"SeekEnd":           int64(io.SeekEnd),
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
		// os.Args is the host process argv as a VARIABLE, like Go's —
		// flag's package init reads it via len(os.Args).
		ospkg["Args"] = strsSlice(os.Args)
		ospkg["Hostname"] = h.fn("os.Hostname", func(a []any) (any, error) { return retErr2(os.Hostname()) })
		// process stdio, boxed for cmd.Stdout / cmd.Stderr wiring
		ospkg["Stdin"] = &runtime.GoValue{V: os.Stdin}
		ospkg["Stdout"] = &runtime.GoValue{V: os.Stdout}
		ospkg["Stderr"] = &runtime.GoValue{V: os.Stderr}
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
		"Separator":     int64(os.PathSeparator),
		"ListSeparator": int64(os.PathListSeparator),
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
		"WalkDir": &runtime.BuiltinFunc{Name: "filepath.WalkDir", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
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
				r, cerr := v.Call(cb, []runtime.Value{sp, &runtime.GoValue{V: d}, errVal(werr)})
				if cerr != nil {
					return cerr
				}
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
	e.Bind("unsafe", map[string]runtime.Value{
		"Sizeof": &runtime.BuiltinFunc{Name: "unsafe.Sizeof", Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			return unsafeSizeOf(args[0]), nil
		}},
		"Alignof": &runtime.BuiltinFunc{Name: "unsafe.Alignof", Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			return unsafeAlignOf(args[0]), nil
		}},
		"Offsetof": h.fn("unsafe.Offsetof", func(a []any) (any, error) {
			return nil, errors.New("unsafe.Offsetof is not supported: selector results are not values")
		}),
	})
	e.Bind("runtime", map[string]runtime.Value{
		"GOOS":   goruntime.GOOS,
		"GOARCH": goruntime.GOARCH,
		"NumGoroutine": h.fn("runtime.NumGoroutine", func(a []any) (any, error) {
			return int64(goruntime.NumGoroutine()), nil
		}),
		"Gosched": h.fn("runtime.Gosched", func(a []any) (any, error) {
			goruntime.Gosched()
			return nil, nil
		}),
		"NumCPU": h.fn("runtime.NumCPU", func(a []any) (any, error) { return int64(goruntime.NumCPU()), nil }),
		"GOMAXPROCS": &runtime.BuiltinFunc{Name: "runtime.GOMAXPROCS", Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			// read-only on the script side: GOMAXPROCS(0) reports the
			// current setting without mutating the host process's
			// parallelism.
			return int64(goruntime.GOMAXPROCS(0)), nil
		}},
		"Version": h.fn("runtime.Version", func(a []any) (any, error) { return goruntime.Version(), nil }),
		"GC":      h.fn("runtime.GC", func(a []any) (any, error) { return nil, nil }),
	})
	e.Bind("time", map[string]runtime.Value{
		"Sleep": h.fn("time.Sleep", func(a []any) (any, error) { time.Sleep(durOf(a[0])); return nil, nil }),
		"After": h.fn("time.After", func(a []any) (any, error) {
			return &runtime.GoValue{V: time.After(durOf(a[0]))}, nil
		}),
		"NewTimer": h.fn("time.NewTimer", func(a []any) (any, error) {
			return &runtime.GoValue{V: time.NewTimer(durOf(a[0]))}, nil
		}),
		"NewTicker": h.fn("time.NewTicker", func(a []any) (any, error) {
			return &runtime.GoValue{V: time.NewTicker(durOf(a[0]))}, nil
		}),
		"Now": h.fn("time.Now", func(a []any) (any, error) { return time.Now(), nil }, time.Now),
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
		"ParseDuration": h.fn1("time.ParseDuration", func(a []any) (any, error) {
			d, err := time.ParseDuration(str(a[0]))
			return &runtime.Tuple{Elems: []runtime.Value{scriptVal(d), errVal(err)}}, nil
		}),
		"Unix": h.fn2("time.Unix", func(a []any) (any, error) {
			return &runtime.GoValue{V: time.Unix(int64Of(a[0]), int64Of(a[1]))}, nil
		}),
		"Nanosecond":  time.Nanosecond,
		"Microsecond": time.Microsecond,
		"Second":      time.Second,
		"Minute":      time.Minute,
		"Hour":        time.Hour,
		"Millisecond": time.Millisecond,
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
	dv, ok := runtime.Deref(v)
	if !ok {
		dv = v
	}
	lock, lok := vc.Member(dv, "Lock")
	unlock, uok := vc.Member(dv, "Unlock")
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

// ---- value marshalling ----

// hostHelpers builds BuiltinFuncs whose Fn marshals arguments to Go natives
// and results back to runtime values.
type hostHelpers struct {
	e *Engine // for the configured output writer
}

// out returns the engine's output writer (io.Discard when unset).
func (h *hostHelpers) out() io.Writer {
	if h.e == nil || h.e.out == nil {
		return io.Discard
	}
	return h.e.out
}

func (h *hostHelpers) fn(name string, f func([]any) (any, error), target ...any) *runtime.BuiltinFunc {
	bf := &runtime.BuiltinFunc{Name: name, Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
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

// sortInPlace sorts a *runtime.Slice's elements directly — going through
// goNative would sort a fresh copy and leave the script's slice untouched.
func (h *hostHelpers) sortInPlace(name string) *runtime.BuiltinFunc {
	return &runtime.BuiltinFunc{Name: name, Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != 1 {
			return nil, fmt.Errorf("%s needs 1 arg, got %d", name, len(args))
		}
		s, ok := args[0].(*runtime.Slice)
		if !ok {
			return nil, fmt.Errorf("%s: arg must be a slice, got %T", name, args[0])
		}
		sortScript(s.Elems)
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
				return a < b
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
		return an < bn
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
			yv, ok := y.Pairs[k]
			if !ok || !equalScript(xv, yv) {
				return false
			}
		}
		return true
	}
	return a == b
}

func (h *hostHelpers) sortSlice(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
	s, ok := args[0].(*runtime.Slice)
	if !ok {
		return nil, fmt.Errorf("sort.Slice: first arg must be a slice")
	}
	less := args[1]
	sort.Slice(s.Elems, func(i, j int) bool {
		r, err := v.Call(less, []runtime.Value{int64(i), int64(j)})
		if err != nil {
			return false
		}
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
		s, ok := args[0].(*runtime.Slice)
		if !ok {
			return nil, fmt.Errorf("%s: first arg must be a slice", name)
		}
		cmp := args[1]
		var cerr error
		sort.SliceStable(s.Elems, func(i, j int) bool {
			if cerr != nil {
				return false
			}
			r, err := v.Call(cmp, []runtime.Value{s.Elems[i], s.Elems[j]})
			if err != nil {
				cerr = err
				return false
			}
			n, _ := r.(int64)
			return n < 0
		})
		if cerr != nil {
			return nil, cerr
		}
		return runtime.NIL, nil
	}}
}

// unsafeSizeOf approximates unsafe.Sizeof on a 64-bit host: script values
// carry erased types, so the answer reflects the boxed representation.
func unsafeSizeOf(v runtime.Value) int64 {
	switch x := v.(type) {
	case bool:
		return 1
	case int64, float64:
		return 8
	case string:
		return 16
	case *runtime.Slice:
		return 24
	case *runtime.Map, *runtime.Chan, *runtime.Cell:
		return 8
	case *runtime.Struct:
		// field sizes without padding — a documented approximation
		var n int64
		for _, f := range x.Fields {
			n += unsafeSizeOf(f)
		}
		return n
	case runtime.Nil, *runtime.TypedNil, *runtime.IfaceNil:
		return 16 // interface pair
	default:
		return 8 // pointer-sized boxes
	}
}

func unsafeAlignOf(v runtime.Value) int64 {
	if n := unsafeSizeOf(v); n < 8 {
		if n < 1 {
			return 1 // alignment is always at least 1
		}
		return n
	}
	return 8
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

// scriptVal converts a Go-native result back to a runtime value. Concrete
// runtime types pass through; anything else (errors, host structs) is boxed
// as a GoValue — Value is `any`, so it cannot be a type-switch case itself.
func scriptVal(v any) runtime.Value {
	switch x := v.(type) {
	case nil:
		return runtime.NIL
	case bool, string, int64, float64:
		return x
	case int:
		return int64(x)
	case []byte:
		// a []byte result unmarshals to a slice of int64s so `string(b)`
		// and indexing behave like Go source suggests.
		el := make([]runtime.Value, len(x))
		for i, b := range x {
			el[i] = int64(b)
		}
		return &runtime.Slice{Elems: el}
	case []string:
		return strsSlice(x)
	case time.Duration:
		// durations stay raw host values: methods (.Hours(), .String())
		// dispatch through reflection and binaryOp unwraps for arithmetic.
		return x
	case []any:
		el := make([]runtime.Value, len(x))
		for i, e := range x {
			el[i] = scriptVal(e)
		}
		return &runtime.Slice{Elems: el}
	case map[any]any:
		m := &runtime.Map{Pairs: map[runtime.Value]runtime.Value{}}
		for k, vv := range x {
			kk := scriptVal(k)
			ck := runtime.CanonicalKey(kk)
			if _, ok := m.Pairs[ck]; !ok {
				m.Order = append(m.Order, kk)
			}
			m.Pairs[ck] = scriptVal(vv)
		}
		return m
	case runtime.Nil, *runtime.Tuple, *runtime.Cell, *runtime.Slice,
		*runtime.Map, *runtime.Struct, *runtime.Function, *runtime.Closure,
		*runtime.BoundMethod, *runtime.BuiltinFunc, *runtime.GoValue,
		*runtime.Chan, *runtime.TypeDef, *runtime.Iterator, *runtime.Package,
		*runtime.ImportRef, *runtime.Named:
		return x
	default:
		return &runtime.GoValue{V: x}
	}
}

// goNative converts a runtime value to its Go-native counterpart for host
// calls: cells unwrap, slices/maps become []any / map[any]any, structs get a
// Stringer view so fmt prints them sensibly.
func goNative(v runtime.Value) any {
	switch x := v.(type) {
	case runtime.Nil:
		return nil
	case *runtime.Named:
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
		out := make(map[any]any, len(x.Pairs))
		for _, k := range x.Order {
			out[goNative(k)] = goNative(x.Pairs[runtime.CanonicalKey(k)])
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
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func intOf(v any) int {
	switch x := v.(type) {
	case *runtime.Named:
		return intOf(x.V)
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

func strsSlice(ss []string) *runtime.Slice {
	el := make([]runtime.Value, len(ss))
	for i, s := range ss {
		el[i] = s
	}
	return &runtime.Slice{Elems: el}
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
// element into a script []byte slice.
func bytesSliceOf(bb [][]byte) any {
	out := make([]any, len(bb))
	for i, b := range bb {
		out[i] = b
	}
	return out
}

// runeSlice lifts a []rune result into []any of int64s.
func runeSlice(rs []rune) any {
	out := make([]any, len(rs))
	for i, r := range rs {
		out[i] = int64(r)
	}
	return out
}

func floatOf(v any) float64 {
	switch x := v.(type) {
	case *runtime.Named:
		return floatOf(x.V)
	case float64:
		return x
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

// runePred adapts a script `func(rune) bool` to the host signature for
// strings.*Func calls; the returned error slot captures a callback
// failure so the builtin can surface it after the host call returns.
func runePred(v runtime.VMCaller, fn runtime.Value) (func(rune) bool, *error) {
	cerr := new(error)
	return func(r rune) bool {
		if *cerr != nil {
			return false
		}
		res, err := v.Call(fn, []runtime.Value{int64(r)})
		if err != nil {
			*cerr = err
			return false
		}
		b, _ := res.(bool)
		return b
	}, cerr
}

// asWriter pulls an io.Writer out of a bound stdio handle (os.Stdout,
// os.Stderr, an *os.File) for the fmt.Fprint* family.
func asWriter(v any) (io.Writer, error) {
	// args arrive fmtArg'd: a *runtime.Cell (a `&b` address-of) surfaces
	// as *fmtValue — unwrap back through the reference to the box.
	if fv, ok := v.(*fmtValue); ok {
		v = fv.x
	}
	if dv, ok := runtime.Deref(v); ok {
		v = dv
	}
	if g, ok := v.(*runtime.GoValue); ok {
		v = g.V
	}
	if w, ok := v.(io.Writer); ok {
		return w, nil
	}
	return nil, fmt.Errorf("not an io.Writer: %T", v)
}

// goJSON marshals a script value into the shape encoding/json expects:
// structs become field-name maps, runtime maps/slices recurse, GoValue
// unwraps.
func goJSON(v any) any {
	switch x := v.(type) {
	case *runtime.Named:
		return goJSON(x.V)
	case *runtime.Cell:
		return goJSON(x.Elem)
	case *runtime.Struct:
		// structs marshal in DECLARATION order (encoding/json never
		// sorts struct fields) — an ordered map keeps that visible.
		var o orderedObject
		for i, name := range x.Def.Fields {
			if i < len(x.Fields) {
				key, omit, skip := jsonFieldKey(x.Def, name)
				if skip || (omit && jsonIsEmpty(x.Fields[i])) {
					continue
				}
				o.keys = append(o.keys, key)
				o.vals = append(o.vals, goJSON(x.Fields[i]))
			}
		}
		return o
	case *runtime.Slice:
		out := make([]any, len(x.Elems))
		for i, e := range x.Elems {
			out[i] = goJSON(e)
		}
		return out
	case *runtime.Map:
		m := make(map[string]any, len(x.Pairs))
		for _, k := range x.Order {
			m[str(goNative(k))] = goJSON(x.Pairs[runtime.CanonicalKey(k)])
		}
		return m
	case *runtime.GoValue:
		return x.V
	case map[any]any: // goNative's *runtime.Map output — keys stringify
		m := make(map[string]any, len(x))
		for k, e := range x {
			m[str(k)] = goJSON(e)
		}
		return m
	case []any: // goNative's *runtime.Slice output
		for i, e := range x {
			x[i] = goJSON(e)
		}
		return x
	default:
		return v
	}
}

// jsonFieldKey maps a struct field to its JSON object key: the `json`
// tag's name wins, a "-" tag skips the field, `omitempty` drops empty
// values on marshal, and an absent tag falls back to the field name
// (matching encoding/json's defaulting).
func jsonFieldKey(def *runtime.TypeDef, name string) (key string, omitEmpty, skip bool) {
	if def != nil && def.FTags != nil {
		if tag, ok := def.FTags[name]; ok {
			j := reflect.StructTag(tag).Get("json")
			if j == "-" {
				return "", false, true
			}
			omit := false
			if i := strings.IndexByte(j, ','); i >= 0 {
				for _, opt := range strings.Split(j[i+1:], ",") {
					if opt == "omitempty" {
						omit = true
					}
				}
				j = j[:i]
			}
			if j != "" {
				return j, omit, false
			}
		}
	}
	return name, false, false
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

// jsonLookup finds key in a decoded object; encoding/json also accepts a
// case-insensitive match as a fallback.
func jsonLookup(m map[string]any, key string) (any, bool) {
	if v, ok := m[key]; ok {
		return v, true
	}
	for k, v := range m {
		if strings.EqualFold(k, key) {
			return v, true
		}
	}
	return nil, false
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

// jsonShape converts a decoded JSON tree (map[string]any / []any /
// scalars from encoding/json) into the runtime shape a declared typedef
// expects: structs get their declared fields by json tag, numeric fields
// land as int64/float64 per the declared scalar, and slices/maps keep
// their typedef tags. Unresolvable shapes fall back to scriptVal.
func jsonShape(c runtime.VMCaller, dec any, td *runtime.TypeDef) runtime.Value {
	if td == nil {
		return scriptVal(jsonDeep(dec))
	}
	switch td.Kind {
	case runtime.KindStruct:
		m, ok := dec.(map[string]any)
		if !ok {
			if dec == nil {
				return c.Zero(td)
			}
			return scriptVal(jsonDeep(dec))
		}
		z, ok := c.Zero(td).(*runtime.Struct)
		if !ok {
			return scriptVal(jsonDeep(dec))
		}
		for i, name := range z.Def.Fields {
			key, _, skip := jsonFieldKey(z.Def, name)
			if skip {
				continue
			}
			if fv, ok := jsonLookup(m, key); ok {
				z.Fields[i] = jsonShape(c, fv, c.TypeOf(z.Fields[i]))
			}
		}
		return z
	case runtime.KindSlice:
		arr, ok := dec.([]any)
		if !ok {
			if dec == nil {
				return c.Zero(td)
			}
			return scriptVal(jsonDeep(dec))
		}
		et := c.TypeOf(c.ElemZero(td))
		el := make([]runtime.Value, len(arr))
		for i := range arr {
			el[i] = jsonShape(c, arr[i], et)
		}
		return &runtime.Slice{Elems: el, Typ: td}
	case runtime.KindMap:
		m, ok := dec.(map[string]any)
		if !ok {
			if dec == nil {
				return c.Zero(td)
			}
			return scriptVal(jsonDeep(dec))
		}
		et := c.TypeOf(c.ElemZero(td))
		rm := &runtime.Map{Pairs: map[runtime.Value]runtime.Value{}, Typ: td}
		for k, e := range m {
			kk := runtime.Value(k)
			ck := runtime.CanonicalKey(kk)
			if _, dup := rm.Pairs[ck]; !dup {
				rm.Order = append(rm.Order, kk)
			}
			rm.Pairs[ck] = jsonShape(c, e, et)
		}
		return rm
	case runtime.KindPointer:
		if dec == nil {
			return c.Zero(td)
		}
		return &runtime.Cell{Elem: jsonShape(c, dec, c.TypeOf(c.ElemZero(td)))}
	case runtime.KindInterface:
		return scriptVal(jsonDeep(dec))
	}
	return jsonScalar(c, dec, td)
}

// jsonScalar coerces a decoded JSON scalar into a builtin scalar type.
// JSON numbers always arrive as float64, so int-typed targets convert;
// anything else (null, mismatch, unknown typedef) defers to scriptVal.
func jsonScalar(c runtime.VMCaller, dec any, td *runtime.TypeDef) runtime.Value {
	switch td.Name {
	case "string":
		s, _ := dec.(string)
		return s
	case "bool":
		b, _ := dec.(bool)
		return b
	case "float64", "float32":
		f, _ := dec.(float64)
		return f
	case "int", "int8", "int16", "int32", "int64",
		"uint", "uint8", "uint16", "uint32", "uint64",
		"byte", "rune", "uintptr":
		f, _ := dec.(float64)
		return int64(f)
	}
	if dec == nil {
		return c.Zero(td)
	}
	return scriptVal(jsonDeep(dec))
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
		el := make([]runtime.Value, len(m.Order))
		for i, k := range m.Order {
			el[i] = m.Pairs[runtime.CanonicalKey(k)]
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
	s, ok := callStringer(e.c, e.v, "Error")
	if !ok {
		return fmt.Sprintf("%v", goNative(e.v))
	}
	return s
}

// Unwrap lets a script-declared `Unwrap() error` method join the host
// errors chain — errors.Unwrap/Is/As walk through it like Go's.
func (e *scriptError) Unwrap() error {
	m, ok := e.c.Member(e.v, "Unwrap")
	if !ok {
		return nil
	}
	r, err := e.c.Call(m, nil)
	if err != nil {
		return nil
	}
	if _, isNil := r.(runtime.Nil); isNil {
		return nil
	}
	return hostErrOf(e.c, r)
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
		// an untyped nil error unwraps to nothing — a typed nil
		// (TypedNil) still carries its declared error type and must
		// stay wrapped so As/Is can match on it.
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

// shortTypName reduces a typedef to its leaf identifier — "*main.MyErr"
// and "MyErr" both yield "MyErr" — for errors.As's loose target match.
func shortTypName(td *runtime.TypeDef) string {
	if td == nil {
		return ""
	}
	s := typedefSpelling(td)
	if s == "interface{}" && td.Elem != nil {
		// synthetic pointer typedefs carry the pointee in Elem
		s = typedefSpelling(td.Elem)
	}
	s = strings.TrimPrefix(s, "*")
	if i := strings.LastIndex(s, "."); i >= 0 {
		s = s[i+1:]
	}
	return s
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
	bf := &runtime.BuiltinFunc{Name: name, Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) < minArgs {
			return nil, fmt.Errorf("%s needs %d args, got %d", name, minArgs, len(args))
		}
		a := make([]any, len(args))
		for i, x := range args {
			a[i] = fmtArg(v, x)
		}
		if formatAt >= 0 && formatAt < len(a) {
			if spec, ok := a[formatAt].(string); ok {
				a[formatAt] = rewriteTypeVerbs(spec, a, args, formatAt)
			}
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

// fmtValue wraps one script value for host fmt: Format renders %v/%+v/%#v
// with Go's composite layout and honors script String/Error/GoString.
type fmtValue struct {
	c     runtime.VMCaller
	x     runtime.Value
	depth int
}

func (s *fmtValue) Format(f fmt.State, verb rune) {
	io.WriteString(f, s.render(verb, f))
}

func (s *fmtValue) render(verb rune, f fmt.State) string {
	// Stringer family first, like fmt does.
	if s.c != nil {
		switch verb {
		case 'v':
			if f.Flag('#') {
				// %#v consults GoString, not String/Error.
				if str, ok := callStringer(s.c, s.x, "GoString"); ok {
					return str
				}
				break
			}
			if str, ok := callStringer(s.c, s.x, "String"); ok {
				return withWidth(f, str)
			}
			if str, ok := callStringer(s.c, s.x, "Error"); ok {
				return withWidth(f, str)
			}
		case 's':
			if str, ok := callStringer(s.c, s.x, "String"); ok {
				return withWidth(f, str)
			}
			if str, ok := callStringer(s.c, s.x, "Error"); ok {
				return withWidth(f, str)
			}
		case 'q':
			if str, ok := callStringer(s.c, s.x, "String"); ok {
				return strconv.Quote(str)
			}
		case 'x', 'X':
			if str, ok := callStringer(s.c, s.x, "String"); ok {
				return fmt.Sprintf("%"+string(verb), str)
			}
		}
	}
	return s.renderValue(s.x, verb, f)
}

func (s *fmtValue) renderValue(x runtime.Value, verb rune, f fmt.State) string {
	if s.depth > 8 {
		return "..."
	}
	switch v := x.(type) {
	case *runtime.Named:
		// %T keeps the declared name; other verbs render through.
		if verb == 'T' {
			return typedefSpelling(v.Typ)
		}
		// an int64 carrying a uint64/uintptr tag must format its bits as
		// unsigned — host fmt would read the int64 as signed otherwise.
		if unsignedIntTyp(v.Typ) {
			if iv, ok := v.V.(int64); ok {
				return fmt.Sprintf(formatOf(f, verb), uint64(iv))
			}
		}
		return (&fmtValue{c: s.c, x: v.V, depth: s.depth + 1}).render(verb, f)
	case *runtime.Cell, *runtime.FieldRef, *runtime.IndexRef:
		dv, ok := runtime.Deref(v)
		if !ok {
			return "<nil>"
		}
		if _, isStruct := dv.(*runtime.Struct); isStruct {
			return "&" + (&fmtValue{c: s.c, x: dv, depth: s.depth + 1}).render(verb, f)
		}
		if n, isNamed := dv.(*runtime.Named); isNamed {
			if _, isStruct := n.V.(*runtime.Struct); isStruct {
				return "&" + (&fmtValue{c: s.c, x: n.V, depth: s.depth + 1}).render(verb, f)
			}
		}
		// scalar pointer: Go prints the address — a host pointer repr
		// is the closest readable stand-in.
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
			fv := (&fmtValue{c: s.c, x: e, depth: s.depth + 1}).render(elemVerb(verb), f)
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
		}
		parts := make([]string, len(v.Elems))
		for i, e := range v.Elems {
			parts[i] = (&fmtValue{c: s.c, x: e, depth: s.depth + 1}).render(elemVerb(verb), f)
		}
		if f.Flag('#') {
			return typedefSpelling(v.Typ) + "{" + strings.Join(parts, ", ") + "}"
		}
		return "[" + strings.Join(parts, " ") + "]"
	case *runtime.Map:
		switch verb {
		case 'T':
			return typedefSpelling(v.Typ)
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
			ka := (&fmtValue{c: s.c, x: a, depth: s.depth + 1}).render('v', f)
			kb := (&fmtValue{c: s.c, x: b, depth: s.depth + 1}).render('v', f)
			return ka < kb
		})
		parts := make([]string, 0, len(order))
		for _, k := range order {
			e := v.Pairs[runtime.CanonicalKey(k)]
			kr := (&fmtValue{c: s.c, x: k, depth: s.depth + 1}).render('v', f)
			vr := (&fmtValue{c: s.c, x: e, depth: s.depth + 1}).render('v', f)
			parts = append(parts, kr+":"+vr)
		}
		return "map[" + strings.Join(parts, " ") + "]"
	case *runtime.Tuple:
		parts := make([]string, len(v.Elems))
		for i, e := range v.Elems {
			parts[i] = (&fmtValue{c: s.c, x: e, depth: s.depth + 1}).render(verb, f)
		}
		return strings.Join(parts, " ")
	case *runtime.TypedNil:
		if verb == 'T' {
			return typedefSpelling(v.Typ)
		}
		return "<nil>"
	case *runtime.IfaceNil:
		if verb == 'T' {
			return typedefSpelling(v.Typ)
		}
		return "<nil>"
	case runtime.Nil:
		if verb == 'T' {
			return "<nil>"
		}
		return "<nil>"
	case *runtime.Chan:
		if verb == 'T' {
			return typedefSpelling(v.Typ)
		}
		return fmt.Sprintf("%p", v)
	case *runtime.Function, *runtime.Closure, *runtime.BoundMethod, *runtime.BuiltinFunc:
		if verb == 'T' {
			return "func"
		}
		return fmt.Sprintf("%p", v)
	case *runtime.GoValue:
		return fmt.Sprintf(formatOf(f, verb), v.V)
	case *runtime.TypeDef:
		if verb == 'T' {
			return "type"
		}
		return typedefSpelling(v)
	}
	return fmt.Sprintf(formatOf(f, verb), x)
}

// unsignedIntTyp reports whether td denotes an unsigned 64-bit integer —
// the declared name or its underlying ident (a `type U uint64` decl).
func unsignedIntTyp(td *runtime.TypeDef) bool {
	if td == nil {
		return false
	}
	switch td.Name {
	case "uint64", "uintptr":
		return true
	}
	x := td.Anon
	if x == nil && td.Spec != nil {
		x = td.Spec.Type
	}
	if id, ok := x.(*ast.Ident); ok {
		return id.Name == "uint64" || id.Name == "uintptr"
	}
	return false
}

// elemVerb picks the verb applied to elements inside a composite:
// numeric verbs descend elementwise like Go's fmt (`%c` on []rune), any
// other verb renders each element as %v.
func elemVerb(verb rune) rune {
	switch verb {
	case 'c', 'd', 'o', 'b', 'e', 'E', 'f', 'F', 'g', 'G', 'U', 'x', 'X':
		return verb
	}
	return 'v'
}

// sliceBytes reports the slice as bytes when every element is an int in
// byte range — the interpreter's model of []byte/[]rune-as-text.
func sliceBytes(v *runtime.Slice) ([]byte, bool) {
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

// scriptTypeString spells a value's type the way Go's %T does —
// "[]int", "main.Point" — using the typedef, not the Go wrapper type.
func scriptTypeString(x runtime.Value) string {
	switch t := x.(type) {
	case *runtime.Named:
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
		return typedefSpelling(t.Typ)
	case *runtime.PanicNilError:
		return "*runtime.PanicNilError"
	case *runtime.Cell, *runtime.FieldRef, *runtime.IndexRef:
		if dv, ok := runtime.Deref(t); ok {
			return "*" + scriptTypeString(dv)
		}
		return "unsafe.Pointer"
	case *runtime.GoValue:
		if se, ok := t.V.(*scriptError); ok {
			return scriptTypeString(se.v)
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
		return "func"
	default:
		return fmt.Sprintf("%T", x)
	}
}

// rewriteTypeVerbs replaces each %T in spec with %s and substitutes the
// matching arg slot with the script type spelling. Positional indexes
// %[n] count arguments after the format string, as does implicit order.
func rewriteTypeVerbs(spec string, a []any, rawArgs []runtime.Value, formatAt int) string {
	vals := rawArgs[formatAt+1:]
	off := formatAt + 1
	var sb strings.Builder
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
		i++ // past '%'
		if i >= len(spec) {
			sb.WriteByte('%')
			break
		}
		pos := -1
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
				}
				i = k + 1
			case ch == '*':
				if pos < 0 {
					seq++
				}
				i++
			case ch == '#' || ch == '+' || ch == '-' || ch == ' ' || ch == '.' || (ch >= '0' && ch <= '9'):
				i++
			default:
				goto gotVerb
			}
		}
		break
	gotVerb:
		if i >= len(spec) {
			sb.WriteString(spec[pct:])
			break
		}
		verb := spec[i]
		i++
		if verb == '%' {
			sb.WriteString("%%")
			continue
		}
		if pos < 0 {
			pos = seq
			seq++
		}
		if verb == 'T' && pos >= 0 && pos < len(vals) && off+pos < len(a) {
			sb.WriteString("%s")
			a[off+pos] = scriptTypeString(vals[pos])
		} else {
			sb.WriteString(spec[pct:i])
		}
	}
	return sb.String()
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

// callStringer invokes a declared String()/Error() method through the VM
// when the value carries one; a panicking or absent method reports false.
func callStringer(c runtime.VMCaller, x runtime.Value, name string) (string, bool) {
	m, ok := c.Member(x, name)
	if !ok {
		return "", false
	}
	r, err := c.Call(m, nil)
	if err != nil {
		return "", false
	}
	s, ok := r.(string)
	return s, ok
}

// fmtArg routes a script value into a host fmt call: scalars unbox to
// Go natives; composites keep their script shape inside a fmtValue.
func fmtArg(v runtime.VMCaller, x runtime.Value) any {
	switch x.(type) {
	case int64, float64, string, bool:
		return x
	case *runtime.GoValue:
		return goNative(x)
	default:
		return &fmtValue{c: v, x: x}
	}
}

// typedefSpelling renders a typedef for %T/#v output.
func typedefSpelling(td *runtime.TypeDef) string {
	if td == nil {
		return "interface{}"
	}
	if td.Name != "" {
		if td.Pkg != nil && td.Pkg.Name != "" {
			return td.Pkg.Name + "." + td.Name
		}
		return td.Name
	}
	if td.Anon != nil {
		return anonTypeSpelling(td.Anon)
	}
	return "interface{}"
}

// anonTypeSpelling renders an anonymous type AST for %T/#v output.
func anonTypeSpelling(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return "*" + anonTypeSpelling(t.X)
	case *ast.ArrayType:
		n := ""
		if t.Len != nil {
			if bl, ok := t.Len.(*ast.BasicLit); ok {
				n = bl.Value
			} else if id, ok := t.Len.(*ast.Ident); ok {
				n = id.Name
			}
		}
		return "[" + n + "]" + anonTypeSpelling(t.Elt)
	case *ast.MapType:
		return "map[" + anonTypeSpelling(t.Key) + "]" + anonTypeSpelling(t.Value)
	case *ast.ChanType:
		return "chan " + anonTypeSpelling(t.Value)
	case *ast.SelectorExpr:
		return anonTypeSpelling(t.X) + "." + t.Sel.Name
	case *ast.IndexExpr:
		return anonTypeSpelling(t.X) + "[" + anonTypeSpelling(t.Index) + "]"
	case *ast.ParenExpr:
		return anonTypeSpelling(t.X)
	case *ast.Ellipsis:
		return "[]" + anonTypeSpelling(t.Elt)
	case *ast.InterfaceType:
		return "interface{}"
	case *ast.StructType:
		return "struct{}"
	case *ast.FuncType:
		return "func()"
	}
	return fmt.Sprintf("%T", e)
}
