package main

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// os.Stdout is a facade that keeps the *os.File API surface while
// writes funnel to the engine's output writer.
func main() {
	fmt.Println(os.Stdout.Name())
	fmt.Println(os.Stdout.Fd())

	fi, err := os.Stdout.Stat()
	fmt.Println(err == nil)
	fmt.Println(fi.Name())

	fmt.Fprintf(os.Stdout, "via-fprintf\n")
	fmt.Fprintln(os.Stdout, "via-fprintln")
	os.Stdout.WriteString("via-writestring\n")
	os.Stdout.Write([]byte("via-write\n"))
	io.Copy(os.Stdout, strings.NewReader("via-iocopy\n"))
}
