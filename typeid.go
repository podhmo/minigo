package minigo

import (
	"fmt"
	"go/ast"
	"go/token"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/podhmo/minigo/runtime"
)

// universeTypeName is the set of predeclared type names — universe-block
// identifiers, so an embedded `int` or `any` is the same type in every
// package and takes no package lock.
var universeTypeName = map[string]bool{
	"bool": true, "byte": true, "rune": true, "string": true,
	"error": true, "any": true, "comparable": true,
	"complex64": true, "complex128": true,
	"float32": true, "float64": true,
	"int": true, "int8": true, "int16": true, "int32": true, "int64": true,
	"uint": true, "uint8": true, "uint16": true, "uint32": true, "uint64": true,
	"uintptr": true,
}

// typeIDOf computes the intern key for a typedef (M4-3a): the identity
// every synthesis path agrees on so a session interns one canonical
// TypeDef per type. See runtime.TypeID for the key shape; the rules
// here decide which typedefs collide:
//
//   - aliases are transparent — `type A = B` IS B, so the key peels to
//     the aliased declaration's.
//   - a source-declared type keys on its package path, the parse's
//     *syntax.File (re-parsing the same file makes a NEW type — the
//     source version), its declaration position, and any resolved type
//     arguments. Two instantiations of one generic declaration differ
//     only in their arguments.
//   - a host-represented type keys on the type it represents, not the
//     carrier box: HostIdent when set, else the proxy-mapped HostType
//     (timerChanBox and afterFuncTimer both key as *time.Timer), else
//     the carrier itself when it names a real type, else the bound
//     typedef's declared name — atomic.Value and atomic.Pointer share
//     the *atomicBox carrier but keep distinct declared names.
//   - an anonymous composite keys on its kind plus its element's key;
//     a composite the walk cannot decompose (func types, anonymous
//     structs and interfaces) keys on the canonical spelling instead.
func (e *Engine) typeIDOf(td *runtime.TypeDef) runtime.TypeID {
	td = e.peelAliasTd(td)
	if td == nil {
		return "?"
	}
	var args string
	// Spell arguments only for a named instantiation (TParams, or a
	// declared type whose TParams a copy dropped). An anonymous typedef
	// merely inherits the enclosing generic's Binds as resolution
	// context — spelling them would split `use[[]int]` inside wrap[int]
	// and wrap[string] into different identities for one type.
	if td.Binds != nil && (len(td.TParams) > 0 || td.Spec != nil) {
		// TParams gives declaration order; copies that drop it (e.g.
		// argTypedef's) still carry Binds — sort the names so an
		// instantiation always spells its arguments.
		params := td.TParams
		if len(params) == 0 {
			params = make([]string, 0, len(td.Binds))
			for n := range td.Binds {
				params = append(params, n)
			}
			slices.Sort(params)
		}
		for _, tp := range params {
			args += ";" + string(e.typeIDOf(boundTypedefOf(td.Binds[tp])))
		}
	}
	for _, ov := range td.OuterArgs {
		// a function-local generic's captured arguments are part of the
		// instantiation's identity, like gc's reflect spelling.
		args += ";" + string(e.typeIDOf(boundTypedefOf(ov)))
	}
	if td.Spec != nil {
		// source-declared: the file pointer is the source version — a
		// re-parse of the same file makes a new version of the type.
		pkg := ""
		if td.Pkg != nil {
			pkg = td.Pkg.Path
		}
		return runtime.TypeID(fmt.Sprintf("src:%s:%p:%d%s", pkg, td.File, td.Spec.Pos(), args))
	}
	if td.HostIdent != nil {
		return hostTypeID(td.HostIdent, args)
	}
	var rt0 reflect.Type
	if td.HostScalar != nil {
		rt0 = reflect.TypeOf(td.HostScalar)
	} else if td.HostNew != nil {
		rt0 = td.HostType()
	}
	if rt0 != nil {
		if rt0.Name() != "" {
			return hostTypeID(rt0, args)
		}
		// the carrier is unnamed (a *box pointer): the bound typedef's
		// declared name is the represented type's identity.
		if pkg, name, ok := splitHostName(td.Name); ok {
			return runtime.TypeID(fmt.Sprintf("host:%s:%s%s", pkg, name, args))
		}
		return hostTypeID(rt0, args)
	}
	// a *box typedef carries the whole value's carrier on its element:
	// a named host type's box IS its pointer, so the element's HostType
	// is this typedef's carrier. Proxy-map it here — *timerChanBox and
	// *afterFuncTimer both key as *time.Timer — rather than at the
	// element level, which would wrap the pointer twice (**time.Timer)
	// and collide with a real **time.Timer typedef.
	if td.Kind == runtime.KindPointer && td.Elem != nil && td.Elem.HostNew != nil {
		if ert := td.Elem.HostType(); ert != nil {
			if rt := proxyHostType(ert); rt != ert {
				return hostTypeID(rt, args)
			}
		}
	}
	// anonymous and named-basic composites: recurse through Elem when
	// the typedef carries one, else fall back to the canonical
	// underlying spelling (func signatures, anonymous structs). A
	// package-resolved anonymous descriptor keeps its spelling: a *T
	// stamped by a host value converges on a source *T (its Elem is a
	// canonical twin the spelling already names), and spelled keys are
	// context-stable in a way per-file elem keys are not.
	if td.Anon != nil && td.Pkg != nil {
		s := runtime.TypGoSpelling(td.Anon, td)
		if s != "" {
			// A leaf naming a function-local type has to spell that
			// decl's identity: the display name is bare, so two
			// functions' same-named `type L` — or one local L bound to
			// different type arguments across instantiations — would
			// collide on it. Unused locals key as before, and a member
			// or field/parameter name is never a type reference.
			sels := map[*ast.Ident]bool{}
			embedded := map[string]bool{}
			ast.Inspect(td.Anon, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.SelectorExpr:
					sels[x.Sel] = true
				case *ast.Field:
					for _, nm := range x.Names {
						sels[nm] = true
					}
				case *ast.StructType:
					if x.Fields == nil {
						break
					}
					for _, f := range x.Fields.List {
						if len(f.Names) != 0 {
							continue
						}
						// an embedded field's name is the type name as
						// written — `struct{A}` is not `struct{T}` even
						// when A is an alias for T, so the leaf keeps
						// its spelled name.
						te := ast.Expr(f.Type)
						for {
							switch w := te.(type) {
							case *ast.ParenExpr:
								te = w.X
							case *ast.StarExpr:
								te = w.X
							default:
								goto unwrapped
							}
						}
					unwrapped:
						if id, ok := te.(*ast.Ident); ok {
							embedded[id.Name] = true
						}
					}
				}
				return true
			})
			used := map[string]*runtime.TypeDef{}
			ast.Inspect(td.Anon, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok && !sels[id] {
					if lt := td.LocalTypes[id.Name]; lt != nil {
						used[id.Name] = lt
					} else if bv, ok := td.Binds[id.Name].(*runtime.TypeDef); ok && bv != nil {
						// a type parameter is transparent: `[]T` under
						// T=int IS `[]int`, so the bound typedef spells
						// the leaf itself.
						used[id.Name] = bv
					}
				}
				return true
			})
			ids := map[string]bool{}
			if len(used) > 0 {
				// Re-spell with the used leafs resolved: a local alias
				// unfolds to its target (`[]A` is `[]int`) and a declared
				// local qualifies by decl site; a declared leaf bound to
				// instantiation arguments also suffixes its own key,
				// which spells the binds.
				ctx := *td
				ctx.LocalTypes = make(map[string]*runtime.TypeDef, len(td.LocalTypes))
				for n, lt := range td.LocalTypes {
					ctx.LocalTypes[n] = lt
				}
				for n, lt := range used {
					if lt.Kind == runtime.KindAlias {
						if embedded[n] {
							if !token.IsExported(n) {
								// handled by the package-locked
								// suffix below.
								continue
							}
							// embedded alias: the name stays in the
							// spelling, and the key is the name plus
							// the shared target — two `type A = int`
							// decls name one field type.
							ids[n+"="+string(e.typeIDOf(lt))] = true
							continue
						}
						if tgt := e.peelAliasTd(lt); tgt != nil {
							ctx.LocalTypes[n] = tgt
						}
						continue
					}
					if _, bound := td.Binds[n]; td.LocalTypes[n] == nil && bound {
						// a bound type parameter substitutes its
						// argument's own spelling — no name survives.
						ctx.LocalTypes[n] = lt
						continue
					}
					cl := lt.CloneUncached()
					if lt.Spec != nil {
						cl.Name = n + "@" + strconv.FormatInt(int64(lt.Spec.Pos()), 10)
					}
					ctx.LocalTypes[n] = cl
					ids[n+"="+string(e.typeIDOf(lt))] = true
				}
				if is := runtime.TypGoSpelling(td.Anon, &ctx); is != "" {
					s = is
				}
			}
			// An unexported embedded name declared in a package is
			// locked to it: `struct{a}` from `type a = int` in one
			// package is not `struct{a}` from a same-named package at
			// another import path, so the key qualifies the name with
			// the declaring package's path — the leaf's own package
			// when the leaf resolved (a bound name locks to the
			// argument's package), td's otherwise. Universe-scope
			// names (`int`, `any`, `error`) are the same type in every
			// package and take no lock.
			for n := range embedded {
				if token.IsExported(n) {
					continue
				}
				lt := used[n]
				pkg := td.Pkg.Path
				if lt != nil && lt.Pkg != nil {
					pkg = lt.Pkg.Path
				}
				q := n + "@" + pkg
				if lt != nil {
					// name + package + target: function-local
					// `a = int` and `a = string` in the same package
					// embed different fields.
					ids[q+"="+string(e.typeIDOf(lt))] = true
					continue
				}
				if td.Pkg.Index != nil {
					if _, ok := td.Pkg.Index.Types[n]; ok {
						// the package shadows the name itself —
						// its decl is the lock, one name per package.
						ids[q] = true
						continue
					}
				}
				if universeTypeName[n] {
					continue
				}
				ids[q] = true
			}
			if len(ids) > 0 {
				leafs := make([]string, 0, len(ids))
				for k := range ids {
					leafs = append(leafs, k)
				}
				slices.Sort(leafs)
				s += "#" + strings.Join(leafs, ";")
			}
		}
		if s != "" {
			// identity spelling (keeps chan direction), not underlying.
			// Selector qualifiers already resolve to import paths through
			// the file's imports, so same-named packages do not collide.
			return runtime.TypeID(fmt.Sprintf("anon:%d:%s%s", td.Kind, s, args))
		}
	}
	if td.Elem != nil {
		return runtime.TypeID(fmt.Sprintf("anon:%d:%s%s", td.Kind, e.typeIDOf(td.Elem), args))
	}
	if td.Name != "" && td.Pkg != nil && td.Pkg.Path != "" {
		// a declared name bound under a package: identity is the owning
		// package's path plus the name — two packages can share a
		// package-name and a type-name, and the underlying spelling
		// only describes shape.
		short := td.Name
		if i := strings.LastIndex(short, "."); i >= 0 {
			short = short[i+1:]
		}
		return runtime.TypeID(fmt.Sprintf("host:%s:%s%s", td.Pkg.Path, short, args))
	}
	s := runtime.TypUnderlyingSpelling(td)
	if s == "" {
		// no spec, no carrier, no expr, no spelling: a bare placeholder
		// descriptor — unidentifiable, so it must not intern. A lone
		// declared name is still an identity ("host:pkg:name").
		if pkg, name, ok := splitHostName(td.Name); ok {
			return runtime.TypeID(fmt.Sprintf("host:%s:%s%s", pkg, name, args))
		}
		return "?"
	}
	return runtime.TypeID(fmt.Sprintf("anon:%d:%s%s", td.Kind, s, args))
}

// internTypedef returns the session's canonical descriptor for td's
// type identity (M4-3b): the first typedef stored under td's TypeID
// wins, later equivalents are dropped. Host-stamped twins resolve to
// the engine's own typedef first (canonicalTypedef), so a stamped
// fs.DirEntry IS the source declaration. Typedefs with no stable
// identity (the "?" fallback — no Spec, no host carrier, no expr)
// pass through uninterned.
func (e *Engine) internTypedef(td *runtime.TypeDef) *runtime.TypeDef {
	if td == nil || e.typeIntern == nil {
		return td
	}
	td = e.canonicalTypedef(td)
	tid := e.typeIDOf(td)
	if tid == "" || tid == "?" {
		return td
	}
	return e.typeIntern.Of(tid, td)
}

// canonicalTypedef resolves a host-stamped twin to the engine's own
// typedef for the same type (M4-4): a Spec-less td carrying a HostIdent
// becomes the source declaration or bound typedef the engine itself
// answers, and a host-minted composite (pointer, slice, …) is rebuilt
// over the canonical element so `*fs.DirEntry` stamped by host code
// interns to the same descriptor source `*fs.DirEntry` does. Anything
// else returns unchanged.
func (e *Engine) canonicalTypedef(td *runtime.TypeDef) *runtime.TypeDef {
	// A td carrying a package IS a canonical descriptor (bound at
	// Bind, or the source declaration) — no twin to resolve, and
	// short-circuiting keeps a re-entrant Bind from deadlocking the
	// lazy package install hostTwin would trigger.
	if td == nil || td.Spec != nil || td.Pkg != nil {
		return td
	}
	if td.HostIdent != nil {
		if twin := e.hostTwin(td.HostIdent); twin != nil {
			return twin
		}
		rt := td.HostIdent
		if rt.Kind() == reflect.Pointer {
			if ert := rt.Elem(); ert.Name() != "" && ert.PkgPath() != "" {
				// an unnamed host *T stamp ([hostPkgElemTypedef] via
				// [HostNilPtrTypedef]) — the descriptor stands for *T, so
				// canonical is a pointer over T's twin, keyed like a source
				// *T descriptor.
				if et := e.hostTwin(ert); et != nil {
					return runtime.AnonPtrTypedefOf(et)
				}
			}
		}
		return td
	}
	var rt0 reflect.Type
	if td.HostScalar != nil {
		rt0 = reflect.TypeOf(td.HostScalar)
	} else if td.HostNew != nil {
		rt0 = td.HostType()
	}
	if rt0 != nil {
		// carrier-backed: the typedef represents the host type its box
		// mints — *T carriers name T — but only when the descriptor
		// names that very type: a bound Widget boxing *strings.Builder
		// represents Widget, not Builder.
		if rt0.Kind() == reflect.Pointer && rt0.Name() == "" {
			rt0 = rt0.Elem()
		}
		if rt0.Name() != "" && td.Name == rt0.String() {
			if twin := e.hostTwin(rt0); twin != nil {
				return twin
			}
		}
		return td
	}
	if td.Elem != nil && td.Name == "" && td.Anon == nil {
		et := e.canonicalTypedef(td.Elem)
		if et != td.Elem {
			if td.Kind == runtime.KindPointer && et.Pkg != nil {
				return runtime.AnonPtrTypedefOf(et)
			}
			nt := td.CloneUncached()
			nt.Elem = et
			return nt
		}
	}
	return td
}

// boundTypedefOf unwraps a Binds value to its typedef (type arguments
// bind typedefs; anything else means the parameter is unbound).
func boundTypedefOf(v runtime.Value) *runtime.TypeDef {
	if td, ok := v.(*runtime.TypeDef); ok {
		return td
	}
	return nil
}

// hostTypeID keys a reflect.Type the represented-type way: named types
// by (PkgPath, Name), composites structurally (the kind numbers are
// runtime.TypeKind's).
func hostTypeID(rt reflect.Type, args string) runtime.TypeID {
	if rt == nil {
		return "?"
	}
	if rt.Name() != "" {
		return runtime.TypeID(fmt.Sprintf("host:%s:%s%s", rt.PkgPath(), rt.Name(), args))
	}
	switch rt.Kind() {
	case reflect.Pointer:
		return runtime.TypeID(fmt.Sprintf("anon:8:%s%s", hostTypeID(rt.Elem(), ""), args))
	case reflect.Slice, reflect.Array:
		return runtime.TypeID(fmt.Sprintf("anon:2:%s%s", hostTypeID(rt.Elem(), ""), args))
	case reflect.Map:
		return runtime.TypeID(fmt.Sprintf("anon:3:%s,%s%s", hostTypeID(rt.Key(), ""), hostTypeID(rt.Elem(), ""), args))
	case reflect.Chan:
		return runtime.TypeID(fmt.Sprintf("anon:7:%s%s", hostTypeID(rt.Elem(), ""), args))
	}
	return runtime.TypeID(fmt.Sprintf("anon:-1:%s%s", rt.String(), args))
}

// splitHostName splits a bound typedef's declared "pkg/path.Name" (or a
// synthesized "pkg.Name") into package path and type name.
func splitHostName(s string) (string, string, bool) {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '.' {
			return s[:i], s[i+1:], i+1 < len(s)
		}
		if s[i] == '[' {
			return "", "", false // generic spelling — args live in Binds
		}
	}
	return "", "", false
}

// anonPtrTypedefOf mints the canonical *T descriptor over an element
// twin: the same anonymous pointer a source `*T` resolves to — the
// expr, package and file are what a declared *T carries, so its intern
// key spells identically.
