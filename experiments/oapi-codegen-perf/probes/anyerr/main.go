package main

import (
	"errors"
	"fmt"
)

func kind[T any](x T) string { return fmt.Sprintf("%T", x) }

func main() {
	vals := []any{1, errors.New("e")}
	for _, v := range vals {
		fmt.Println(kind(v))
	}
	for _, v := range []any{errors.New("e")} {
		fmt.Println(kind(v))
	}
}
