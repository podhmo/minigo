package main

// filepath.Walk with a script callback: the fs.FileInfo it is handed
// (Name, IsDir, Mode().IsDir/IsRegular/Perm, Size), SkipDir/SkipAll
// spelled from either filepath or the source-interpreted io/fs, a
// script error stopping the walk and coming back by identity, a missing
// root (nil info), and a relative root. WalkDir shares the callback
// path: its nil DirEntry and script error identity are checked too.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type myErr struct{ code int }

func (e *myErr) Error() string { return fmt.Sprint("my ", e.code) }

func main() {
	d, err := os.MkdirTemp("", "walk")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(d)
	os.MkdirAll(filepath.Join(d, "a", "deep"), 0o755)
	os.MkdirAll(filepath.Join(d, "b"), 0o755)
	os.WriteFile(filepath.Join(d, "a", "0.txt"), []byte("0"), 0o644)
	os.WriteFile(filepath.Join(d, "a", "x.txt"), []byte("xx"), 0o644)
	os.WriteFile(filepath.Join(d, "a", "deep", "y.txt"), []byte("yyy"), 0o600)
	os.WriteFile(filepath.Join(d, "b", "z.txt"), []byte("z"), 0o644)
	os.WriteFile(filepath.Join(d, "top.txt"), []byte("t"), 0o644)
	rel := func(p string) string {
		r, _ := filepath.Rel(d, p)
		return r
	}

	fmt.Println("== all")
	err = filepath.Walk(d, func(p string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		fmt.Println(rel(p), info.Name() == filepath.Base(p), info.IsDir(), info.Mode().IsDir(), info.Mode().IsRegular(), info.Mode().Perm() == 0o600)
		if !info.IsDir() {
			fmt.Println("  size", info.Size())
		}
		return nil
	})
	fmt.Println("err:", err)

	fmt.Println("== SkipDir from filepath and fs")
	err = filepath.Walk(d, func(p string, info os.FileInfo, err error) error {
		if info.IsDir() && info.Name() == "a" {
			return filepath.SkipDir
		}
		if info.IsDir() && info.Name() == "b" {
			return fs.SkipDir
		}
		fmt.Println(rel(p))
		return nil
	})
	fmt.Println("err:", err)

	fmt.Println("== SkipDir on a file skips the rest of the dir")
	err = filepath.Walk(d, func(p string, info fs.FileInfo, err error) error {
		fmt.Println(rel(p))
		if strings.HasSuffix(p, "0.txt") {
			return filepath.SkipDir
		}
		return nil
	})
	fmt.Println("err:", err)

	fmt.Println("== SkipAll")
	err = filepath.Walk(d, func(p string, info fs.FileInfo, err error) error {
		fmt.Println(rel(p))
		if info.Name() == "deep" {
			return fs.SkipAll
		}
		return nil
	})
	fmt.Println("err:", err)

	fmt.Println("== error stops the walk")
	me := &myErr{7}
	err = filepath.Walk(d, func(p string, info fs.FileInfo, err error) error {
		if !info.IsDir() {
			return me
		}
		return nil
	})
	var got *myErr
	fmt.Println("err:", err, err == me, errors.As(err, &got), got == me)

	fmt.Println("== missing root")
	err = filepath.Walk(filepath.Join(d, "nope"), func(p string, info fs.FileInfo, err error) error {
		fmt.Println(rel(p), info == nil, os.IsNotExist(err), errors.Is(err, fs.ErrNotExist))
		return err
	})
	fmt.Println("err:", os.IsNotExist(err))

	fmt.Println("== WalkDir missing root and script error")
	err = filepath.WalkDir(filepath.Join(d, "nope"), func(p string, de fs.DirEntry, err error) error {
		fmt.Println(rel(p), de == nil, errors.Is(err, fs.ErrNotExist))
		return nil
	})
	fmt.Println("err:", err)
	err = filepath.WalkDir(d, func(p string, de fs.DirEntry, err error) error {
		if !de.IsDir() {
			return &fs.PathError{Op: "walk", Path: rel(p), Err: fs.ErrPermission}
		}
		return nil
	})
	var pe *fs.PathError
	fmt.Println("err:", err, os.IsPermission(err), errors.Is(err, fs.ErrPermission), errors.As(err, &pe))

	fmt.Println("== relative root")
	os.Chdir(d)
	err = filepath.Walk("a", func(p string, info fs.FileInfo, err error) error {
		fmt.Println(p)
		return nil
	})
	fmt.Println("err:", err)
}
