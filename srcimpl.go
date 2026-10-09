package minigo

import (
	"os"
	goruntime "runtime"
	"sync"
	"sync/atomic"
	"weak"

	"github.com/podhmo/minigo/bytecode"
	"github.com/podhmo/minigo/index"
	"github.com/podhmo/minigo/internal/tmpllex"
	"github.com/podhmo/minigo/runtime"
)

// srcImpl replaces individual methods of interpreted stdlib packages
// with host implementations. Unlike asmImpls the declaration has a Go
// body; the package itself stays source-interpreted, so every value the
// rest of the package sees is a script value with Go's semantics.
//
// text/template/parse's lexer runs a per-rune state machine that
// dominated template parsing; (*lexer).nextItem is lexed by the host
// copy in internal/tmpllex while the parser and its trees stay script.
//
// srcImpl returns a chunk calling the host implementation of a
// method, or nil to compile the source body. (A switch, not a table: a
// table would close an initialization cycle through materialize.)
func (e *Engine) srcImpl(pkg *runtime.Package, recv string, md *index.Decl) *bytecode.Chunk {
	var b *runtime.BuiltinFunc
	switch pkg.Path + "." + recv + "." + md.Name {
	case "text/template/parse.lexer.nextItem":
		b = tmplNextItem(e, pkg, md)
	}
	if b == nil {
		return nil
	}
	// return impl(recv): slot 0 holds the receiver
	return &bytecode.Chunk{
		Name:   recv + "." + md.Name,
		Consts: []any{b},
		Code: []bytecode.Instruction{
			{Op: bytecode.OpConst, A: 0, C: -1},
			{Op: bytecode.OpLocal, A: 0, C: -1},
			{Op: bytecode.OpNil, C: -1}, // no static arg type
			{Op: bytecode.OpCall, A: 1, C: -1},
			{Op: bytecode.OpReturn, A: 1, C: -1},
		},
		NLocals: 1, NParams: 1, NResults: 1,
	}
}

// hostLexers maps a script *lexer struct to its host lexer. Entries go
// away when the lexer reaches EOF or an error, or when the script
// struct is collected (a parse error abandons the lexer mid-input).
var hostLexers struct {
	sync.Mutex
	m map[weak.Pointer[runtime.Struct]]*tmpllex.Lexer
}

// hostLexerStarts counts host lexers started (tests check the hook
// fires rather than silently falling back to the interpreted lexer).
var hostLexerStarts atomic.Int64

// tmplNextItem lexes on the host when the toolchain's lex.go is the
// one internal/tmpllex copies; a drifted lexer stays interpreted.
func tmplNextItem(e *Engine, pkg *runtime.Package, md *index.Decl) *runtime.BuiltinFunc {
	if md.File == nil {
		return nil
	}
	src, err := os.ReadFile(md.File.Name)
	if err != nil || !tmpllex.SameSource(src) {
		return nil
	}
	var (
		once                 sync.Once
		itemTD, typTD, posTD *runtime.TypeDef
		tdErr                error
	)
	typeDef := func(name string) *runtime.TypeDef {
		info, ok := pkg.Index.Types[name]
		if !ok {
			return nil
		}
		v, err := e.materialize(pkg, info.Decl)
		if err != nil {
			tdErr = err
			return nil
		}
		td, _ := v.(*runtime.TypeDef)
		return td
	}
	return &runtime.BuiltinFunc{Name: "text/template/parse.lexer.nextItem", Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		once.Do(func() {
			itemTD, typTD, posTD = typeDef("item"), typeDef("itemType"), typeDef("Pos")
		})
		if tdErr != nil {
			return nil, tdErr
		}
		s := scriptStruct(args[0])
		if s == nil || itemTD == nil || typTD == nil || posTD == nil {
			return nil, errNilLexer
		}
		x := hostLexer(s)
		opts := structField(s, "options")
		x.SetOptions(tmpllex.Options{
			EmitComment: fieldBool(opts, "emitComment"),
			BreakOK:     fieldBool(opts, "breakOK"),
			ContinueOK:  fieldBool(opts, "continueOK"),
		})
		it := x.NextItem()
		if it.Typ == tmpllex.ItemEOF || it.Typ == tmpllex.ItemError {
			hostLexers.Lock()
			delete(hostLexers.m, weak.Make(s))
			hostLexers.Unlock()
		}
		return &runtime.Struct{Def: itemTD, Fields: []runtime.Value{
			runtime.Tag(typTD, int64(it.Typ)),
			runtime.Tag(posTD, int64(it.Pos)),
			it.Val,
			int64(it.Line),
		}}, nil
	}}
}

type nilLexerError struct{}

func (nilLexerError) Error() string { return "text/template/parse: nextItem on a nil lexer" }

var errNilLexer error = nilLexerError{}

// hostLexer returns s's host lexer, starting one from the fields lex
// set (the defaulted delimiters included).
func hostLexer(s *runtime.Struct) *tmpllex.Lexer {
	k := weak.Make(s)
	hostLexers.Lock()
	defer hostLexers.Unlock()
	if x, ok := hostLexers.m[k]; ok {
		return x
	}
	if hostLexers.m == nil {
		hostLexers.m = map[weak.Pointer[runtime.Struct]]*tmpllex.Lexer{}
	}
	x := tmpllex.New(fieldString(s, "name"), fieldString(s, "input"),
		fieldString(s, "leftDelim"), fieldString(s, "rightDelim"))
	hostLexers.m[k] = x
	hostLexerStarts.Add(1)
	goruntime.AddCleanup(s, func(k weak.Pointer[runtime.Struct]) {
		hostLexers.Lock()
		delete(hostLexers.m, k)
		hostLexers.Unlock()
	}, k)
	return x
}

// scriptStruct peels a receiver (pointer cell, tag) down to its struct.
func scriptStruct(v runtime.Value) *runtime.Struct {
	for range 8 {
		switch t := runtime.Unwrap(v).(type) {
		case *runtime.Struct:
			return t
		default:
			d, ok := runtime.Deref(t)
			if !ok {
				return nil
			}
			v = d
		}
	}
	return nil
}

func structField(s *runtime.Struct, name string) runtime.Value {
	if s == nil {
		return nil
	}
	for i, f := range s.Def.Fields {
		if f == name && i < len(s.Fields) {
			return s.Fields[i]
		}
	}
	return nil
}

func fieldString(s *runtime.Struct, name string) string {
	str, _ := runtime.Unwrap(structField(s, name)).(string)
	return str
}

func fieldBool(v runtime.Value, name string) bool {
	b, _ := runtime.Unwrap(structField(scriptStruct(v), name)).(bool)
	return b
}
