package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// options controls detection and output filtering.
type options struct {
	includeUntested bool
	exclude         []*regexp.Regexp
}

// stats reports scan cost for -verbose.
type stats struct {
	modules int
	files   int
	edges   int
	elapsed time.Duration
}

// detection is the outcome of one run: the affected packages after
// filtering plus diagnostics.
type detection struct {
	graph    *graph
	root     string
	packages []*pkg
	dropped  []*pkg // affected but filtered out (kept for -verbose)
	warnings []string
	stats    stats
}

// detectChanged resolves changed .go files to packages, walks reverse
// dependencies, and applies output filters.
func detectChanged(root string, changed []string, o options) (*detection, error) {
	start := time.Now()
	g, err := scanRepo(root)
	if err != nil {
		return nil, err
	}
	d := &detection{graph: g, root: root, warnings: g.warnings}
	d.stats.modules = len(g.modules)
	for _, p := range g.byDir {
		d.stats.files += len(p.files)
	}
	for _, parents := range g.rev {
		d.stats.edges += len(parents)
	}

	seeds := d.resolveChanged(changed)
	affected := bfs(g, seeds)
	d.packages, d.dropped = filterAffected(affected, o)
	d.stats.elapsed = time.Since(start)
	return d, nil
}

// resolveChanged maps changed file paths to their package nodes.
// Non-.go inputs are ignored; .go files whose directory is not in the
// graph (deleted trees, testdata, files outside the modules) produce a
// warning — a silently dropped file would silently drop test coverage.
// Inputs that clearly are not files at all (directories, Windows-style
// `\` separators pointing at nothing) warn too: they claim a package
// the tool cannot see.
func (d *detection) resolveChanged(changed []string) []*pkg {
	var seeds []*pkg
	seen := map[*pkg]bool{}
	for _, f := range changed {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		abs := f
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(d.root, f)
		}
		abs = filepath.Clean(abs)
		info, statErr := os.Stat(abs)
		if statErr == nil && info.IsDir() {
			d.warnings = append(d.warnings, fmt.Sprintf("%s: is a directory, not a .go file (skipped)", f))
			continue
		}
		// A '\' in a path that does not exist is almost certainly a
		// Windows separator; a real file may legitimately contain one.
		if statErr != nil && filepath.Separator != '\\' && strings.ContainsRune(f, '\\') {
			d.warnings = append(d.warnings, fmt.Sprintf("%s: contains '\\'; pass '/'-separated paths on this platform (skipped)", f))
			continue
		}
		if !strings.HasSuffix(f, ".go") {
			continue
		}
		p, ok := d.graph.byDir[filepath.Dir(abs)]
		if !ok {
			d.warnings = append(d.warnings, fmt.Sprintf("%s: %s (skipped)", f, d.whyNotInGraph(abs)))
			continue
		}
		if !seen[p] {
			seen[p] = true
			seeds = append(seeds, p)
		}
	}
	return seeds
}

// whyNotInGraph explains why an input's directory produced no package
// node: the same failure has different fixes depending on whether the
// path escaped -root, the directory is gone, or the walk skipped it.
func (d *detection) whyNotInGraph(abs string) string {
	if abs != d.root && !strings.HasPrefix(abs, d.root+string(filepath.Separator)) {
		return "outside -root"
	}
	dir := filepath.Dir(abs)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return fmt.Sprintf("directory %s does not exist", relDisplay(d.root, dir))
	}
	// The directory exists but produced no package node: either the walk
	// skipped it by convention or it holds no .go files.
	rel := relDisplay(d.root, dir)
	for _, seg := range strings.Split(rel, string(filepath.Separator)) {
		if seg != "." && seg != "" && skipDir(seg) {
			return fmt.Sprintf("directory %s is skipped by the walk", rel)
		}
	}
	if entries, err := os.ReadDir(dir); err == nil {
		hasGo := false
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".go") {
				hasGo = true
				break
			}
		}
		if !hasGo {
			return fmt.Sprintf("directory %s has no .go files", rel)
		}
	}
	return "not in scanned graph"
}

// relDisplay renders dir relative to root for messages, keeping the
// absolute path when it does not lie underneath.
func relDisplay(root, dir string) string {
	if rel, err := filepath.Rel(root, dir); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return rel
	}
	return dir
}

// bfs walks reverse-dependency edges from the seed packages and returns
// every affected package. Traversal passes through untested packages —
// a changed leaf still reaches tested dependents through them.
func bfs(g *graph, seeds []*pkg) []*pkg {
	seen := map[*pkg]bool{}
	var queue []*pkg
	for _, s := range seeds {
		if !seen[s] {
			seen[s] = true
			queue = append(queue, s)
		}
	}
	var out []*pkg
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		out = append(out, p)
		for _, parent := range g.rev[p.importPath] {
			if !seen[parent] {
				seen[parent] = true
				queue = append(queue, parent)
			}
		}
	}
	return out
}

// filterAffected drops untested packages (unless includeUntested) and
// excluded import paths. Filtering is output-only: it happens after the
// BFS, so excluded packages still propagate impact to their dependents.
func filterAffected(affected []*pkg, o options) (kept, dropped []*pkg) {
	for _, p := range affected {
		switch {
		case !o.includeUntested && !p.hasTests:
			dropped = append(dropped, p)
		case excluded(o.exclude, p.importPath):
			dropped = append(dropped, p)
		default:
			kept = append(kept, p)
		}
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].importPath < kept[j].importPath })
	sort.Slice(dropped, func(i, j int) bool { return dropped[i].importPath < dropped[j].importPath })
	return kept, dropped
}

func excluded(res []*regexp.Regexp, path string) bool {
	for _, re := range res {
		if re.MatchString(path) {
			return true
		}
	}
	return false
}
