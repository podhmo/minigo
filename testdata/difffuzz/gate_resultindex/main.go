package main

import (
	"fmt"

	inner "github.com/podhmo/minigo/testdata/difffuzz/gate_resultindex/inner"
)

func onlyInt[T ~int](v T) {
	fmt.Println(v)
}

func main() {
	// rows is []int — the real index into the call result, not a
	// synthetic result-index marker.
	rows := inner.MakeRows()[0]
	for _, v := range rows {
		onlyInt(v)
	}
}
