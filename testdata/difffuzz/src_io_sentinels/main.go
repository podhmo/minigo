package main

// Under --src io the interpreted io's sentinels are the host objects:
// host-bound readers and writers (strings.Reader, bufio) return the host
// io.EOF / io.ErrShortWrite, and the interpreted io helpers return
// sentinels the script compares against the same names.

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
)

type short struct{}

func (short) Write(p []byte) (int, error) { return 0, nil }

func main() {
	_, err := strings.NewReader("").Read(make([]byte, 1))
	fmt.Println(err == io.EOF)
	_, err = io.ReadFull(strings.NewReader("a"), make([]byte, 4))
	fmt.Println(err == io.ErrUnexpectedEOF)
	_, err = bufio.NewReader(strings.NewReader("")).ReadByte()
	fmt.Println(err == io.EOF, errors.Is(err, io.EOF))
	bw := bufio.NewWriter(short{})
	bw.WriteString("x")
	fmt.Println(bw.Flush() == io.ErrShortWrite)
	b, err := io.ReadAll(strings.NewReader("abc"))
	fmt.Println(string(b), err)
}
