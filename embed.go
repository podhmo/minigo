package minigo

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/podhmo/minigo/compile"
	"github.com/podhmo/minigo/runtime"
)

// embedBuiltin implements `//go:embed` (compile.EmbedBuiltin): args are
// the var's declared type then the directive's patterns, resolved against
// the calling package's directory like cmd/go's resolveEmbed. A string or
// []byte var takes the single matched file; an embed.FS gets every file
// plus the directories leading to them, in embed's (dir, elem) order.
func embedBuiltin(e *Engine, v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
	if len(args) < 2 {
		return nil, fmt.Errorf("%s: no patterns", compile.EmbedBuiltin)
	}
	td, ok := args[0].(*runtime.TypeDef)
	if !ok {
		return nil, fmt.Errorf("go:embed: %T is not a type", args[0])
	}
	pkg := v.Package()
	if pkg == nil || pkg.Dir == "" {
		return nil, fmt.Errorf("go:embed: no package directory")
	}
	var pats []string
	for _, a := range args[1:] {
		s, ok := a.(string)
		if !ok {
			return nil, fmt.Errorf("go:embed: pattern %T is not a string", a)
		}
		pats = append(pats, s)
	}
	files, err := embedFiles(pkg.Dir, pats)
	if err != nil {
		return nil, err
	}
	if fsTd := embedFSType(e, td); fsTd != nil {
		return embedFS(e, v, fsTd, pkg.Dir, files)
	}
	// string / []byte (or a named type over them): one file, converted
	// through the declared type.
	if len(files) != 1 {
		return nil, fmt.Errorf("go:embed: invalid pattern syntax: multiple files for type %s", td.Name)
	}
	abs := filepath.Join(pkg.Dir, filepath.FromSlash(files[0]))
	if err := e.cfg.CheckPath(abs); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("go:embed: %w", err)
	}
	return v.Convert(td, string(data))
}

// embedFSType reports td when it is embed.FS (by its declaring package) —
// `type FS = embed.FS` aliases resolve to the same typedef first.
func embedFSType(e *Engine, td *runtime.TypeDef) *runtime.TypeDef {
	if e != nil {
		td = e.peelAliasTd(td)
	}
	if td != nil && td.Name == "FS" && td.Pkg != nil && td.Pkg.Path == "embed" {
		return td
	}
	return nil
}

// embedFiles resolves patterns to slash-separated file paths relative to
// dir. A matched directory contributes its files recursively, skipping
// names starting with '.' or '_' unless the pattern carries `all:`, and
// never descending into a nested module (a directory with go.mod).
func embedFiles(dir string, pats []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	add := func(rel string) {
		if !seen[rel] {
			seen[rel] = true
			out = append(out, rel)
		}
	}
	for _, pat := range pats {
		all := false
		if p, ok := strings.CutPrefix(pat, "all:"); ok {
			pat, all = p, true
		}
		if _, err := path.Match(pat, ""); err != nil || pat == "" || path.IsAbs(pat) {
			return nil, fmt.Errorf("go:embed: invalid pattern syntax: %s", pat)
		}
		// "." and ".." are invalid as path ELEMENTS — names that merely
		// contain the dots (a..b) are legal.
		for _, el := range strings.Split(pat, "/") {
			if el == "" || el == "." || el == ".." {
				return nil, fmt.Errorf("go:embed: invalid pattern syntax: %s", pat)
			}
		}
		matches, err := filepath.Glob(filepath.Join(dir, filepath.FromSlash(pat)))
		if err != nil {
			return nil, fmt.Errorf("go:embed: %w", err)
		}
		// embed may never reach outside the package directory: a glob
		// match travels through symlinked dirs, so verify the resolved
		// real path stays inside the (real) package dir — and a symlink
		// matched directly is an irregular file, like cmd/go reports.
		root, err := filepath.EvalSymlinks(dir)
		if err != nil {
			return nil, fmt.Errorf("go:embed: %w", err)
		}
		n := 0
		for _, m := range matches {
			info, err := os.Lstat(m)
			if err != nil {
				return nil, fmt.Errorf("go:embed: %w", err)
			}
			rel, err := filepath.Rel(dir, m)
			if err != nil {
				return nil, err
			}
			if !info.IsDir() && !info.Mode().IsRegular() {
				return nil, fmt.Errorf("go:embed: pattern %s: cannot embed irregular file %s", pat, filepath.ToSlash(rel))
			}
			if real, err := filepath.EvalSymlinks(m); err != nil || (real != root && !strings.HasPrefix(real, root+string(filepath.Separator))) {
				return nil, fmt.Errorf("go:embed: pattern %s: cannot embed file %s: outside the package directory", pat, filepath.ToSlash(rel))
			}
			if !info.IsDir() {
				add(filepath.ToSlash(rel))
				n++
				continue
			}
			err = filepath.WalkDir(m, func(p string, d os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if p != m {
					base := d.Name()
					if !all && (strings.HasPrefix(base, ".") || strings.HasPrefix(base, "_")) {
						if d.IsDir() {
							return filepath.SkipDir
						}
						return nil
					}
					if d.IsDir() {
						if _, err := os.Stat(filepath.Join(p, "go.mod")); err == nil {
							return filepath.SkipDir
						}
						return nil
					}
				}
				if d.IsDir() || !d.Type().IsRegular() {
					return nil
				}
				r, err := filepath.Rel(dir, p)
				if err != nil {
					return err
				}
				add(filepath.ToSlash(r))
				n++
				return nil
			})
			if err != nil {
				return nil, fmt.Errorf("go:embed: %w", err)
			}
		}
		if n == 0 {
			return nil, fmt.Errorf("go:embed: pattern %s: no matching files found", pat)
		}
	}
	return out, nil
}

// embedSplit mirrors embed's split: "dir/elem" or "dir/elem/" (a
// directory), with a missing dir taken to be ".".
func embedSplit(name string) (dir, elem string) {
	name = strings.TrimSuffix(name, "/")
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		return name[:i], name[i+1:]
	}
	return ".", name
}

// embedFS builds the embed.FS value the compiler would emit: the struct's
// `files *[]file` holds every file and every directory leading to one
// (named "dir/"), sorted by (dir, elem) so embed's binary search works.
func embedFS(e *Engine, v runtime.VMCaller, td *runtime.TypeDef, dir string, files []string) (runtime.Value, error) {
	names := map[string]bool{}
	for _, f := range files {
		names[f] = true
		for d := path.Dir(f); d != "."; d = path.Dir(d) {
			names[d+"/"] = true
		}
	}
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Slice(sorted, func(i, j int) bool {
		di, ei := embedSplit(sorted[i])
		dj, ej := embedSplit(sorted[j])
		if di != dj {
			return di < dj
		}
		return ei < ej
	})

	fsv, ok := v.Zero(td).(*runtime.Struct)
	if !ok {
		return nil, fmt.Errorf("go:embed: embed.FS zero is not a struct")
	}
	fi := fieldIndex(fsv.Def, "files")
	if fi < 0 {
		return nil, fmt.Errorf("go:embed: embed.FS has no files field")
	}
	// *[]file -> []file -> file, read off the zero values so the element
	// typedef is embed's own unexported file type.
	ptrNil, ok := fsv.Fields[fi].(*runtime.TypedNil)
	if !ok || ptrNil.Typ == nil {
		return nil, fmt.Errorf("go:embed: unexpected embed.FS.files zero %T", fsv.Fields[fi])
	}
	sliceTd := v.TypeOf(v.ElemZero(ptrNil.Typ))
	if sliceTd == nil {
		return nil, fmt.Errorf("go:embed: cannot resolve embed.FS.files element type")
	}
	fileZero := v.ElemZero(sliceTd)
	elems := make([]runtime.Value, 0, len(sorted))
	for _, n := range sorted {
		fv, ok := v.Copy(fileZero).(*runtime.Struct)
		if !ok {
			return nil, fmt.Errorf("go:embed: embed file zero is %T", fileZero)
		}
		data := ""
		if !strings.HasSuffix(n, "/") {
			abs := filepath.Join(dir, filepath.FromSlash(n))
			if err := e.cfg.CheckPath(abs); err != nil {
				return nil, err
			}
			b, err := os.ReadFile(abs)
			if err != nil {
				return nil, fmt.Errorf("go:embed: %w", err)
			}
			data = string(b)
		}
		if i := fieldIndex(fv.Def, "name"); i >= 0 {
			fv.Fields[i] = n
		}
		if i := fieldIndex(fv.Def, "data"); i >= 0 {
			fv.Fields[i] = data
		}
		elems = append(elems, fv)
	}
	fsv.Fields[fi] = &runtime.Cell{Elem: &runtime.Slice{Elems: elems, Typ: sliceTd}, Typ: sliceTd}
	return fsv, nil
}

func fieldIndex(def *runtime.TypeDef, name string) int {
	if def == nil {
		return -1
	}
	for i, f := range def.Fields {
		if f == name {
			return i
		}
	}
	return -1
}
