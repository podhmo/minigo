package minigo

import (
	"crypto/sha256"
	"encoding/binary"
	"go/ast"
	"go/token"
	"hash/fnv"
	"os"
	"sync"
	"unsafe"

	"github.com/podhmo/minigo/index"
	"github.com/podhmo/minigo/syntax"
)

// parseCacheEntry is one file's parse result, shared by every session
// forked from one base engine. sum revalidates against the bytes on
// disk on every load, so an edited file is re-parsed — a preview
// reload always sees current source.
type parseCacheEntry struct {
	sum [sha256.Size]byte
	sf  *syntax.File
	tf  *token.File // registered in the engine's shared FileSet
}

// parseCache holds parsed files keyed by path. The engine that owns a
// FileSet owns the cache: entries are parsed into it, so their Pos
// values stay valid in every session sharing that FileSet.
type parseCache struct {
	mu sync.Mutex
	m  map[string]*parseCacheEntry
}

func newParseCache() *parseCache {
	return &parseCache{m: map[string]*parseCacheEntry{}}
}

// parseFilesCached parses paths like parsePackageFiles but reuses the
// parse result of any file whose on-disk content hash is unchanged.
// lang is the File.LangMod of the loaded files; it is assigned once,
// before a file enters the shared cache, so sessions never mutate a
// shared *syntax.File.
func (e *Engine) parseFilesCached(paths []string, lang string) (files []*syntax.File, failed string, err error) {
	srcs := make([][]byte, len(paths))
	errs := make([]error, len(paths))
	each(len(paths), func(i int) {
		srcs[i], errs[i] = os.ReadFile(paths[i])
	})
	for _, err := range errs {
		if err != nil {
			// let the sequential path report it exactly as before
			files, failed, err := parseFilesSeq(e.fset, paths)
			for _, sf := range files {
				sf.LangMod = lang
			}
			return files, failed, err
		}
	}

	pc := e.pcache
	files = make([]*syntax.File, len(paths))
	sums := make([][sha256.Size]byte, len(paths))
	var miss []int
	pc.mu.Lock()
	for i, path := range paths {
		sums[i] = sha256.Sum256(srcs[i])
		// lang must match too: a file is cached under the module's
		// language version, and a changed go.mod `go` directive means
		// the stored *syntax.File's already-fixed LangMod is stale —
		// re-parse so CheckLang judges the new version.
		if ent := pc.m[path]; ent != nil && ent.sum == sums[i] && ent.sf.LangMod == lang {
			files[i] = ent.sf
		} else {
			miss = append(miss, i)
		}
	}
	pc.mu.Unlock()
	if len(miss) == 0 {
		return files, "", nil
	}

	missPaths := make([]string, len(miss))
	missSrcs := make([][]byte, len(miss))
	for j, i := range miss {
		missPaths[j] = paths[i]
		missSrcs[j] = srcs[i]
	}
	missFiles, tfs, failed, err := parseSrcs(e.fset, missPaths, missSrcs)
	if err != nil {
		return nil, failed, err
	}
	for _, mf := range missFiles {
		mf.LangMod = lang
	}

	pc.mu.Lock()
	for j, i := range miss {
		// a racing load may have published the same file first; share
		// the stored entry so concurrent packages converge on one
		// *syntax.File per path. An entry whose sum or language no
		// longer matches is stale: this load just parsed fresher
		// bytes — replace it.
		if ent := pc.m[paths[i]]; ent != nil && ent.sum == sums[i] && ent.sf.LangMod == lang {
			files[i] = ent.sf
			continue
		}
		files[i] = missFiles[j]
		if !symbolicArrayLen(missFiles[j]) {
			pc.m[paths[i]] = &parseCacheEntry{sum: sums[i], sf: missFiles[j], tf: tfs[j]}
		}
	}
	pc.mu.Unlock()
	return files, "", nil
}

// symbolicArrayLen reports whether a file contains an array type whose
// length is an expression rather than a literal or `...` — e.g. `[N]int`
// where N is a const declared in a sibling file. runtime's array-length
// fold table is keyed by the AST node, so a node shared across sessions
// would keep the value folded in the first session even after the
// const's file is edited. Such files are therefore parsed fresh on
// every load and never enter the shared cache; a literal or `...`
// length derives its value from this file alone and stays shareable.
func symbolicArrayLen(sf *syntax.File) bool {
	found := false
	ast.Inspect(sf.AST, func(n ast.Node) bool {
		if found {
			return false
		}
		if at, ok := n.(*ast.ArrayType); ok && at.Len != nil {
			switch at.Len.(type) {
			case *ast.BasicLit, *ast.Ellipsis:
			default:
				found = true
				return false
			}
		}
		return true
	})
	return found
}

// indexCacheEntry is one package index shared by sessions: the index is
// built once over a fixed set of *syntax.File and read only afterwards,
// so sessions loading the same unchanged files reuse it. Keeping files
// alive in the entry pins their addresses — a *syntax.File freed by GC
// could otherwise be reallocated at the same address and collide in fp.
type indexCacheEntry struct {
	files []*syntax.File
	ix    *index.Index
}

// indexCacheKey identifies an index build by the exact parsed file set:
// the file-pointer fingerprint changes whenever any file is re-parsed
// (an edit mints a new *syntax.File), so a reload after an edit always
// re-indexes. lenient selects between index.Build and BuildLenient.
type indexCacheKey struct {
	fp      uint64
	lenient bool
}

// indexCache shares index builds across sessions forked from one base
// engine. The map is bounded: at capacity it resets — entries are cheap
// to rebuild and steady-state reloads keep reusing one fingerprint.
type indexCache struct {
	mu sync.Mutex
	m  map[indexCacheKey]*indexCacheEntry
}

const indexCacheCap = 64

func newIndexCache() *indexCache {
	return &indexCache{m: map[indexCacheKey]*indexCacheEntry{}}
}

// indexFor returns the index for files, building it on a cache miss.
// The fingerprint hashes file identity in order: same pointers in the
// same order is the same set; one re-parsed file changes the key.
func (c *indexCache) indexFor(files []*syntax.File, lenient bool) (*index.Index, error) {
	h := fnv.New64a()
	var buf [8]byte
	for _, sf := range files {
		binary.LittleEndian.PutUint64(buf[:], uint64(uintptr(unsafe.Pointer(sf))))
		h.Write(buf[:])
	}
	key := indexCacheKey{fp: h.Sum64(), lenient: lenient}

	c.mu.Lock()
	if ent := c.m[key]; ent != nil {
		c.mu.Unlock()
		return ent.ix, nil
	}
	c.mu.Unlock()

	var ix *index.Index
	var err error
	if lenient {
		ix, err = index.BuildLenient(files)
	} else {
		ix, err = index.Build(files)
	}
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	if len(c.m) >= indexCacheCap {
		clear(c.m)
	}
	if ent := c.m[key]; ent != nil {
		ix = ent.ix // a racing build published first; converge on it
	} else {
		c.m[key] = &indexCacheEntry{files: files, ix: ix}
	}
	c.mu.Unlock()
	return ix, nil
}
