package main

import (
	"fmt"
	"io"
	"strings"
)

func kind[T any](x T) string {
	return fmt.Sprintf("%T", x)
}

func kindR[T io.Reader](x T) string {
	return fmt.Sprintf("%T", x)
}

func main() {
	r := strings.NewReader("r")
	fmt.Println(kind(r))
	fmt.Println(kindR(r))
	var a any = r
	fmt.Println(kind(a))
}
