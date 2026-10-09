package minigo

import (
	"fmt"
	"go/token"
	"os"
	goruntime "runtime"
	"sync"

	"github.com/podhmo/minigo/syntax"
)

// parsePackageFiles parses a package's files into fset concurrently,
// with the same Pos bases a sequential parse would assign. Each file is
// parsed into a private FileSet whose next base is its reserved slot,
// and the resulting token.Files join fset in file order. Any read or
// parse failure is reported for the first failing file, as the
// sequential loop did. failed names that file.
func parsePackageFiles(fset *token.FileSet, paths []string) (files []*syntax.File, failed string, err error) {
	if len(paths) < 2 {
		return parseFilesSeq(fset, paths)
	}
	srcs := make([][]byte, len(paths))
	errs := make([]error, len(paths))
	each(len(paths), func(i int) {
		srcs[i], errs[i] = os.ReadFile(paths[i])
	})
	for _, err := range errs {
		if err != nil {
			// let the sequential path report it exactly as before
			return parseFilesSeq(fset, paths)
		}
	}

	// reserve the whole range: a placeholder advances fset's base past
	// it, so concurrent AddFile calls elsewhere land after it.
	total := 0
	for _, src := range srcs {
		total += len(src) + 1
	}
	ph := fset.AddFile("", -1, total-1)
	bases := make([]int, len(paths))
	next := ph.Base()
	for i, src := range srcs {
		bases[i] = next
		next += len(src) + 1
	}

	files = make([]*syntax.File, len(paths))
	tfs := make([]*token.File, len(paths))
	each(len(paths), func(i int) {
		private := token.NewFileSet()
		if bases[i] > 1 {
			// a filler moves the private set's next base to the slot
			private.AddFile("", 1, bases[i]-2)
		}
		sf, err := syntax.ParseFile(private, paths[i], srcs[i])
		if err != nil {
			errs[i] = err
			return
		}
		// the sequential path read the file itself and kept no source
		sf.Src = nil
		files[i] = sf
		tfs[i] = private.File(sf.AST.FileStart)
	})
	fset.RemoveFile(ph)
	for i, err := range errs {
		if err != nil {
			return nil, paths[i], err
		}
	}
	for i, tf := range tfs {
		if tf == nil || tf.Base() != bases[i] {
			return nil, paths[i], fmt.Errorf("file base slot %d not taken", bases[i])
		}
	}
	fset.AddExistingFiles(tfs...)
	return files, "", nil
}

func parseFilesSeq(fset *token.FileSet, paths []string) ([]*syntax.File, string, error) {
	var files []*syntax.File
	for _, f := range paths {
		sf, err := syntax.ParseFile(fset, f, nil)
		if err != nil {
			return nil, f, err
		}
		files = append(files, sf)
	}
	return files, "", nil
}

// each runs f(0..n-1) on up to GOMAXPROCS goroutines and waits.
func each(n int, f func(i int)) {
	workers := min(n, goruntime.GOMAXPROCS(0))
	var next sync.Mutex
	i := 0
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for {
				next.Lock()
				k := i
				i++
				next.Unlock()
				if k >= n {
					return
				}
				f(k)
			}
		})
	}
	wg.Wait()
}
