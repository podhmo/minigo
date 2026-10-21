package main

// io/fs is interpreted from source while os is host-bound: its sentinel
// errors must be the host objects, or errors.Is(err, fs.ErrNotExist) on
// an os error is false. Both directions: host errors checked against
// fs.Err*, and errors io/fs builds itself checked by host os helpers.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

func main() {
	fmt.Println(fs.ErrNotExist == os.ErrNotExist, fs.ErrExist == os.ErrExist,
		fs.ErrPermission == os.ErrPermission, fs.ErrInvalid == os.ErrInvalid,
		fs.ErrClosed == os.ErrClosed)

	_, err := os.Open("does-not-exist.txt")
	fmt.Println(errors.Is(err, fs.ErrNotExist), errors.Is(err, os.ErrNotExist), os.IsNotExist(err))
	var pe *fs.PathError
	if errors.As(err, &pe) {
		fmt.Println(pe.Err == fs.ErrNotExist, errors.Is(pe, fs.ErrNotExist))
	}
	wrapped := fmt.Errorf("wrap: %w", err)
	fmt.Println(errors.Is(wrapped, fs.ErrNotExist), errors.Is(wrapped, fs.ErrExist))

	d, err := os.MkdirTemp("", "sentinel")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(d)

	err = os.Mkdir(d, 0o755)
	fmt.Println(errors.Is(err, fs.ErrExist), os.IsExist(err))

	f, err := os.Create(filepath.Join(d, "x.txt"))
	if err != nil {
		panic(err)
	}
	f.Close()
	err = f.Close()
	fmt.Println(errors.Is(err, fs.ErrClosed))

	// errors io/fs builds on its own side, checked by host-bound helpers
	fsys := os.DirFS(d)
	_, err = fs.ReadFile(fsys, "missing.txt")
	fmt.Println(errors.Is(err, fs.ErrNotExist), os.IsNotExist(err))
	_, err = fs.Sub(fsys, "../bad")
	fmt.Println(errors.Is(err, fs.ErrInvalid), errors.Is(err, os.ErrInvalid))

	// a script-built PathError around a fs sentinel
	mine := &fs.PathError{Op: "open", Path: "p", Err: fs.ErrPermission}
	fmt.Println(errors.Is(mine, os.ErrPermission), errors.Is(fmt.Errorf("w: %w", mine), fs.ErrPermission), mine)
}
