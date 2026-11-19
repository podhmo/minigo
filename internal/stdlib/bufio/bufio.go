package bufio

import (
	"bufio"
	"fmt"
	"go/ast"
	"io"

	"github.com/podhmo/minigo/internal/hostbridge"
	"github.com/podhmo/minigo/internal/stdlib"
	stdio "github.com/podhmo/minigo/internal/stdlib/io"
	"github.com/podhmo/minigo/runtime"
)

// Install binds bufio: Scanner/Reader/Writer box the host types and the
// sentinels are the real host errors so `err == bufio.X` compares by
// identity.
func Install(bind stdlib.Bind, h stdlib.Helpers, ad *stdio.Adapters) {
	// bufio: Scanner/Reader/Writer box the host types — Scan/Text/Err and
	// friends dispatch through reflection.
	bind("bufio", map[string]runtime.Value{
		// the sentinel is the real host error: `err == bufio.ErrBufferFull`
		// in csv's reader compares GoValues by identity.
		"ErrBufferFull":        stdlib.ErrVal(bufio.ErrBufferFull),
		"ErrInvalidUnreadByte": stdlib.ErrVal(bufio.ErrInvalidUnreadByte),
		"ErrInvalidUnreadRune": stdlib.ErrVal(bufio.ErrInvalidUnreadRune),
		"ErrNegativeCount":     stdlib.ErrVal(bufio.ErrNegativeCount),
		"ErrTooLong":           stdlib.ErrVal(bufio.ErrTooLong),
		"ErrNegativeAdvance":   stdlib.ErrVal(bufio.ErrNegativeAdvance),
		"ErrAdvanceTooFar":     stdlib.ErrVal(bufio.ErrAdvanceTooFar),
		"ErrBadReadCount":      stdlib.ErrVal(bufio.ErrBadReadCount),
		// ErrFinalToken must be the host object: the host Scanner stops
		// on `err == ErrFinalToken` when a script split func returns it.
		"ErrFinalToken": stdlib.ErrVal(bufio.ErrFinalToken),
		// the stock split funcs are host funcs: callable from script
		// (a custom split delegating to ScanWords) and handed to
		// Scanner.Split as themselves.
		"SplitFunc": &runtime.TypeDef{Name: "bufio.SplitFunc", Kind: runtime.KindFunc,
			Anon: &ast.FuncType{
				Params: &ast.FieldList{List: []*ast.Field{
					{Type: &ast.ArrayType{Elt: ast.NewIdent("byte")}},
					{Type: ast.NewIdent("bool")},
				}},
				Results: &ast.FieldList{List: []*ast.Field{
					{Type: ast.NewIdent("int")},
					{Type: &ast.ArrayType{Elt: ast.NewIdent("byte")}},
					{Type: ast.NewIdent("error")},
				}},
			}},
		"ScanBytes": splitFuncBind("bufio.ScanBytes", bufio.ScanBytes),
		"ScanRunes": splitFuncBind("bufio.ScanRunes", bufio.ScanRunes),
		"ScanLines": splitFuncBind("bufio.ScanLines", bufio.ScanLines),
		"ScanWords": splitFuncBind("bufio.ScanWords", bufio.ScanWords),
		// type assertions like `w.(*bufio.Writer)` need the typedefs;
		// methods run on the boxed host object through reflection.
		"Reader":     stdlib.HostType("bufio.Reader", func() any { return bufio.NewReader(nil) }),
		"Writer":     stdlib.HostType("bufio.Writer", func() any { return bufio.NewWriter(io.Discard) }),
		"Scanner":    stdlib.HostType("bufio.Scanner", func() any { return bufio.NewScanner(nil) }),
		"ReadWriter": stdlib.HostType("bufio.ReadWriter", func() any { return bufio.NewReadWriter(bufio.NewReader(nil), bufio.NewWriter(io.Discard)) }),
		"NewScanner": &runtime.BuiltinFunc{Name: "bufio.NewScanner", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("bufio.NewScanner needs 1 arg")
			}
			r, err := ad.AsReaderVM(vc, args[0])
			if err != nil {
				return nil, err
			}
			return &runtime.GoValue{V: bufio.NewScanner(r)}, nil
		}},
		"NewReader": &runtime.BuiltinFunc{Name: "bufio.NewReader", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("bufio.NewReader needs 1 arg")
			}
			r, err := ad.AsReaderVM(vc, args[0])
			if err != nil {
				return nil, err
			}
			return &runtime.GoValue{V: bufio.NewReader(r)}, nil
		}},
		"NewReaderSize": &runtime.BuiltinFunc{Name: "bufio.NewReaderSize", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("bufio.NewReaderSize needs 2 args")
			}
			r, err := ad.AsReaderVM(vc, args[0])
			if err != nil {
				return nil, err
			}
			return &runtime.GoValue{V: bufio.NewReaderSize(r, int(stdlib.Int64Of(args[1])))}, nil
		}},
		"NewWriter": &runtime.BuiltinFunc{Name: "bufio.NewWriter", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("bufio.NewWriter needs 1 arg")
			}
			w, err := ad.AsWriterVM(vc, args[0])
			if err != nil {
				return nil, err
			}
			return &runtime.GoValue{V: bufio.NewWriter(w)}, nil
		}},
		"NewWriterSize": &runtime.BuiltinFunc{Name: "bufio.NewWriterSize", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("bufio.NewWriterSize needs 2 args")
			}
			w, err := ad.AsWriterVM(vc, args[0])
			if err != nil {
				return nil, err
			}
			return &runtime.GoValue{V: bufio.NewWriterSize(w, int(stdlib.Int64Of(args[1])))}, nil
		}},
	})
}

// hostSplitFunc binds one of bufio's stock split funcs: a script call
// returns the (advance, token, err) triple, and Target hands the host
// func itself to Scanner.Split.
func splitFuncBind(name string, f bufio.SplitFunc) *runtime.BuiltinFunc {
	// the Target is the unnamed func type: gc's stock split funcs are
	// declared as plain funcs (%T spells the signature), and an unnamed
	// func still assigns to a SplitFunc parameter.
	var target func([]byte, bool) (int, []byte, error) = f
	return &runtime.BuiltinFunc{Name: name, Target: target, Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != 2 {
			return nil, fmt.Errorf("%s needs 2 args, got %d", name, len(args))
		}
		atEOF, _ := runtime.Unwrap(args[1]).(bool)
		adv, tok, err := f(stdlib.ByteSlice(hostbridge.GoNative(args[0])), atEOF)
		return &runtime.Tuple{Elems: []runtime.Value{int64(adv), stdlib.ScriptVal(tok), stdlib.ErrVal(err)}}, nil
	}}
}
