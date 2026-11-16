package main

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"mime/multipart"
)

// crypto/rand is bound so mime/multipart's writer side never reaches
// crypto/internal/sysrand → internal/syscall/unix (issue #1084). Only
// lengths and error booleans print — the bytes are random.

func main() {
	fmt.Println(len(rand.Text()))
	b := make([]byte, 8)
	n, err := rand.Read(b)
	fmt.Println(n, err == nil)
	n, err = io.ReadFull(rand.Reader, b)
	fmt.Println(n, err == nil)

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := w.WriteField("body", "hi"); err != nil {
		panic(err)
	}
	fw, err := w.CreateFormFile("file", "x.txt")
	if err != nil {
		panic(err)
	}
	fmt.Fprint(fw, "FILEDATA")
	if err := w.Close(); err != nil {
		panic(err)
	}
	fmt.Println("boundary", len(w.Boundary()))

	r := multipart.NewReader(&buf, w.Boundary())
	for {
		part, err := r.NextPart()
		if err != nil {
			break
		}
		data, err := io.ReadAll(part)
		fmt.Println(part.FormName(), part.FileName(), string(data), err)
		part.Close()
	}
}
