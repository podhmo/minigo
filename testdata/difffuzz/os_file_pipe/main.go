package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
)

func main() {
	var w io.Writer = &bytes.Buffer{}
	_, isFile := w.(*os.File)
	fmt.Println(isFile)
	r, pw, err := os.Pipe()
	if err != nil {
		panic(err)
	}
	var wf io.Writer = pw
	_, isFile = wf.(*os.File)
	fmt.Println(isFile)
	fmt.Fprint(pw, "through the pipe")
	pw.Close()
	b, _ := io.ReadAll(r)
	r.Close()
	fmt.Println(string(b))
	fmt.Println(os.Interrupt, os.Kill, errors.Is(os.ErrProcessDone, os.ErrProcessDone))
}
