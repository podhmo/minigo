package minigo

import (
	"bufio"
	"fmt"
	"go/ast"
	"io"

	"github.com/podhmo/minigo/runtime"
)

func (e *Engine) installBufio(h *hostHelpers) {
	// bufio: Scanner/Reader/Writer box the host types — Scan/Text/Err and
	// friends dispatch through reflection.
	e.Bind("bufio", map[string]runtime.Value{
		// the sentinel is the real host error: `err == bufio.ErrBufferFull`
		// in csv's reader compares GoValues by identity.
		"ErrBufferFull":        errVal(bufio.ErrBufferFull),
		"ErrInvalidUnreadByte": errVal(bufio.ErrInvalidUnreadByte),
		"ErrInvalidUnreadRune": errVal(bufio.ErrInvalidUnreadRune),
		"ErrNegativeCount":     errVal(bufio.ErrNegativeCount),
		"ErrTooLong":           errVal(bufio.ErrTooLong),
		"ErrNegativeAdvance":   errVal(bufio.ErrNegativeAdvance),
		"ErrAdvanceTooFar":     errVal(bufio.ErrAdvanceTooFar),
		"ErrBadReadCount":      errVal(bufio.ErrBadReadCount),
		// ErrFinalToken must be the host object: the host Scanner stops
		// on `err == ErrFinalToken` when a script split func returns it.
		"ErrFinalToken": errVal(bufio.ErrFinalToken),
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
		"ScanBytes": hostSplitFunc("bufio.ScanBytes", bufio.ScanBytes),
		"ScanRunes": hostSplitFunc("bufio.ScanRunes", bufio.ScanRunes),
		"ScanLines": hostSplitFunc("bufio.ScanLines", bufio.ScanLines),
		"ScanWords": hostSplitFunc("bufio.ScanWords", bufio.ScanWords),
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
}
