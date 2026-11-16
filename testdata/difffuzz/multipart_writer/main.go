package main

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"math/big"
	"mime/multipart"
)

// crypto/rand is bound so mime/multipart's writer side never reaches
// crypto/internal/sysrand → internal/syscall/unix (issue #1084). Only
// lengths, error booleans and nonzero checks print — the bytes are
// random.

func main() {
	fmt.Println(len(rand.Text()))
	b := make([]byte, 8)
	n, err := rand.Read(b)
	fmt.Println(n, err == nil, !allZero(b))
	n, err = io.ReadFull(rand.Reader, b)
	fmt.Println(n, err == nil)

	// rand.Int marshals a script-built math/big value through its
	// Sign/Bytes methods; rand.Prime takes a plain int.
	n1, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	fmt.Println(err == nil, n1.Sign() >= 0)
	_, err = rand.Prime(rand.Reader, 8)
	fmt.Println(err == nil)

	// io.SectionReader is a bound host type: Size dispatches and the
	// assert sees the host twin.
	sr := io.NewSectionReader(bytes.NewReader([]byte("hello")), 1, 3)
	fmt.Println(sr.Size())
	_, ok := any(sr).(*io.SectionReader)
	fmt.Println(ok)

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
	fmt.Println("boundary", len(w.Boundary()), !allZero([]byte(w.Boundary())))

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

func allZero(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}
