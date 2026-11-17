package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

func main() {
	// a host-built PathError (from os.Open) matches *os.PathError: gc
	// aliases os.PathError = fs.PathError, and minigo's alias must let
	// the script-declared target see the host error.
	_, err := os.Open("testdata/difffuzz/os_err_family/nope.txt")
	var pe *os.PathError
	fmt.Println("as pe:", errors.As(err, &pe))
	if pe != nil {
		fmt.Println("op/path:", pe.Op, pe.Path)
	}
	// *fs.PathError spells the same type through the alias.
	var fe *fs.PathError
	fmt.Println("as fe:", errors.As(err, &fe))

	// a script-built PathError feeds the host predicates like gc's
	// `type PathError = fs.PathError` does.
	synth := &os.PathError{Op: "open", Path: "p", Err: os.ErrNotExist}
	fmt.Println("isnotexist:", os.IsNotExist(synth))
	fmt.Println("is:", errors.Is(synth, os.ErrNotExist))
	var pe2 *os.PathError
	fmt.Println("as synth:", errors.As(synth, &pe2))

	// NewSyscallError + As on the os.SyscallError typedef.
	se := os.NewSyscallError("mmap", os.ErrInvalid)
	var serr *os.SyscallError
	fmt.Println("as se:", errors.As(se, &serr))
	if serr != nil {
		fmt.Println("syscall:", serr.Syscall)
	}
	fmt.Println("is invalid:", errors.Is(se, os.ErrInvalid))

	// os.Link's failure is a *os.LinkError the script can decompose.
	lerr := os.Link("testdata/difffuzz/os_err_family/nope.txt", "testdata/difffuzz/os_err_family/x")
	var le *os.LinkError
	fmt.Println("as le:", errors.As(lerr, &le))
	if le != nil {
		fmt.Println("link op/old/new:", le.Op, le.Old, le.New)
	}
	fmt.Println("is notexist:", errors.Is(lerr, os.ErrNotExist))

	// a script-built LinkError reaches the host predicates: the boxed
	// Err field must land on the host struct as fs.ErrExist.
	sle := &os.LinkError{Op: "link", Old: "a", New: "b", Err: os.ErrExist}
	fmt.Println("isexist sle:", os.IsExist(sle))
	fmt.Println("sle.Error:", sle.Error())

	// a script-defined error keeps its methods through the host
	// boundary: NewSyscallError wraps it and os.IsTimeout calls back
	// into the script's pointer-receiver Timeout.
	myErr := &timeoutErr{msg: "dial timed out"}
	se2 := os.NewSyscallError("dial", myErr)
	fmt.Println("se2:", se2.Error())
	fmt.Println("timeout se2:", os.IsTimeout(se2))
	fmt.Println("is myErr:", errors.Is(se2, myErr))
	fmt.Println("timeout plain:", os.IsTimeout(myErr))
	fmt.Println("timeout named:", os.IsTimeout(namedTimeout(true)))
}

type timeoutErr struct{ msg string }

func (e *timeoutErr) Error() string   { return e.msg }
func (e *timeoutErr) Timeout() bool   { return true }
func (e *timeoutErr) Temporary() bool { return false }

type namedTimeout bool

func (n namedTimeout) Error() string { return "named timeout" }
func (n namedTimeout) Timeout() bool { return bool(n) }
