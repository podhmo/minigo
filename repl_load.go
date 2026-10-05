package minigo

import (
	"context"
	"fmt"
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/podhmo/minigo/compile"
	"github.com/podhmo/minigo/index"
	"github.com/podhmo/minigo/resolve"
	"github.com/podhmo/minigo/runtime"
	"github.com/podhmo/minigo/syntax"
)

// loadUnit is one :load target — a single .go file or a directory's
// buildable files. Its source is kept so every reload re-parses each file
// into its own syntax.File: the decls join the repl package block (the
// prompt calls them unqualified) while each file keeps its own import
// scope, exactly like the files of one Go package. The package clause is
// ignored.
type loadUnit struct {
	origin string // absolute path given to :load (file or directory)
	files  []loadedFile
	// keys are the decl keys the files declare (var/const names
	// included); defs the func/type/method subset a prompt decl can
	// redefine.
	keys []string
	defs map[string]bool
	// shadowed names the unit's func/type/method keys redefined at the
	// prompt since the load — reload drops them from the file's AST so
	// the newer prompt definition wins. A fresh :load clears it.
	shadowed map[string]bool
}

type loadedFile struct {
	path string // absolute
	src  []byte
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
// it loaded before; a newer prompt definition of a loaded func/type wins
// until the next load. ref is a path, optionally quoted, relative to the
// engine's start directory. It returns the loaded file paths.
func (r *REPL) Load(ctx context.Context, ref string) ([]string, error) {
	if unq, err := strconv.Unquote(ref); err == nil {
		ref = unq
	}
	if ref == "" {
		return nil, fmt.Errorf("load: missing path")
	}
	origin, err := filepath.Abs(r.anchor(ref))
	if err != nil {
		return nil, fmt.Errorf("load: %w", err)
	}
	st, err := os.Stat(origin)
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
	}

	unit := &loadUnit{origin: origin, defs: map[string]bool{}, shadowed: map[string]bool{}}
	owner := map[string]string{} // decl key -> defining file
	for _, path := range paths {
		src, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("load: %w", err)
		}
		sf, err := syntax.ParseFile(token.NewFileSet(), path, src)
		if err != nil {
			return nil, fmt.Errorf("load: %w", err)
		}
		for _, k := range fileDeclKeys(sf.AST, true) {
			if prev, dup := owner[k]; dup {
				return nil, fmt.Errorf("load: %s redeclared in %s and %s", k, prev, path)
			}
			owner[k] = path
			unit.keys = append(unit.keys, k)
		}
		for _, k := range fileDeclKeys(sf.AST, false) {
			unit.defs[k] = true
		}
		unit.files = append(unit.files, loadedFile{path: path, src: src})
	}
	// a name another load already owns is a cross-file redeclaration in
	// Go terms — unload the other ref (or :reset) first
	for _, u := range r.loads {
		if u.origin == origin {
			continue
		}
		for _, k := range u.keys {
			if file, dup := owner[k]; dup {
				return nil, fmt.Errorf("load: %s in %s is already declared by :load %s", k, file, u.origin)
			}
		}
	}

	prevLoads := slices.Clone(r.loads)
	prevDecls := slices.Clone(r.decls)
	rollback := func() {
		r.loads = prevLoads
		r.decls = prevDecls
		_ = r.reload() // best effort; the original error is the one that matters
	}

	// the load is the newest definition: prompt decls of the same names go
	r.decls = slices.DeleteFunc(r.decls, func(d string) bool {
		for _, k := range promptDeclKeys(d) {
			if _, ok := owner[k]; ok {
				return true
			}
		}
		return false
	})
	if i := slices.IndexFunc(r.loads, func(u *loadUnit) bool { return u.origin == origin }); i >= 0 {
		r.loads[i] = unit
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

// loadedFiles parses every loaded file for a reload, dropping decls a
// later prompt input shadowed.
func (r *REPL) loadedFiles() ([]*syntax.File, error) {
	var out []*syntax.File
	for _, u := range r.loads {
		for _, f := range u.files {
			sf, err := syntax.ParseFile(r.engine.fset, f.path, f.src)
			if err != nil {
				return nil, fmt.Errorf("repl: reparse %s: %w", f.path, err)
			}
			if len(u.shadowed) > 0 {
				sf.AST.Decls = slices.DeleteFunc(sf.AST.Decls, func(d ast.Decl) bool {
					for _, k := range declKeys(d, false) {
						if u.shadowed[k] {
							return true
						}
					}
					return false
				})
			}
			out = append(out, sf)
		}
	}
	return out, nil
}

// loadedImportRefs builds a loaded file's import refs: directory imports anchor
// at the file's own directory and import paths resolve from it (vendoring
// and module context follow the file, not the prompt).
func (r *REPL) loadedImportRefs(sf *syntax.File) (scope map[string]*runtime.ImportRef, refs []*runtime.ImportRef) {
	dir := filepath.Dir(sf.Name)
	scope = map[string]*runtime.ImportRef{}
	for _, imp := range sf.Imports {
		ref := &runtime.ImportRef{Path: imp.Path, Alias: imp.Alias}
		if path := imp.Path; resolve.LooksLikeDir(path) {
			if !filepath.IsAbs(path) {
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

// shadowLoaded marks loaded decls redefined by the current prompt input.
func (r *REPL) shadowLoaded(d ast.Decl) {
	for _, k := range declKeys(d, false) {
		for _, u := range r.loads {
			if u.defs[k] && !u.shadowed[k] {
				u.shadowed[k] = true
				r.pendingShadow = append(r.pendingShadow, shadowMark{unit: u, key: k})
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
// var/const are hoisted cells that assign through, never redefinitions).
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
