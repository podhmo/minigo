package main

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
)

// FileHeader.Open returns sectionReadCloser — a script struct embedding
// the host *io.SectionReader — as multipart.File. Interface satisfaction
// misses the methods promoted from the host embed (TODO.md "a script
// struct embedding a host pointer does not satisfy interfaces through
// promoted methods"), so the return traps `cannot use ... as File`.

func main() {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	w.WriteField("body", "hi")
	fw, _ := w.CreateFormFile("file", "x.txt")
	fmt.Fprint(fw, "FILEDATA")
	w.Close()
	fmt.Println("boundary", len(w.Boundary()))

	r := multipart.NewReader(&buf, w.Boundary())
	form, err := r.ReadForm(1 << 20)
	if err != nil {
		panic(err)
	}
	fmt.Println("body", form.Value["body"])
	fh := form.File["file"][0]
	fmt.Println("size", fh.Size)
	f, err := fh.Open()
	if err != nil {
		panic(err)
	}
	data, err := io.ReadAll(f)
	fmt.Println("file", fh.Filename, string(data), err)
	f.Close()
}
