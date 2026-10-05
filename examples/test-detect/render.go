package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// moduleGroup is one discovered module and its affected packages; the
// JSON output shape for CI loops that run `go -C <module_dir> test`.
type moduleGroup struct {
	ModuleDir  string   `json:"module_dir"`
	ModulePath string   `json:"module_path"`
	Packages   []string `json:"packages"`
}

// render formats the kept packages. Formats:
//
//	pkg   import paths, one per line (default)
//	space import paths, space-separated on one line (for go test $(...))
//	dir   repo-relative directories, one per line
//	json  [{module_dir, module_path, packages}] grouped per module
func (d *detection) render(format string) ([]byte, error) {
	paths := make([]string, 0, len(d.packages))
	for _, p := range d.packages {
		paths = append(paths, p.importPath)
	}
	switch format {
	case "pkg":
		if len(paths) == 0 {
			return nil, nil
		}
		return []byte(strings.Join(paths, "\n") + "\n"), nil
	case "space":
		return []byte(strings.Join(paths, " ") + "\n"), nil
	case "dir":
		var dirs []string
		for _, p := range d.packages {
			rel, err := filepath.Rel(d.root, p.dir)
			if err != nil {
				return nil, err
			}
			dirs = append(dirs, rel)
		}
		if len(dirs) == 0 {
			return nil, nil
		}
		return []byte(strings.Join(dirs, "\n") + "\n"), nil
	case "json":
		groups := d.groupByModule()
		return json.Marshal(groups)
	default:
		return nil, fmt.Errorf("unknown -format %q (want pkg|space|dir|json)", format)
	}
}

// groupByModule buckets the kept packages under their owning module,
// with module dirs relative to the scan root.
func (d *detection) groupByModule() []moduleGroup {
	byModule := map[string]*moduleGroup{}
	var order []string
	for _, m := range d.graph.modules {
		rel, err := filepath.Rel(d.root, m.dir)
		if err != nil {
			rel = m.dir
		}
		byModule[m.dir] = &moduleGroup{ModuleDir: rel, ModulePath: m.path}
		order = append(order, m.dir)
	}
	for _, p := range d.packages {
		g := byModule[p.moduleDir]
		g.Packages = append(g.Packages, p.importPath)
	}
	var out []moduleGroup
	sort.Strings(order)
	for _, dir := range order {
		g := byModule[dir]
		if len(g.Packages) > 0 {
			out = append(out, *g)
		}
	}
	return out
}
