package main

// fs.SkipDir / fs.SkipAll come from source-interpreted io/fs, while
// filepath.WalkDir is host-bound and only knows the host sentinels; the
// interpreted fs.WalkDir compares against its own SkipDir, which a
// callback may spell filepath.SkipDir. Both must be one object.

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	fmt.Println(fs.SkipDir == filepath.SkipDir, fs.SkipAll == filepath.SkipAll)

	d, err := os.MkdirTemp("", "walkskip")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(d)
	for _, p := range []string{"a/b/c", "a/d", "e"} {
		os.MkdirAll(filepath.Join(d, p), 0o755)
	}
	for _, p := range []string{"a/b/c/f.txt", "a/d/g.txt", "a/h.txt", "e/i.txt"} {
		os.WriteFile(filepath.Join(d, p), []byte(p), 0o644)
	}
	rel := func(p string) string {
		r, _ := filepath.Rel(d, p)
		return filepath.ToSlash(r)
	}

	// host walker, script callback returning the io/fs sentinels
	var seen []string
	err = filepath.WalkDir(d, func(p string, de fs.DirEntry, err error) error {
		seen = append(seen, rel(p))
		if de.IsDir() && de.Name() == "b" {
			return fs.SkipDir
		}
		return nil
	})
	fmt.Println(strings.Join(seen, " "), err)

	seen = nil
	err = filepath.WalkDir(d, func(p string, de fs.DirEntry, err error) error {
		seen = append(seen, rel(p))
		if de.Name() == "d" {
			return fs.SkipAll
		}
		return nil
	})
	fmt.Println(strings.Join(seen, " "), err)

	// interpreted walker over os.DirFS, callback spelling the host sentinels
	fsys := os.DirFS(d)
	seen = nil
	err = fs.WalkDir(fsys, ".", func(p string, de fs.DirEntry, err error) error {
		seen = append(seen, p)
		if p == "a/b" {
			return filepath.SkipDir
		}
		return nil
	})
	fmt.Println(strings.Join(seen, " "), err)

	seen = nil
	err = fs.WalkDir(fsys, ".", func(p string, de fs.DirEntry, err error) error {
		seen = append(seen, p)
		if p == "a/d" {
			return filepath.SkipAll
		}
		return nil
	})
	fmt.Println(strings.Join(seen, " "), err)

	seen = nil
	err = fs.WalkDir(fsys, ".", func(p string, de fs.DirEntry, err error) error {
		seen = append(seen, p)
		if p == "e" {
			return fs.SkipDir
		}
		return nil
	})
	fmt.Println(strings.Join(seen, " "), err)

	// a missing root: the walker hands the callback a not-exist error
	err = fs.WalkDir(fsys, "nope", func(p string, de fs.DirEntry, err error) error {
		fmt.Println(p, de == nil, os.IsNotExist(err))
		return err
	})
	fmt.Println(err != nil)
}
