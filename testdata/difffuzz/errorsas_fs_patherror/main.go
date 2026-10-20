package main

// errors.As into *fs.PathError on an error from the host-bound os: io/fs
// is interpreted from source, so the target's typedef is named "PathError"
// in package io/fs while the chain holds a host *fs.PathError. Identity is
// package path + name, not the "io/fs.PathError" spelling of bound types.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

func main() {
	_, err := os.Open("does-not-exist.txt")
	var pe *fs.PathError
	fmt.Println(errors.As(err, &pe), pe != nil)
	if pe != nil {
		fmt.Println(pe.Op, filepath.Base(pe.Path))
	}
	wrapped := fmt.Errorf("wrap: %w", err)
	var pe2 *fs.PathError
	fmt.Println(errors.As(wrapped, &pe2), pe2 == pe)
}
