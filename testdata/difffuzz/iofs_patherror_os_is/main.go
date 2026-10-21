package main

// io/fs is interpreted from source while os is host-bound: a
// script-built *fs.PathError must still look like one to the host
// os.IsNotExist/IsExist/IsPermission/IsTimeout, which type-switch on
// the host *fs.PathError to reach its Err. Only one layer is unwrapped
// (a %w wrap is not looked through), and a script error's own
// Timeout() method answers os.IsTimeout.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

type tmo struct{}

func (tmo) Error() string { return "tmo" }
func (tmo) Timeout() bool { return true }

func main() {
	pe := &fs.PathError{Op: "open", Path: "p", Err: fs.ErrPermission}
	fmt.Println(os.IsPermission(pe), os.IsNotExist(pe), errors.Is(pe, fs.ErrPermission))
	fmt.Println(os.IsNotExist(&fs.PathError{Op: "open", Path: "p", Err: fs.ErrNotExist}))
	fmt.Println(os.IsNotExist(&fs.PathError{Op: "open", Path: "p", Err: os.ErrNotExist}))
	fmt.Println(os.IsExist(&fs.PathError{Op: "mkdir", Path: "p", Err: fs.ErrExist}))
	fmt.Println(os.IsTimeout(&fs.PathError{Op: "read", Path: "p", Err: os.ErrDeadlineExceeded}))
	fmt.Println(os.IsTimeout(tmo{}), os.IsTimeout(&fs.PathError{Op: "read", Path: "p", Err: tmo{}}))
	fmt.Println(os.IsNotExist(nil), os.IsNotExist(errors.New("x")))
	var nilPE *fs.PathError
	fmt.Println(os.IsNotExist(&fs.PathError{Op: "o", Path: "p", Err: nilPE}))

	// %w is not looked through by os.Is*, but errors.Is/As are.
	wrapped := fmt.Errorf("w: %w", pe)
	fmt.Println(os.IsPermission(wrapped), errors.Is(wrapped, os.ErrPermission))
	var pe2 *fs.PathError
	fmt.Println(errors.As(wrapped, &pe2), pe2 == pe)

	// a host os error is a *fs.PathError to errors.As.
	_, err := os.Open("/nonexistent/minigo-iofs-patherror")
	var pe3 *fs.PathError
	fmt.Println(errors.As(err, &pe3), os.IsNotExist(err))
	if pe3 != nil {
		fmt.Println(pe3.Op, pe3.Path, pe3.Err)
	}
	fmt.Println(pe.Error())
	fmt.Printf("%T\n", error(pe))
}
