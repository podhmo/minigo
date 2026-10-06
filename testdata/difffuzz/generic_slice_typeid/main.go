package main

import (
	"fmt"
	"reflect"
	"strconv"
)

func mapper[F, T any](s []F, f func(F) T) []T {
	r := make([]T, len(s))
	for i, v := range s {
		r[i] = f(v)
	}
	return r
}

func main() {
	got := mapper([]int{1, 2, 3}, strconv.Itoa)
	fmt.Println(reflect.DeepEqual(got, []string{"1", "2", "3"}))
}
