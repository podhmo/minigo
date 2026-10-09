package minigo

// inspect.go — the intrinsic table behind the "minigo.dev/inspect" stub
// package (docs/sketch/plan-package-introspection.md). Views are boxed
// as *runtime.GoValue carrying inspect.Decl/File/Import/Field/TypeExpr
// pointers, so scripts read fields through reflective member dispatch;
// unexported fields on those views hold the AST context.
//
// Locators take strings (PackageOf/DirOf/FileOf); symbol access takes
// values (OwnerOf/SymbolOf); *runtime.ImportRef is accepted anywhere a
// package is expected.

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	goruntime "runtime"
	"strings"

	"github.com/podhmo/minigo/index"
	xinspect "github.com/podhmo/minigo/inspect"
	"github.com/podhmo/minigo/runtime"
)

func (e *Engine) installInspect() {
	bf := func(name string, fn func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error)) *runtime.BuiltinFunc {
		return &runtime.BuiltinFunc{Name: "inspect." + name, Fn: fn}
	}
	argerr := func(name string, want string) error {
		return fmt.Errorf("inspect.%s expects %s", name, want)
	}

	pkg := map[string]runtime.Value{
		// the pseudo import path predeclared identifiers resolve to —
		// bound as a plain string so interpreted code can compare a
		// SymbolID's PackagePath against it directly.
		"BuiltinPackagePath": xinspect.BuiltinPackagePath,
		// ---- locators ----
		"PackageOf": bf("PackageOf", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 1 {
				return nil, argerr("PackageOf", "one path string")
			}
			return e.loadPath(context.Background(), str(runtime.Unwrap(args[0])))
		}),
		"SourceOf": bf("SourceOf", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 1 {
				return nil, argerr("SourceOf", "one path string")
			}
			return e.SourceOf(context.Background(), str(runtime.Unwrap(args[0])))
		}),
		"DirOf": bf("DirOf", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 1 {
				return nil, argerr("DirOf", "one dir string")
			}
			return e.loadDir(context.Background(), str(runtime.Unwrap(args[0])))
		}),
		"FileOf": bf("FileOf", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 1 {
				return nil, argerr("FileOf", "one file string")
			}
			name := str(runtime.Unwrap(args[0]))
			p, err := e.LoadFile(context.Background(), name)
			if err != nil {
				return nil, err
			}
			abs, _ := filepath.Abs(name)
			sf := p.FileByName[abs]
			if sf == nil {
				for _, f := range p.Files {
					sf = f // single-file package: fall back to its only file
					break
				}
			}
			if sf == nil {
				return nil, fmt.Errorf("inspect.FileOf: no file view for %s", name)
			}
			return &runtime.GoValue{V: xinspect.NewFile(p, sf)}, nil
		}),
		"Current": bf("Current", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			p := v.Package()
			if p == nil {
				return nil, fmt.Errorf("inspect.Current: no running frame")
			}
			return p, nil
		}),
		// ---- symbol -> package / value -> decl ----
		"OwnerOf": bf("OwnerOf", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 1 {
				return nil, argerr("OwnerOf", "one value")
			}
			return e.ownerOf(runtime.Unwrap(args[0]))
		}),
		"PathOf": bf("PathOf", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 1 {
				return nil, argerr("PathOf", "one value")
			}
			p, err := e.ownerOf(runtime.Unwrap(args[0]))
			if err != nil {
				return nil, err
			}
			if p == nil {
				return runtime.NIL, nil
			}
			return p.Path, nil
		}),
		"SymbolIDOf": bf("SymbolIDOf", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 1 {
				return nil, argerr("SymbolIDOf", "one value")
			}
			d, err := e.symbolOf(runtime.Unwrap(args[0]))
			if err != nil {
				return nil, err
			}
			if d == nil {
				return runtime.NIL, nil
			}
			path := ""
			if d.Package != nil {
				path = d.Package.Path
			}
			return &runtime.GoValue{V: runtime.SymbolID{PackagePath: path, Name: d.Name}}, nil
		}),
		"SymbolOf": bf("SymbolOf", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 1 {
				return nil, argerr("SymbolOf", "one value")
			}
			d, err := e.symbolOf(runtime.Unwrap(args[0]))
			if err != nil {
				return nil, err
			}
			if d == nil {
				return runtime.NIL, nil
			}
			return &runtime.GoValue{V: d}, nil
		}),
		// ---- index layer ----
		"Decls": bf("Decls", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 1 {
				return nil, argerr("Decls", "a package or file view")
			}
			return e.declsOf(runtime.Unwrap(args[0]))
		}),
		"Symbol": bf("Symbol", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, argerr("Symbol", "a package and a name")
			}
			p, err := e.pkgOf(runtime.Unwrap(args[0]))
			if err != nil {
				return nil, err
			}
			return e.lookupSymbol(p, str(runtime.Unwrap(args[1])))
		}),
		"Files": bf("Files", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 1 {
				return nil, argerr("Files", "a package")
			}
			p, err := e.pkgOf(runtime.Unwrap(args[0]))
			if err != nil {
				return nil, err
			}
			xs := make([]runtime.Value, len(p.Files))
			for i, sf := range p.Files {
				xs[i] = &runtime.GoValue{V: xinspect.NewFile(p, sf)}
			}
			return &runtime.Slice{Elems: xs}, nil
		}),
		"Imports": bf("Imports", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 1 {
				return nil, argerr("Imports", "a file view")
			}
			f, err := fileViewOf(args[0])
			if err != nil {
				return nil, err
			}
			imps := xinspect.ImportsOf(f)
			xs := make([]runtime.Value, len(imps))
			for i, imp := range imps {
				xs[i] = &runtime.GoValue{V: imp}
			}
			return &runtime.Slice{Elems: xs}, nil
		}),
		// ---- metadata ----
		"Kind": bf("Kind", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			s, err := declViewOf(args[0])
			if err != nil {
				return nil, err
			}
			return s.Kind, nil
		}),
		"Doc": bf("Doc", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			s, err := declViewOf(args[0])
			if err != nil {
				return nil, err
			}
			return s.Doc, nil
		}),
		"Pos": bf("Pos", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			s, err := declViewOf(args[0])
			if err != nil {
				return nil, err
			}
			if s.Pos == nil {
				return runtime.NIL, nil
			}
			return &runtime.GoValue{V: s.Pos}, nil
		}),
		"Name": bf("Name", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 1 {
				return nil, argerr("Name", "one value")
			}
			switch x := runtime.Unwrap(args[0]).(type) {
			case *runtime.Package:
				return x.Name, nil
			case *runtime.ImportRef:
				p, err := x.Materialize()
				if err != nil {
					return nil, err
				}
				return p.Name, nil
			default:
				s, err := declViewOf(args[0])
				if err != nil {
					return nil, err
				}
				return s.Name, nil
			}
		}),
		"Path": bf("Path", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			p, err := e.pkgOf(runtime.Unwrap(args[0]))
			if err != nil {
				return nil, err
			}
			return p.Path, nil
		}),
		"Dir": bf("Dir", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			p, err := e.pkgOf(runtime.Unwrap(args[0]))
			if err != nil {
				return nil, err
			}
			return filepath.ToSlash(p.Dir), nil
		}),
		"State": bf("State", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			p, err := e.pkgOf(runtime.Unwrap(args[0]))
			if err != nil {
				return nil, err
			}
			return stateName(p.State()), nil
		}),
		"Standard": bf("Standard", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			p, err := e.pkgOf(runtime.Unwrap(args[0]))
			if err != nil {
				return nil, err
			}
			return p.Standard, nil
		}),
		"SymbolID": bf("SymbolID", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 1 {
				return nil, argerr("SymbolID", "a decl or type expression")
			}
			if te, err := typeExprOf(args[0]); err == nil {
				sid, ok := te.SymbolID()
				if !ok {
					return runtime.NIL, nil
				}
				return &runtime.GoValue{V: &sid}, nil
			}
			s, err := declViewOf(args[0])
			if err != nil {
				return nil, err
			}
			path := ""
			if s.Package != nil {
				path = s.Package.Path
			}
			return &runtime.GoValue{V: &runtime.SymbolID{PackagePath: path, Name: s.Name}}, nil
		}),
		// ---- syntax layer ----
		"Fields": bf("Fields", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			s, err := declViewOf(args[0])
			if err != nil {
				return nil, err
			}
			fs, err := xinspect.FieldsOf(s)
			if err != nil {
				return nil, err
			}
			return boxedSlice(fs), nil
		}),
		"Methods": bf("Methods", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 1 {
				return nil, argerr("Methods", "a type decl, a package, or a file view")
			}
			if s, err := declViewOf(args[0]); err == nil {
				ms, err := xinspect.MethodsOf(s)
				if err != nil {
					return nil, err
				}
				return boxedSlice(ms), nil
			}
			if f, err := fileViewOf(args[0]); err == nil {
				ms, err := xinspect.MethodsIn(f.Pkg(), f.SyntaxFile())
				if err != nil {
					return nil, err
				}
				return boxedSlice(ms), nil
			}
			switch u := runtime.Unwrap(args[0]).(type) {
			case *runtime.Package, *runtime.ImportRef:
				p, err := e.pkgOf(u)
				if err != nil {
					return nil, err
				}
				ms, err := xinspect.MethodsIn(p, nil)
				if err != nil {
					return nil, err
				}
				return boxedSlice(ms), nil
			default:
				return nil, argerr("Methods", "a type decl, a package, or a file view")
			}
		}),
		"MethodSet": bf("MethodSet", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			s, err := declViewOf(args[0])
			if err != nil {
				return nil, err
			}
			ms, err := xinspect.MethodSetOf(s, e.resolverForInspect())
			if err != nil {
				return nil, err
			}
			return boxedSlice(ms), nil
		}),
		"Implementers": bf("Implementers", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, argerr("Implementers", "a package and an interface decl")
			}
			p, err := e.pkgOf(runtime.Unwrap(args[0]))
			if err != nil {
				return nil, err
			}
			s, err := declViewOf(args[1])
			if err != nil {
				return nil, err
			}
			ds, err := xinspect.ImplementersOf(p, s, e.resolverForInspect())
			if err != nil {
				return nil, err
			}
			return boxedSlice(ds), nil
		}),
		"EnumMembers": bf("EnumMembers", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			s, err := declViewOf(args[0])
			if err != nil {
				return nil, err
			}
			ms, err := xinspect.EnumMembersOf(s)
			if err != nil {
				return nil, err
			}
			return boxedSlice(ms), nil
		}),
		"MReqs": bf("MReqs", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			s, err := declViewOf(args[0])
			if err != nil {
				return nil, err
			}
			fs, err := xinspect.MReqsOf(s)
			if err != nil {
				return nil, err
			}
			return boxedSlice(fs), nil
		}),
		"IEmbeds": bf("IEmbeds", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			s, err := declViewOf(args[0])
			if err != nil {
				return nil, err
			}
			es, err := xinspect.IEmbedsOf(s)
			if err != nil {
				return nil, err
			}
			return boxedSlice(es), nil
		}),
		"AST": bf("AST", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			s, err := declViewOf(args[0])
			if err != nil {
				return nil, err
			}
			node, err := xinspect.ASTOf(s)
			if err != nil {
				return nil, err
			}
			return &runtime.GoValue{V: node}, nil
		}),
		"Signature": bf("Signature", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			s, err := declViewOf(args[0])
			if err != nil {
				return nil, err
			}
			sig, err := xinspect.SignatureOf(s)
			if err != nil {
				return nil, err
			}
			return &runtime.GoValue{V: sig}, nil
		}),
		"TypeParams": bf("TypeParams", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			s, err := declViewOf(args[0])
			if err != nil {
				return nil, err
			}
			fs, err := xinspect.TypeParamsOf(s)
			if err != nil {
				return nil, err
			}
			return boxedSlice(fs), nil
		}),
		"Def": bf("Def", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			s, err := declViewOf(args[0])
			if err != nil {
				return nil, err
			}
			te, err := xinspect.DefOf(s)
			if err != nil {
				return nil, err
			}
			return &runtime.GoValue{V: te}, nil
		}),
		"DeclType": bf("DeclType", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			s, err := declViewOf(args[0])
			if err != nil {
				return nil, err
			}
			te, err := xinspect.DeclTypeOf(s)
			if err != nil {
				return nil, err
			}
			if te == nil {
				return runtime.NIL, nil
			}
			return &runtime.GoValue{V: te}, nil
		}),
		"Initializer": bf("Initializer", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			s, err := declViewOf(args[0])
			if err != nil {
				return nil, err
			}
			te, err := xinspect.InitializerOf(s)
			if err != nil {
				return nil, err
			}
			if te == nil {
				return runtime.NIL, nil
			}
			return &runtime.GoValue{V: te}, nil
		}),
		"IsAlias": bf("IsAlias", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			s, err := declViewOf(args[0])
			if err != nil {
				return nil, err
			}
			return xinspect.IsAliasOf(s)
		}),
		"Children": bf("Children", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			te, err := typeExprOf(args[0])
			if err != nil {
				return nil, err
			}
			return boxedSlice(te.Children()), nil
		}),
		"TypeFields": bf("TypeFields", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			te, err := typeExprOf(args[0])
			if err != nil {
				return nil, err
			}
			fs, err := xinspect.TypeFieldsOf(te)
			if err != nil {
				return nil, err
			}
			return boxedSlice(fs), nil
		}),
		"UnWrap": bf("UnWrap", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			te, err := typeExprOf(args[0])
			if err != nil {
				return nil, err
			}
			return &runtime.GoValue{V: te.Unwrap(e.resolverForInspect())}, nil
		}),
		"UnRef": bf("UnRef", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			te, err := typeExprOf(args[0])
			if err != nil {
				return nil, err
			}
			return &runtime.GoValue{V: te.Unref()}, nil
		}),
		"Origin": bf("Origin", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			te, err := typeExprOf(args[0])
			if err != nil {
				return nil, err
			}
			return &runtime.GoValue{V: te.Origin(e.resolverForInspect())}, nil
		}),
		"Resolve": bf("Resolve", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			te, err := typeExprOf(args[0])
			if err != nil {
				return nil, err
			}
			sid, ok := te.SymbolID()
			if !ok || sid.PackagePath == xinspect.BuiltinPackagePath {
				return runtime.NIL, nil
			}
			d, err := e.resolverForInspect()(sid)
			if err != nil {
				return nil, err
			}
			return &runtime.GoValue{V: d}, nil
		}),
		"SameType": bf("SameType", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, argerr("SameType", "two type expressions")
			}
			a, err := typeExprOf(args[0])
			if err != nil {
				return nil, err
			}
			b, err := typeExprOf(args[1])
			if err != nil {
				return nil, err
			}
			return a.SameType(b, e.resolverForInspect()), nil
		}),
		"UsedSymbols": bf("UsedSymbols", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			f, err := fileViewOf(args[0])
			if err != nil {
				return nil, err
			}
			sids := xinspect.UsedSymbolsOf(f)
			xs := make([]runtime.Value, len(sids))
			for i, sid := range sids {
				sid := sid
				xs[i] = &runtime.GoValue{V: sid}
			}
			return &runtime.Slice{Elems: xs}, nil
		}),
		// ---- value layer ----
		"Value": bf("Value", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, argerr("Value", "a package and a name")
			}
			p, err := e.pkgOf(runtime.Unwrap(args[0]))
			if err != nil {
				return nil, err
			}
			m, err := p.Member(str(runtime.Unwrap(args[1])), e.materialize)
			if err != nil {
				return nil, err
			}
			// a const's read-only cell is an internal handle — hand the
			// constant across the boundary, not a *string-lookalike.
			// Writable cells (vars) stay cells: `*v` must keep
			// dereferencing the storage.
			if c, ok := m.(*runtime.Cell); ok && c.ReadOnly {
				return c.Elem, nil
			}
			return m, nil
		}),
		"TypeOf": bf("TypeOf", func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			s, err := declViewOf(args[0])
			if err != nil {
				return nil, err
			}
			d := s.DeclOf()
			if d == nil || s.Package == nil {
				return nil, fmt.Errorf("inspect.TypeOf: %s is not a decl", s.Name)
			}
			if d.Kind != index.TypeDecl {
				return nil, fmt.Errorf("inspect.TypeOf: %s is a %s, not a type", s.Name, s.Kind)
			}
			return e.materialize(s.Package, d)
		}),
	}
	for _, path := range []string{"minigo.dev/inspect", "github.com/podhmo/minigo/inspect"} {
		e.Bind(path, pkg)
	}
}

func stateName(s runtime.State) string {
	switch s {
	case runtime.Unseen:
		return "unseen"
	case runtime.Located:
		return "located"
	case runtime.Parsed:
		return "parsed"
	case runtime.Indexed:
		return "indexed"
	case runtime.Initializing:
		return "initializing"
	case runtime.Ready:
		return "ready"
	case runtime.Failed:
		return "failed"
	}
	return "unknown"
}

func boxedSlice[T any](xs []T) *runtime.Slice {
	vs := make([]runtime.Value, len(xs))
	for i, x := range xs {
		vs[i] = &runtime.GoValue{V: x}
	}
	return &runtime.Slice{Elems: vs}
}

func goView[T any](v runtime.Value, what string) (*T, error) {
	gv, ok := runtime.Unwrap(v).(*runtime.GoValue)
	if !ok {
		return nil, fmt.Errorf("inspect: expected a %s view, got %T", what, v)
	}
	x, ok := gv.V.(*T)
	if !ok {
		return nil, fmt.Errorf("inspect: expected a %s view, got %T", what, gv.V)
	}
	return x, nil
}

func declViewOf(v runtime.Value) (*xinspect.Decl, error) { return goView[xinspect.Decl](v, "decl") }
func fileViewOf(v runtime.Value) (*xinspect.File, error) { return goView[xinspect.File](v, "file") }
func typeExprOf(v runtime.Value) (*xinspect.TypeExpr, error) {
	return goView[xinspect.TypeExpr](v, "type expression")
}

// pkgOf normalizes package-ish values: *runtime.Package or
// *runtime.ImportRef (materialized lazily, no init).
func (e *Engine) pkgOf(v runtime.Value) (*runtime.Package, error) {
	switch x := v.(type) {
	case *runtime.Package:
		return x, nil
	case *runtime.ImportRef:
		return x.Materialize()
	}
	return nil, fmt.Errorf("inspect: expected a package, got %T", v)
}

// resolverForInspect binds the SymbolID -> decl view resolver used by
// UnWrap/Origin/Resolve/SameType.
func (e *Engine) resolverForInspect() xinspect.Resolver {
	return func(sid runtime.SymbolID) (*xinspect.Decl, error) {
		p, err := e.loadPath(context.Background(), sid.PackagePath)
		if err != nil {
			return nil, err
		}
		d, err := e.lookupDeclIn(p, sid.Name)
		if err != nil {
			return nil, err
		}
		if d == nil {
			return nil, fmt.Errorf("inspect: no decl %s in %s", sid.Name, sid.PackagePath)
		}
		return xinspect.NewDecl(p, d), nil
	}
}

// lookupDeclIn finds a top-level decl by name in an indexed package.
func (e *Engine) lookupDeclIn(p *runtime.Package, name string) (*index.Decl, error) {
	if p == nil || p.Index == nil {
		return nil, nil
	}
	if d, ok := p.Index.Funcs[name]; ok {
		return d, nil
	}
	if td, ok := p.Index.Types[name]; ok && td.Decl != nil {
		return td.Decl, nil
	}
	if d, ok := p.Index.Consts[name]; ok {
		return d, nil
	}
	if d, ok := p.Index.Vars[name]; ok {
		return d, nil
	}
	return nil, nil
}

// lookupSymbol implements inspect.Symbol(pkg, name): an index hit, else
// a bound-package global as a host pseudo-decl.
func (e *Engine) lookupSymbol(p *runtime.Package, name string) (runtime.Value, error) {
	if p == nil {
		return nil, fmt.Errorf("inspect.Symbol: nil package")
	}
	if p.Index != nil {
		if d, err := e.lookupDeclIn(p, name); err != nil {
			return nil, err
		} else if d != nil {
			return &runtime.GoValue{V: xinspect.NewDecl(p, d)}, nil
		}
	}
	if v, ok := p.Globals.Get(name); ok {
		if bf, ok := v.(*runtime.BuiltinFunc); ok {
			return &runtime.GoValue{V: e.hostDecl(p, name, bf)}, nil
		}
		return &runtime.GoValue{V: xinspect.NewHostDecl(p, name, nil, reflect.TypeOf(v))}, nil
	}
	return nil, fmt.Errorf("inspect.Symbol: no %s in %s", name, p.Path)
}

// hostDecl synthesizes a Kind:"host" pseudo-decl for a bound intrinsic,
// recovering a real signature from BuiltinFunc.Target when present, and
// the owner/signature/position from BuiltinFunc.Method for host-value
// method builtins.
func (e *Engine) hostDecl(p *runtime.Package, name string, bf *runtime.BuiltinFunc) *xinspect.Decl {
	var sig *xinspect.Sig
	var target reflect.Type
	if bf != nil && bf.Method != nil {
		if p == nil {
			p = e.hostMethodPkg(bf.Method)
		}
		mt := bf.Method.Type
		if mt.Kind() == reflect.Func && mt.NumIn() > 0 {
			target = mt
			// In(0) is the receiver — the call site drops it, so the
			// sig view puts it on Recv like a script method decl.
			sig = &xinspect.Sig{
				Recv: &xinspect.Field{Type: xinspect.NewHostType(mt.In(0))},
				Params: hostFields(mt.NumIn()-1, func(i int) reflect.Type {
					return mt.In(i + 1)
				}),
				Results: hostFields(mt.NumOut(), mt.Out),
			}
		}
	} else if bf != nil && bf.Target != nil {
		if t := reflect.TypeOf(bf.Target); t != nil && t.Kind() == reflect.Func {
			target = t
			sig = &xinspect.Sig{
				Params:  hostFields(t.NumIn(), t.In),
				Results: hostFields(t.NumOut(), t.Out),
			}
		}
	}
	d := xinspect.NewHostDecl(p, name, sig, target)
	if bf != nil && bf.Method != nil {
		d.Pos = hostMethodPos(bf.Method)
	}
	return d
}

// hostFields boxes a func type's params or results as Field views
// (unnamed — host signatures carry no identifiers).
func hostFields(n int, at func(i int) reflect.Type) *runtime.Slice {
	xs := make([]runtime.Value, n)
	for i := 0; i < n; i++ {
		xs[i] = &runtime.GoValue{V: &xinspect.Field{Type: xinspect.NewHostType(at(i))}}
	}
	return &runtime.Slice{Elems: xs}
}

// hostMethodPkg recovers the package a host method was declared in from
// its receiver type's PkgPath (peeling pointer receivers).
func (e *Engine) hostMethodPkg(m *reflect.Method) *runtime.Package {
	rt := m.Type
	if rt.NumIn() == 0 {
		return nil
	}
	recv := rt.In(0)
	for recv.Kind() == reflect.Pointer {
		recv = recv.Elem()
	}
	if recv.PkgPath() == "" {
		return nil
	}
	p, err := e.loadPath(context.Background(), recv.PkgPath())
	if err != nil {
		return nil
	}
	return p
}

// hostMethodPos locates the method's definition through its Func PC —
// the bound method value's PC is a reflect thunk and can't be used.
// The column is runtime-only information a PC can't recover, so it
// stays 0.
func hostMethodPos(m *reflect.Method) *xinspect.Position {
	pc := m.Func.Pointer()
	fn := goruntime.FuncForPC(pc)
	if fn == nil {
		return nil
	}
	file, line := fn.FileLine(pc)
	if file == "" {
		return nil
	}
	return &xinspect.Position{File: file, Line: line}
}

// declsOf implements inspect.Decls(x): a package's top-level decls from
// the index, filtered to one file when given a File view; bound packages
// fall back to Globals names as Kind:"host" pseudo-decls.
func (e *Engine) declsOf(v runtime.Value) (runtime.Value, error) {
	if f, err := fileViewOf(v); err == nil {
		p := f.Pkg()
		var xs []runtime.Value
		if p != nil && p.Index != nil {
			for _, d := range p.Index.Decls {
				if d.File != f.SyntaxFile() {
					continue
				}
				xs = append(xs, &runtime.GoValue{V: xinspect.NewDecl(p, d)})
			}
		}
		return &runtime.Slice{Elems: xs}, nil
	}
	p, err := e.pkgOf(runtime.Unwrap(v))
	if err != nil {
		return nil, err
	}
	var xs []runtime.Value
	if p.Index != nil {
		for _, d := range p.Index.Decls {
			xs = append(xs, &runtime.GoValue{V: xinspect.NewDecl(p, d)})
		}
	} else {
		for _, name := range p.Globals.Names() {
			gv, _ := p.Globals.Get(name)
			if bf, ok := gv.(*runtime.BuiltinFunc); ok {
				xs = append(xs, &runtime.GoValue{V: e.hostDecl(p, name, bf)})
			} else {
				xs = append(xs, &runtime.GoValue{V: xinspect.NewHostDecl(p, name, nil, reflect.TypeOf(gv))})
			}
		}
	}
	return &runtime.Slice{Elems: xs}, nil
}

// ownerOf implements inspect.OwnerOf: value -> its declaring package.
func (e *Engine) ownerOf(v runtime.Value) (*runtime.Package, error) {
	switch x := v.(type) {
	case *runtime.Package:
		return x, nil
	case *runtime.ImportRef:
		return x.Materialize()
	case *runtime.Function:
		return x.Pkg, nil
	case *runtime.Closure:
		return x.Fn.Pkg, nil
	case *runtime.BoundMethod:
		return x.Fn.Pkg, nil
	case *runtime.TypeDef:
		return x.Pkg, nil
	case *runtime.Struct:
		return x.Def.Pkg, nil
	case *runtime.BuiltinFunc:
		if x.Pkg != nil {
			return x.Pkg, nil
		}
		if x.Method != nil {
			return e.hostMethodPkg(x.Method), nil
		}
		return nil, nil
	case *runtime.GoValue:
		if x.V != nil {
			t := reflect.TypeOf(x.V)
			// unnamed carriers (pointers, slices, maps, chans) have no
			// PkgPath themselves — walk to the named element type
			for t != nil && t.PkgPath() == "" {
				switch t.Kind() {
				case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Chan, reflect.Map:
					t = t.Elem()
				default:
					t = nil
				}
			}
			if t != nil {
				return e.loadPath(context.Background(), t.PkgPath())
			}
		}
		return nil, nil
	}
	return nil, fmt.Errorf("inspect.OwnerOf: no owner for %T", v)
}

// symbolOf implements inspect.SymbolOf: value -> decl view.
func (e *Engine) symbolOf(v runtime.Value) (*xinspect.Decl, error) {
	switch x := v.(type) {
	case *runtime.Function:
		return e.symbolOfFunc(x)
	case *runtime.Closure:
		return e.symbolOfFunc(x.Fn)
	case *runtime.BoundMethod:
		return e.symbolOfFunc(x.Fn)
	case *runtime.TypeDef:
		if x.Pkg != nil && x.Pkg.Index != nil {
			if td, ok := x.Pkg.Index.Types[x.Name]; ok && td.Decl != nil {
				return xinspect.NewDecl(x.Pkg, td.Decl), nil
			}
		}
		return nil, nil
	case *runtime.Struct:
		if x.Def == nil {
			return nil, nil
		}
		return e.symbolOf(x.Def)
	case *runtime.BuiltinFunc:
		name := x.Name
		if x.Pkg != nil && strings.HasPrefix(name, x.Pkg.Path+".") {
			name = name[len(x.Pkg.Path)+1:]
		}
		return e.hostDecl(x.Pkg, name, x), nil
	case *runtime.Package:
		return nil, fmt.Errorf("inspect.SymbolOf: %s is a package, not a symbol", x.Path)
	}
	return nil, fmt.Errorf("inspect.SymbolOf: no symbol for %T", v)
}

func (e *Engine) symbolOfFunc(fn *runtime.Function) (*xinspect.Decl, error) {
	if fn.Pkg == nil || fn.Pkg.Index == nil {
		return nil, nil
	}
	if fn.Recv != "" {
		// fn.Name is qualified ("User.Greet"); the method dict keys are
		// the short names from the index
		name := strings.TrimPrefix(fn.Name, fn.Recv+".")
		if td, ok := fn.Pkg.Index.Types[fn.Recv]; ok {
			if d, ok := td.Methods[name]; ok {
				return xinspect.NewDecl(fn.Pkg, d), nil
			}
		}
		return nil, nil
	}
	if d, ok := fn.Pkg.Index.Funcs[fn.Name]; ok {
		return xinspect.NewDecl(fn.Pkg, d), nil
	}
	return nil, nil
}
