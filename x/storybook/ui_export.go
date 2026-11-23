package storybook

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Export renders every story into dir: index.html plus <Story>.html and
// a copy of the static directory, so serving the export root resolves
// /static/ the same way the live preview does. A failing story still
// gets its row (with the error) so the rest of the book is written, but
// Export then reports the failures.
func (b *Book) Export(ctx context.Context, dir string, stories []Story) error {
	// The containment checks below compare against the absolute static
	// root, so the output must be absolute too — a relative -o would
	// otherwise let dst compare unequal to src and RemoveAll it.
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	dir = absDir
	// Fail fast on pathological layouts before writing anything: the
	// snapshot must neither write inside the static source (recursive
	// self-copy) nor contain it (the re-export cleanup would erase it).
	src := b.StaticDir()
	srcOK := false
	if src != "" {
		// The checks run whenever a static root is named — not only
		// when it is a directory: a `static` that is a regular file or
		// a path os.Stat cannot read must not pass the checks and reach
		// the unconditional cleanup below.
		dst := filepath.Join(dir, "static")
		// Compare link-resolved paths: a symlinked static root can
		// hide containment that the literal paths do not show. The
		// literal check still matters — a source reached through a
		// link inside the output dies with the re-export cleanup.
		realSrc, realDst := resolvePath(src), resolvePath(dst)
		inside := func(a, b string) bool { // a inside-or-equal b
			rel, err := filepath.Rel(b, a)
			return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
		}
		if inside(dst, src) || inside(realDst, realSrc) {
			return fmt.Errorf("export output must not be inside the static directory: %s", dst)
		}
		if inside(src, dst) || inside(realSrc, realDst) {
			return fmt.Errorf("static directory must not be inside the export output: %s", src)
		}
		if info, err := os.Stat(src); err == nil && info.IsDir() {
			srcOK = true
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var errs []error
	entries := make([]entry, len(stories))
	for i, s := range stories {
		entries[i] = entry{Story: s, Href: s.Name + ".html", Src: s.Name + ".html", Target: "story", Hay: haystack(s), Blurb: blurb(s)}
		if s.Group != "" {
			entries[i].Grid = "grid_" + s.Group + ".html"
		}
		res, err := b.Render(ctx, s.Name)
		if err != nil {
			entries[i].Err = firstLine(err.Error())
			errs = append(errs, err)
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, s.Name+".html"), res.Body, 0o644); err != nil {
			return err
		}
	}
	// The variants grid exports too: one page per render function whose
	// iframes point at the exported story pages.
	groups := groupEntries(entries)
	for i := range groups {
		if groups[i].Name == "" {
			continue
		}
		groups[i].Href = "grid_" + groups[i].Name + ".html"
		groups[i].Src = groups[i].Href
		groups[i].Target = "story"
		f, err := os.Create(filepath.Join(dir, groups[i].Href))
		if err != nil {
			return err
		}
		err = gridTmpl.Execute(f, map[string]any{
			"Index": "index.html",
			// The grid renders inside the story iframe; the back link
			// must leave it, not nest a second index inside the first.
			"IndexTarget": "_top",
			"Name":        groups[i].Name,
			"Stories":     gridRows(groups[i]),
		})
		f.Close()
		if err != nil {
			return err
		}
	}
	f, err := os.Create(filepath.Join(dir, "index.html"))
	if err != nil {
		return err
	}
	defer f.Close()
	// The first frameable story is the snapshot's landing page; the
	// toolbar shows it selected the way the live index does. Prefer a
	// print story when one exists, matching the live index's default.
	var sel *entry
	for i := range entries {
		if !entries[i].IsHTTP() {
			sel = &entries[i]
			break
		}
	}
	if sel == nil {
		for i := range entries {
			if entries[i].Src != "" {
				sel = &entries[i]
				break
			}
		}
	}
	excluded, _ := b.Excluded()
	data := map[string]any{"Dir": b.Dir, "Groups": groups, "Total": len(stories), "Excluded": excluded, "Widths": []string{"", "320", "360", "768"}}
	if sel != nil {
		data["S"] = sel.Name
		data["Src"] = sel.Src
		data["SelGroup"] = sel.Group
		data["SelPos"] = sel.Pos
		data["OpenHref"] = sel.Src
		if sel.Grid != "" {
			data["GridHref"] = "?g=" + sel.Group
			data["GridSrc"] = sel.Grid
		}
	}
	if err := indexTmpl.Execute(f, data); err != nil {
		return err
	}
	// The snapshot mirrors the static source: a fresh copy drops assets
	// deleted upstream since the last export, and a vanished source
	// means no static in the output either. The containment checks
	// above keep the removal safe.
	dst := filepath.Join(dir, "static")
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	if srcOK {
		if err := copyTree(src, dst); err != nil {
			return err
		}
	}
	return errors.Join(errs...)
}

// copyTree copies regular files under src into dst, preserving the
// layout. Symlinks resolve the way http.FileServer resolves them: linked
// assets land in the snapshot, and an alias naming a directory already
// copied is copied again under its own name. What is refused is a link
// cycle — a directory on its own copy path (self -> .) — tracked by the
// real paths currently on the copy stack, not by every directory seen;
// a dangling link, which the live server would answer 404, is skipped.
func copyTree(src, dst string) error {
	real, err := filepath.EvalSymlinks(src)
	if err != nil {
		return err
	}
	return copyDir(real, dst, map[string]bool{real: true})
}

func copyDir(dir, dst string, active map[string]bool) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		to := filepath.Join(dst, e.Name())
		mode := e.Type()
		if mode&os.ModeSymlink != 0 {
			target, err := filepath.EvalSymlinks(path)
			if err != nil {
				continue
			}
			info, err := os.Stat(target)
			if err != nil {
				continue
			}
			if info.IsDir() {
				if active[target] {
					continue
				}
				active[target] = true
				err := copyDir(target, to, active)
				delete(active, target)
				if err != nil {
					return err
				}
				continue
			}
			mode = info.Mode().Type()
		}
		if e.IsDir() {
			real, err := filepath.EvalSymlinks(path)
			if err != nil {
				return err
			}
			active[real] = true
			err = copyDir(real, to, active)
			delete(active, real)
			if err != nil {
				return err
			}
			continue
		}
		if !mode.IsRegular() {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.WriteFile(to, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}
