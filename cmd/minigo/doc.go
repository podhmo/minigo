package main

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/podhmo/minigo"
	"github.com/podhmo/minigo/resolve"
)

// docTarget maps a :doc argument to `go doc` arguments and the directory
// to run it in. Accepted spellings:
//
//	json / json.Unmarshal        — a name bound by a session import (alias too)
//	"encoding/json".Unmarshal    — a quoted import path, as in an import spec
//	encoding/json Unmarshal      — go doc's own two-argument form
//	encoding/json.Unmarshal      — anything else is handed to go doc as is
//
// Directory packages run go doc inside the directory (`go doc . Sym`) so
// the module that owns them supplies the context.
func docTarget(r *minigo.REPL, cwd, arg string) (dir string, args []string, err error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return "", nil, fmt.Errorf("usage: :doc <pkg>[.<sym>[.<method>]]")
	}
	var pkg, sym string
	switch {
	case strings.HasPrefix(arg, `"`):
		quoted, err := strconv.QuotedPrefix(arg)
		if err != nil {
			return "", nil, fmt.Errorf("doc: bad quoted path: %w", err)
		}
		pkg, _ = strconv.Unquote(quoted)
		sym = strings.TrimLeft(arg[len(quoted):], ". ")
	case strings.Contains(arg, " "):
		pkg, sym, _ = strings.Cut(arg, " ")
		sym = strings.TrimSpace(sym)
	default:
		head, rest, _ := strings.Cut(arg, ".")
		path, ok := r.ImportPathOf(head)
		if !ok {
			return cwd, []string{arg}, nil
		}
		pkg, sym = path, rest
	}
	if path, ok := r.ImportPathOf(pkg); ok {
		pkg = path
	}
	dir = cwd
	if resolve.LooksLikeDir(pkg) {
		if !filepath.IsAbs(pkg) {
			pkg = filepath.Join(cwd, pkg)
		}
		dir, pkg = pkg, "."
	}
	args = []string{pkg}
	if sym != "" {
		args = append(args, sym)
	}
	return dir, args, nil
}

// runDoc prints `go doc` output for a :doc argument.
func runDoc(ctx context.Context, out io.Writer, r *minigo.REPL, cwd, arg string) {
	dir, args, err := docTarget(r, cwd, arg)
	if err != nil {
		fmt.Fprintf(out, "error: %s\n", err)
		return
	}
	cmd := exec.CommandContext(ctx, "go", append([]string{"doc"}, args...)...)
	cmd.Dir = dir
	b, err := cmd.CombinedOutput()
	out.Write(b)
	if err != nil && len(b) == 0 {
		fmt.Fprintf(out, "error: %s\n", err)
	}
}
