package main

import (
	"bytes"
	"fmt"
	"io"
	"strings"
)

type panicReader struct{}

func (panicReader) Read(p []byte) (int, error) { panic("boom in Read") }

type panicWriter struct{}

func (panicWriter) Write(p []byte) (int, error) { panic("boom in Write") }

type panicWriteTo struct{}

func (panicWriteTo) Read(p []byte) (int, error) { return 0, nil }
func (panicWriteTo) WriteTo(w io.Writer) (int64, error) {
	panic("boom in WriteTo")
}

func main() {
	func() {
		defer func() { fmt.Println("R:", recover()) }()
		io.Copy(io.Discard, panicReader{})
	}()
	func() {
		defer func() { fmt.Println("W:", recover()) }()
		fmt.Fprint(panicWriter{}, "x")
	}()
	func() {
		defer func() { fmt.Println("WT:", recover()) }()
		var buf bytes.Buffer
		io.Copy(&buf, panicWriteTo{})
	}()
	n, err := io.Copy(io.Discard, strings.NewReader("ab"))
	fmt.Println(n, err)
}
