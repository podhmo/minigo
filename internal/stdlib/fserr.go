package stdlib

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// scriptPathOf maps a host path back to the script's spelling: ap is
// the engine's resolved form (fsPath) of the script path sp, so ap
// itself reads back as sp verbatim and a descendant of ap joins onto sp
// (filepath.Join cleans, like the host's own joins from sp would).
// Other paths pass through.
//
// A proper ancestor of ap (MkdirAll failing on an intermediate
// component) is sp with as many trailing elements dropped — gc slices
// the spelled path — provided that prefix of sp still resolves to p.
func ScriptPathOf(sp, ap, p string) string {
	if p == ap {
		return sp
	}
	if rest, ok := cutPathPrefix(p, ap); ok {
		return filepath.Join(sp, rest)
	}
	if rest, ok := cutPathPrefix(ap, p); ok {
		t := sp
		for range strings.Split(rest, string(filepath.Separator)) {
			t = strings.TrimRight(t, string(filepath.Separator))
			i := strings.LastIndexByte(t, filepath.Separator)
			if i < 0 {
				return p
			}
			t = t[:i]
			if t == "" {
				t = string(filepath.Separator)
			}
		}
		if t = strings.TrimRight(t, string(filepath.Separator)); t == "" {
			t = string(filepath.Separator)
		}
		if resolveLike(sp, ap, t) == p {
			return t
		}
	}
	return p
}

// cutPathPrefix reports p's remainder below the directory dir.
func cutPathPrefix(p, dir string) (string, bool) {
	rest, ok := strings.CutPrefix(p, dir)
	if !ok || rest == "" {
		return "", false
	}
	if strings.HasSuffix(dir, string(filepath.Separator)) {
		return rest, true
	}
	return strings.CutPrefix(rest, string(filepath.Separator))
}

// resolveLike resolves the spelled path t the way fsPath resolved sp to
// ap — against the same base directory — or "" when the base cannot be
// recovered from the pair.
func resolveLike(sp, ap, t string) string {
	if filepath.IsAbs(t) {
		return filepath.Clean(t)
	}
	csp := filepath.Clean(sp)
	if csp == "." {
		return filepath.Join(ap, t)
	}
	base, ok := strings.CutSuffix(ap, string(filepath.Separator)+csp)
	if !ok || strings.HasPrefix(csp, "..") {
		return ""
	}
	return filepath.Join(base, t)
}

// scriptPathErr rewrites the path an os/filepath error reports from the
// host form the engine resolved (absolute, under the virtual cwd) back
// to the script's spelling — gc says `open nope: ...` for
// os.ReadFile("nope"). The host error is fresh, so it is rewritten in
// place and keeps its identity.
func ScriptPathErr(err error, sp, ap string) error {
	switch x := err.(type) {
	case *fs.PathError:
		x.Path = ScriptPathOf(sp, ap, x.Path)
	case *os.LinkError:
		x.Old = ScriptPathOf(sp, ap, x.Old)
		x.New = ScriptPathOf(sp, ap, x.New)
	}
	return err
}
