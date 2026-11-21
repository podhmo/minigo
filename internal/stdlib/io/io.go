// Package io moves the io installer and the script-reader/writer
// adapters out of the root package (P2-12). The adapters need two
// engine hooks supplied by the caller: MethodSetOf (env's
// MethodSetOfValue) and UnwrapArg (the fmt argument box unwrap).
package io

import (
	"fmt"
	"go/constant"
	"io"

	"github.com/podhmo/minigo/internal/hostbridge"
	"github.com/podhmo/minigo/internal/stdlib"
	"github.com/podhmo/minigo/runtime"
)

// Adapters carries the engine hooks the reader/writer adapters need:
// MethodSetOf reports a value's declared method set (env.MethodSetOfValue)
// and UnwrapArg peels the fmt argument box off a `&x` reference.
type Adapters struct {
	MethodSetOf func(any) map[string]bool
	UnwrapArg   func(any) any
}

// Install binds the io intrinsics: interface typedefs so script values
// can declare and satisfy them, the host sentinels (err == io.EOF must
// compare by pointer), and the Copy/Read*/Write* helpers — each adapted
// through Adapters so script-defined readers/writers resolve by method
// set.
func Install(bind stdlib.Bind, h stdlib.Helpers, ad *Adapters) {
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
					r, err := ad.AsReaderVM(vc, args[i])
					if err != nil {
						return nil, err
					}
					a[i] = r
				case 'w':
					w, err := ad.AsWriterVM(vc, args[i])
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

	bind("io", map[string]runtime.Value{
		"Reader":     &runtime.TypeDef{Name: "io.Reader", Kind: runtime.KindInterface, MReqs: []string{"Read"}},
		"Writer":     &runtime.TypeDef{Name: "io.Writer", Kind: runtime.KindInterface, MReqs: []string{"Write"}},
		"Closer":     &runtime.TypeDef{Name: "io.Closer", Kind: runtime.KindInterface, MReqs: []string{"Close"}},
		"ReadWriter": &runtime.TypeDef{Name: "io.ReadWriter", Kind: runtime.KindInterface, MReqs: []string{"Read", "Write"}},
		"Seeker":     &runtime.TypeDef{Name: "io.Seeker", Kind: runtime.KindInterface, MReqs: []string{"Seek"}},
		"ReadSeeker": &runtime.TypeDef{Name: "io.ReadSeeker", Kind: runtime.KindInterface, MReqs: []string{"Read", "Seek"}},
		"WriterAt":   &runtime.TypeDef{Name: "io.WriterAt", Kind: runtime.KindInterface, MReqs: []string{"WriteAt"}},
		"ReaderAt":   &runtime.TypeDef{Name: "io.ReaderAt", Kind: runtime.KindInterface, MReqs: []string{"ReadAt"}},
		"ReaderFrom": &runtime.TypeDef{Name: "io.ReaderFrom", Kind: runtime.KindInterface, MReqs: []string{"ReadFrom"}},
		"WriterTo":   &runtime.TypeDef{Name: "io.WriterTo", Kind: runtime.KindInterface, MReqs: []string{"WriteTo"}},
		"ByteReader": &runtime.TypeDef{Name: "io.ByteReader", Kind: runtime.KindInterface, MReqs: []string{"ReadByte"}},
		"ByteWriter": &runtime.TypeDef{Name: "io.ByteWriter", Kind: runtime.KindInterface, MReqs: []string{"WriteByte"}},
		"ByteScanner": &runtime.TypeDef{Name: "io.ByteScanner", Kind: runtime.KindInterface,
			MReqs: []string{"ReadByte", "UnreadByte"}},
		"RuneReader": &runtime.TypeDef{Name: "io.RuneReader", Kind: runtime.KindInterface, MReqs: []string{"ReadRune"}},
		"RuneScanner": &runtime.TypeDef{Name: "io.RuneScanner", Kind: runtime.KindInterface,
			MReqs: []string{"ReadRune", "UnreadRune"}},
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
		"NewSectionReader": h.Fn3("io.NewSectionReader", func(a []any) (any, error) {
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
			return &runtime.GoValue{V: io.NewSectionReader(ra, stdlib.Int64Of(a[1]), stdlib.Int64Of(a[2]))}, nil
		}, io.NewSectionReader),
		"EOF":              stdlib.ErrVal(io.EOF),
		"ErrClosedPipe":    stdlib.ErrVal(io.ErrClosedPipe),
		"ErrNoProgress":    stdlib.ErrVal(io.ErrNoProgress),
		"ErrShortBuffer":   stdlib.ErrVal(io.ErrShortBuffer),
		"ErrShortWrite":    stdlib.ErrVal(io.ErrShortWrite),
		"ErrUnexpectedEOF": stdlib.ErrVal(io.ErrUnexpectedEOF),
		"Discard":          &runtime.GoValue{V: io.Discard},
		// *io.LimitedReader assertions (http's body.readLocked probes
		// `lr.N == 0` for early-EOF) need the typedef; field reads like
		// `lr.N` dispatch on the host struct through reflection.
		"LimitedReader": stdlib.HostType("io.LimitedReader", func() any { return &io.LimitedReader{} }),
		"SectionReader": stdlib.HostType("io.SectionReader", func() any { return &io.SectionReader{} }),
		"SeekStart":     &runtime.UConst{V: constant.MakeInt64(io.SeekStart)},
		"SeekCurrent":   &runtime.UConst{V: constant.MakeInt64(io.SeekCurrent)},
		"SeekEnd":       &runtime.UConst{V: constant.MakeInt64(io.SeekEnd)},
		"ReadAll": ioCall("io.ReadAll", "r", func(a []any) (any, error) {
			b, rerr := io.ReadAll(a[0].(io.Reader))
			return &runtime.Tuple{Elems: []runtime.Value{stdlib.ScriptVal(b), stdlib.ErrVal(rerr)}}, nil
		}),
		"WriteString": ioCall("io.WriteString", "wv", func(a []any) (any, error) {
			n, werr := io.WriteString(a[0].(io.Writer), stdlib.Str(a[1]))
			return &runtime.Tuple{Elems: []runtime.Value{int64(n), stdlib.ErrVal(werr)}}, nil
		}),
		"Copy": ioCall("io.Copy", "wr", func(a []any) (any, error) {
			n, cerr := io.Copy(a[0].(io.Writer), a[1].(io.Reader))
			return &runtime.Tuple{Elems: []runtime.Value{int64(n), stdlib.ErrVal(cerr)}}, nil
		}),
		"CopyBuffer": ioCall("io.CopyBuffer", "wrv", func(a []any) (any, error) {
			n, cerr := io.CopyBuffer(a[0].(io.Writer), a[1].(io.Reader), stdlib.ByteSlice(a[2]))
			return &runtime.Tuple{Elems: []runtime.Value{int64(n), stdlib.ErrVal(cerr)}}, nil
		}),
		"CopyN": ioCall("io.CopyN", "wrv", func(a []any) (any, error) {
			n, cerr := io.CopyN(a[0].(io.Writer), a[1].(io.Reader), stdlib.Int64Of(a[2]))
			return &runtime.Tuple{Elems: []runtime.Value{int64(n), stdlib.ErrVal(cerr)}}, nil
		}),
		// ReadFull borrows the script buffer instead of a byteSlice copy —
		// a copy would drop the reader's writes and leave the script slice
		// untouched (#362).
		"ReadFull": ioCall("io.ReadFull", "rv", func(a []any) (any, error) {
			bs, commit := stdlib.BorrowBytes(a[1])
			n, rerr := io.ReadFull(a[0].(io.Reader), bs)
			commit()
			return &runtime.Tuple{Elems: []runtime.Value{int64(n), stdlib.ErrVal(rerr)}}, nil
		}),
		"ReadAtLeast": ioCall("io.ReadAtLeast", "rvv", func(a []any) (any, error) {
			bs, commit := stdlib.BorrowBytes(a[1])
			n, rerr := io.ReadAtLeast(a[0].(io.Reader), bs, stdlib.IntOf(a[2]))
			commit()
			return &runtime.Tuple{Elems: []runtime.Value{int64(n), stdlib.ErrVal(rerr)}}, nil
		}),
		"LimitReader": ioCall("io.LimitReader", "rv", func(a []any) (any, error) {
			return &runtime.GoValue{V: io.LimitReader(a[0].(io.Reader), stdlib.Int64Of(a[1]))}, nil
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
			r, err := ad.AsReaderVM(vc, args[0])
			if err != nil {
				return nil, err
			}
			return &runtime.GoValue{V: io.NopCloser(r)}, nil
		}},
		"Pipe": h.Fn("io.Pipe", func(a []any) (any, error) {
			pr, pw := io.Pipe()
			return &runtime.Tuple{Elems: []runtime.Value{&runtime.GoValue{V: pr}, &runtime.GoValue{V: pw}}}, nil
		}, io.Pipe),
	})
}

// AsWriter pulls an io.Writer out of a bound stdio handle (os.Stdout,
// os.Stderr, an *os.File) for the fmt.Fprint* family.
func (ad *Adapters) AsWriter(v any) (io.Writer, error) {
	return ad.AsWriterVM(nil, v)
}

// AsWriterVM is AsWriter plus script-defined writers: when the VM is
// available and the value carries a Write method, it adapts into an
// io.Writer that calls back into the script.
func (ad *Adapters) AsWriterVM(vc runtime.MemberCaller, v any) (io.Writer, error) {
	// args arrive fmtArg'd: a *runtime.Cell (a `&b` address-of) surfaces
	// as the fmt box — unwrap back through the reference to the box.
	v = ad.UnwrapArg(v)
	// the method-set check runs on the box (&w keeps its pointer
	// receivers); the Write callback itself resolves on the unwrapped
	// value like before.
	orig := v
	if dv, ok := runtime.Deref(orig); ok {
		v = dv
	}
	v = stdlib.HostBoxOf(orig, v)
	if g, ok := v.(*runtime.GoValue); ok {
		v = g.V
	}
	if w, ok := v.(io.Writer); ok {
		return w, nil
	}
	if vc != nil && v != nil && v != runtime.NIL {
		if m, ok := runtime.IfaceMember(vc, orig, "Write"); ok && m != nil && m != runtime.NIL {
			return &scriptWriter{v: vc, write: m, orig: orig}, nil
		}
	}
	return nil, fmt.Errorf("not an io.Writer: %T", v)
}

// AsReaderVM is AsReader plus script-defined readers: when the VM is
// available, a script value advertising a Read method adapts to io.Reader
// through a callback proxy, like asWriterVM does for writers. When the
// value also advertises WriteTo the proxy implements it too — Go's
// NopCloser picks its concrete type off that interface, so the probe
// keeps `io.nopCloserWriterTo` vs `io.nopCloser` faithful. Advertisement
// is checked on the method set, not member selection: a method promoted
// from an embedded interface (net/http's nopCloser type probes) has no
// selectable member yet still declares the interface.
func (ad *Adapters) AsReaderVM(vc runtime.MemberCaller, v any) (io.Reader, error) {
	v = ad.UnwrapArg(v)
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
	v = stdlib.HostBoxOf(orig, v)
	if g, ok := v.(*runtime.GoValue); ok {
		v = g.V
	}
	if r, ok := v.(io.Reader); ok {
		return r, nil
	}
	if vc != nil && v != nil && v != runtime.NIL {
		set := ad.MethodSetOf(orig)
		if set["Read"] {
			if set["WriteTo"] {
				return &scriptReaderWriterTo{scriptReader{v: vc, recv: orig}}, nil
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
	v    runtime.MemberCaller
	recv runtime.Value
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
		dst[i] = byte(stdlib.Int64Of(sl.Elems[i]))
	}
}

// scriptReaderWriterTo adds a WriteTo proxy: the pair of interfaces is
// what net/http's nopCloser type probes dispatch on.
type scriptReaderWriterTo struct {
	scriptReader
}

// ScriptValue hands back the script value the proxy adapts — a host
// field read returning the proxy (io.LimitedReader.R) resolves to the
// script's own concrete value, so `l.R.(*rd)` asserts like gc.
func (s *scriptReader) ScriptValue() runtime.Value { return s.recv }

// readResult decodes a script (n int, err error) pair.
func readResult(c runtime.MemberCaller, r runtime.Value) (int, error) {
	n, err := readResult64(c, r)
	return int(n), err
}

func readResult64(c runtime.MemberCaller, r runtime.Value) (int64, error) {
	if t, ok := r.(*runtime.Tuple); ok {
		var n int64
		if len(t.Elems) > 0 {
			if i, ok := runtime.Unwrap(t.Elems[0]).(int64); ok {
				n = i
			}
		}
		var werr error
		if len(t.Elems) > 1 {
			werr = hostbridge.HostErrOf(c, t.Elems[1])
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
	v     runtime.MemberCaller
	write runtime.Value
	orig  runtime.Value
}

func (w *scriptWriter) ScriptValue() runtime.Value { return w.orig }

// AsReader mirrors AsWriter for io.Reader args: `strings.NewReader`'s
// GoValue unwraps to the real reader, `&r` cells deref through. A nil
// argument is a valid (nil) io.Reader — `io.NopCloser(nil)` is legal Go.
func (ad *Adapters) AsReader(v any) (io.Reader, error) {
	v = ad.UnwrapArg(v)
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

func (s *scriptReader) Read(p []byte) (int, error) {
	m, ok := s.v.Member(s.recv, "Read")
	if !ok || m == nil || m == runtime.NIL {
		return 0, fmt.Errorf("runtime error: invalid memory address or nil pointer dereference")
	}
	pv := stdlib.ScriptVal(p)
	r, err := s.v.Call(m, []runtime.Value{pv})
	if err != nil {
		// a panic inside the script Read propagates as a panic like
		// callOrPanic — io.Copy's caller must not see "panic: boom"
		// as an error return.
		panic(err)
	}
	n, err2 := readResult(s.v, r)
	copyBackBytes(p, pv, n)
	return n, err2
}

func (s *scriptReaderWriterTo) Read(p []byte) (int, error) {
	m, ok := s.v.Member(s.recv, "Read")
	if !ok || m == nil || m == runtime.NIL {
		return 0, fmt.Errorf("runtime error: invalid memory address or nil pointer dereference")
	}
	pv := stdlib.ScriptVal(p)
	r, err := s.v.Call(m, []runtime.Value{pv})
	if err != nil {
		panic(err)
	}
	n, err2 := readResult(s.v, r)
	copyBackBytes(p, pv, n)
	return n, err2
}

func (s *scriptReaderWriterTo) WriteTo(w io.Writer) (int64, error) {
	m, ok := s.v.Member(s.recv, "WriteTo")
	if !ok || m == nil || m == runtime.NIL {
		return 0, fmt.Errorf("runtime error: invalid memory address or nil pointer dereference")
	}
	r, err := s.v.Call(m, []runtime.Value{&runtime.GoValue{V: w}})
	if err != nil {
		panic(err)
	}
	return readResult64(s.v, r)
}

func (s *scriptWriter) Write(p []byte) (int, error) {
	r, err := s.v.Call(s.write, []runtime.Value{stdlib.ScriptVal(p)})
	if err != nil {
		panic(err)
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
			return n, hostbridge.HostErrOf(s.v, t.Elems[1])
		}
		return n, nil
	default:
		if i, ok := runtime.Unwrap(r).(int64); ok {
			return int(i), nil
		}
		return 0, nil
	}
}
