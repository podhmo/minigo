package minigo

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/podhmo/minigo/compile"
	"github.com/podhmo/minigo/index"
	"github.com/podhmo/minigo/resolve"
	"github.com/podhmo/minigo/runtime"
	"github.com/podhmo/minigo/syntax"
)

// loadUnit is one :load target — a single .go file or a directory's
// buildable files. Each file is parsed once into its own syntax.File: the
// decls join the repl package block (the prompt calls them unqualified)
// while each file keeps its own import scope, exactly like the files of
// one Go package. The package clause is ignored.
type loadUnit struct {
	origin string // absolute path given to :load (file or directory)
	dir    bool   // origin is a directory
	files  []loadedFile
	// keys are the decl keys the files declare: funcs and types by name,
	// methods as Recv.Name, var/const names.
	keys []string
	// cells are the var/const cells the load's initializers bound — the
	// globals this unit owns (a re-load replacing its own const is no
	// redeclaration; a re-load dropping a var deletes exactly its cell).
	cells map[string]*runtime.Cell
	// shadowed names the unit's keys redefined at the prompt since the
	// load — reload drops them from the file's AST so the newer prompt
	// definition wins. A fresh :load clears it.
	shadowed map[string]bool
}

func (u *loadUnit) declares(key string) bool { return slices.Contains(u.keys, key) }

func (u *loadUnit) hasFile(path string) bool {
	return slices.ContainsFunc(u.files, func(f loadedFile) bool { return f.path == path })
}

type loadedFile struct {
	path string       // absolute
	sf   *syntax.File // parsed once into the engine's FileSet
}

// shadowMark records a prompt input shadowing a loaded decl, so a failed
// input can un-shadow it on rollback.
type shadowMark struct {
	unit *loadUnit
	key  string
}

// Load reads a .go file or a directory's buildable files (go/build match
// rules; _test.go excluded) into the session: decls become repl globals,
// each file resolves imports in its own scope, and var/const initializers
// and init() run once per load. Loading the same ref again replaces what
// it loaded before (a directory also supersedes single-file loads of its
// files); a newer prompt definition of a loaded name wins until the next
// load. A failing load leaves the session as it was. ref is a path,
// optionally quoted, relative to the engine's start directory. It returns
// the loaded file paths.
func (r *REPL) Load(ctx context.Context, ref string) ([]string, error) {
	r.warnings = nil
	ref = unquoteRef(ref)
	if ref == "" {
		return nil, fmt.Errorf("load: missing path")
	}
	origin, err := filepath.Abs(r.anchor(ref))
	if err != nil {
		return nil, fmt.Errorf("load: %w", err)
	}
	st, err := os.Stat(origin)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("load: %s: no such file or directory (:load takes a .go file or a directory; for a package by import path use import or :cd)", ref)
	}
	if err != nil {
		return nil, fmt.Errorf("load: %w", err)
	}
	var paths []string
	if st.IsDir() {
		meta, err := resolve.ReadPackageFiles(origin, "", r.engine.cfg)
		if err != nil {
			return nil, fmt.Errorf("load: %w", err)
		}
		paths = meta.GoFiles
	} else {
		if !strings.HasSuffix(origin, ".go") {
			return nil, fmt.Errorf("load: %s is not a .go file", ref)
		}
		paths = []string{origin}
		for _, u := range r.loads {
			if u.dir && u.hasFile(origin) {
				return nil, fmt.Errorf("load: %s is part of :load %s — load the directory again to reload it", ref, u.origin)
			}
		}
	}
	// replaces reports whether u is superseded by this load: the same ref
	// again, or — loading a directory — a single-file load of one of its
	// files.
	replaces := func(u *loadUnit) bool {
		return u.origin == origin || (st.IsDir() && !u.dir && slices.Contains(paths, u.origin))
	}

	// the package's own init must have run before a unit's initializers:
	// otherwise the first reload's package init would run them too
	if r.pkg.State() != runtime.Ready {
		if err := r.reload(); err != nil {
			return nil, err
		}
	}

	unit := &loadUnit{origin: origin, dir: st.IsDir(), shadowed: map[string]bool{}}
	owner := map[string]string{}          // decl key -> defining file
	declAt := map[string]token.Position{} // decl key -> where, for duplicate reports
	for _, path := range paths {
		src, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("load: %w", err)
		}
		sf, err := syntax.ParseFile(r.engine.fset, path, src)
		if err != nil {
			return nil, fmt.Errorf("load: %w", err)
		}
		// like Go, a name declared twice in the package (same file or
		// not) rejects the load — no last-wins inside a load
		for _, d := range sf.AST.Decls {
			for _, k := range declKeys(d, true) {
				at := r.engine.fset.Position(d.Pos())
				if prev, dup := declAt[k]; dup {
					return nil, fmt.Errorf("load: %s: %s redeclared (previous declaration at %s)", at, k, prev)
				}
				declAt[k] = at
				owner[k] = path
				unit.keys = append(unit.keys, k)
			}
		}
		// imports resolve eagerly, like the prompt's
		_, refs := r.importRefs(sf, filepath.Dir(path))
		for _, ref := range refs {
			if _, err := ref.Materialize(); err != nil {
				return nil, fmt.Errorf("load: %s: import %q: %w", path, ref.Path, err)
			}
		}
		unit.files = append(unit.files, loadedFile{path: path, sf: sf})
	}
	// a name another load already owns is a cross-file redeclaration in
	// Go terms — unload the other ref (or :reset) first
	var superseded []*loadUnit
	for _, u := range r.loads {
		if replaces(u) {
			superseded = append(superseded, u)
			continue
		}
		for _, k := range u.keys {
			if file, dup := owner[k]; dup {
				return nil, fmt.Errorf("load: %s in %s is already declared by :load %s", k, file, u.origin)
			}
		}
	}

	// Snapshot everything the load may touch so a failure restores the
	// session exactly: units, prompt decls, :pin bookkeeping, and the
	// globals of every name involved. Initializers bind fresh cells, so
	// restoring the bindings restores the values.
	touched := slices.Clone(unit.keys)
	for _, u := range superseded {
		touched = append(touched, u.keys...)
	}
	saved := map[string]runtime.Value{}
	for _, k := range touched {
		if gv, ok := r.pkg.Globals.Get(k); ok {
			saved[k] = gv
		}
	}
	prevLoads := slices.Clone(r.loads)
	prevDecls := slices.Clone(r.decls)
	prevPinned := maps.Clone(r.pinnedDecls)
	rollback := func() {
		r.loads = prevLoads
		r.decls = prevDecls
		r.pinnedDecls = prevPinned
		for _, k := range touched {
			if gv, ok := saved[k]; ok {
				r.pkg.Globals.Set(k, gv)
			} else {
				r.pkg.Globals.Delete(k)
			}
		}
		_ = r.reload() // best effort; the original error is the one that matters
	}

	// a const the prompt (or another file) holds is replaced like any
	// redefinition — but say so, since `C = v` alone traps
	prevCells := map[string]*runtime.Cell{}
	for _, u := range superseded {
		maps.Copy(prevCells, u.cells)
	}
	var redecl []string
	for _, k := range unit.keys {
		if gv, ok := r.pkg.Globals.Get(k); ok {
			if c, isCell := gv.(*runtime.Cell); isCell && c.ReadOnly && prevCells[k] != c {
				redecl = append(redecl, fmt.Sprintf("const %s redeclared by %s (was %v)", k, filepath.Base(owner[k]), display(c.Elem)))
			}
		}
	}

	// The load is the newest definition of each name it declares: prompt
	// decls go, and so do prompt-hoisted cells (they would hide a loaded
	// func/type, since globals resolve before the index) and :pin
	// publications (their repl binding is never evicted otherwise).
	r.decls = slices.DeleteFunc(r.decls, func(d string) bool {
		for _, k := range promptDeclKeys(d) {
			if _, ok := owner[k]; ok {
				return true
			}
		}
		return false
	})
	for _, k := range unit.keys {
		if r.pinnedDecls[k] {
			delete(r.pinnedDecls, k)
			r.pkg.Globals.Delete(k)
		}
		if gv, ok := r.pkg.Globals.Get(k); ok {
			if _, isCell := gv.(*runtime.Cell); isCell {
				r.pkg.Globals.Delete(k)
			}
		}
	}
	// a superseded unit's cells for names this load no longer declares go
	for _, u := range superseded {
		for k, c := range u.cells {
			if gv, ok := r.pkg.Globals.Get(k); ok && gv == runtime.Value(c) && !unit.declares(k) {
				r.pkg.Globals.Delete(k)
			}
		}
	}
	if i := slices.IndexFunc(r.loads, replaces); i >= 0 {
		// take the first superseded unit's place, drop the rest
		r.loads[i] = unit
		r.loads = slices.Concat(r.loads[:i+1], slices.DeleteFunc(slices.Clone(r.loads[i+1:]), replaces))
	} else {
		r.loads = append(r.loads, unit)
	}
	if err := r.reload(); err != nil {
		rollback()
		return nil, err
	}
	if err := r.initUnit(unit); err != nil {
		rollback()
		return nil, err
	}
	unit.cells = map[string]*runtime.Cell{}
	for _, k := range unit.keys {
		if gv, ok := r.pkg.Globals.Get(k); ok {
			if c, isCell := gv.(*runtime.Cell); isCell {
				unit.cells[k] = c
			}
		}
	}
	r.warnings = redecl
	return paths, nil
}

// Loaded returns the refs (absolute paths) loaded by :load, in load order.
func (r *REPL) Loaded() []string {
	out := make([]string, len(r.loads))
	for i, u := range r.loads {
		out[i] = u.origin
	}
	return out
}

// initUnit runs the unit's var/const initializers (dependency order) and
// init() functions, compiled against the full repl index so they may use
// prompt decls and other loads.
func (r *REPL) initUnit(unit *loadUnit) error {
	p := r.pkg
	inUnit := map[*syntax.File]bool{}
	for _, f := range unit.files {
		if sf, ok := p.FileByName[f.path]; ok {
			inUnit[sf] = true
		}
	}
	view := *p.Index
	view.Decls = nil
	view.Inits = nil
	for _, d := range p.Index.Decls {
		if inUnit[d.File] && (d.Kind == index.VarDecl || d.Kind == index.ConstDecl) {
			view.Decls = append(view.Decls, d)
		}
	}
	for _, d := range p.Index.Inits {
		if inUnit[d.File] {
			view.Inits = append(view.Inits, d)
		}
	}
	if len(view.Decls) == 0 && len(view.Inits) == 0 {
		return nil
	}
	vp := &runtime.Package{
		Path:       p.Path,
		Name:       p.Name,
		Fset:       p.Fset,
		Files:      p.Files,
		FileByName: p.FileByName,
		Index:      &view,
		Globals:    p.Globals,
		Scopes:     p.Scopes,
		Imports:    p.Imports,
		Specials:   p.Specials,
	}
	ch, err := compile.InitFunc(vp)
	if err != nil {
		return err
	}
	bindCompiles(p, ch)
	return p.RunInit(&runtime.Function{Pkg: p, Name: p.Name + ".__load__", Chunk: ch})
}

// loadedFiles returns every loaded file for a reload. A unit with
// shadowed names gets a copy of its files whose AST drops those decls (a
// var/const spec goes whole if any of its names is shadowed); the parsed
// originals stay untouched.
func (r *REPL) loadedFiles() []*syntax.File {
	var out []*syntax.File
	for _, u := range r.loads {
		for _, f := range u.files {
			sf := f.sf
			if len(u.shadowed) > 0 {
				af := *sf.AST
				af.Decls = dropShadowed(sf.AST.Decls, u.shadowed)
				cp := *sf
				cp.AST = &af
				sf = &cp
			}
			out = append(out, sf)
		}
	}
	return out
}

// dropShadowed filters decls (copying any GenDecl it trims) so no
// shadowed key survives.
func dropShadowed(decls []ast.Decl, shadowed map[string]bool) []ast.Decl {
	hit := func(keys []string) bool {
		return slices.ContainsFunc(keys, func(k string) bool { return shadowed[k] })
	}
	var out []ast.Decl
	for _, d := range decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok {
			if !hit(declKeys(d, true)) {
				out = append(out, d)
			}
			continue
		}
		var specs []ast.Spec
		for _, spec := range gd.Specs {
			if !hit(declKeys(&ast.GenDecl{Tok: gd.Tok, Specs: []ast.Spec{spec}}, true)) {
				specs = append(specs, spec)
			}
		}
		switch {
		case len(specs) == len(gd.Specs):
			out = append(out, gd)
		case len(specs) > 0:
			cp := *gd
			cp.Specs = specs
			out = append(out, &cp)
		}
	}
	return out
}

// shadowLoaded marks loaded decls the current prompt decl redefines —
// funcs, types and methods, and var/const names a prompt func or type
// takes over. The unit's own cell for such a name is unbound too, or it
// would keep hiding the prompt's definition (globals resolve before the
// index); rollbackSource restores both.
func (r *REPL) shadowLoaded(d ast.Decl) {
	for _, k := range declKeys(d, false) {
		for _, u := range r.loads {
			if !u.declares(k) || u.shadowed[k] {
				continue
			}
			u.shadowed[k] = true
			r.pendingShadow = append(r.pendingShadow, shadowMark{unit: u, key: k})
			if c := u.cells[k]; c != nil {
				if gv, ok := r.pkg.Globals.Get(k); ok && gv == runtime.Value(c) {
					r.pendingUnbind = append(r.pendingUnbind, namedValue{name: k, value: gv})
					r.pkg.Globals.Delete(k)
				}
			}
		}
	}
}

func fileDeclKeys(f *ast.File, withValues bool) []string {
	var out []string
	for _, d := range f.Decls {
		out = append(out, declKeys(d, withValues)...)
	}
	return out
}

// promptDeclKeys lists the keys one accumulated prompt decl declares.
func promptDeclKeys(src string) []string {
	f, err := syntax.ParseFile(token.NewFileSet(), "repl-decl.go", []byte("package repl\n"+src))
	if err != nil {
		return nil
	}
	return fileDeclKeys(f.AST, false)
}

// declKeys names what a top-level decl declares: funcs and types by name,
// methods as Recv.Name; var/const names only when withValues (prompt
// var/const are hoisted cells, handled by hoist rather than as decls).
// init and blank names never collide.
func declKeys(d ast.Decl, withValues bool) []string {
	var out []string
	switch d := d.(type) {
	case *ast.FuncDecl:
		switch {
		case d.Recv != nil:
			if recv := index.ReceiverTypeName(d.Recv); recv != "" {
				out = append(out, recv+"."+d.Name.Name)
			}
		case d.Name.Name != "init" && d.Name.Name != "_":
			out = append(out, d.Name.Name)
		}
	case *ast.GenDecl:
		for _, spec := range d.Specs {
			switch s := spec.(type) {
			case *ast.TypeSpec:
				out = append(out, s.Name.Name)
			case *ast.ValueSpec:
				if !withValues {
					continue
				}
				for _, n := range s.Names {
					if n.Name != "_" {
						out = append(out, n.Name)
					}
				}
			}
		}
	}
	return out
}

// importRefs builds a file's import refs and its named scope. dir is the
// directory the file's imports resolve from — a loaded file's own
// directory (vendoring and module context follow the file); "" means the
// prompt, whose directory imports anchor at the engine's start directory.
func (r *REPL) importRefs(sf *syntax.File, dir string) (scope map[string]*runtime.ImportRef, refs []*runtime.ImportRef) {
	scope = map[string]*runtime.ImportRef{}
	for _, imp := range sf.Imports {
		ref := &runtime.ImportRef{Path: imp.Path, Alias: imp.Alias}
		if path := imp.Path; resolve.LooksLikeDir(path) {
			switch {
			case dir == "":
				path = r.anchor(path)
			case !filepath.IsAbs(path):
				path = filepath.Join(dir, path)
			}
			ref.Load = func(string) (*runtime.Package, error) {
				return r.engine.loadDir(context.Background(), path)
			}
		} else {
			ref.Load = func(path string) (*runtime.Package, error) {
				return r.engine.loadPathFrom(context.Background(), dir, path)
			}
		}
		refs = append(refs, ref)
		if imp.Alias != "_" && imp.Alias != "." {
			scope[imp.LocalName()] = ref
		}
	}
	return scope, refs
}
