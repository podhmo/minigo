package main

import (
	"fmt"
	"sort"
	"time"
	"unicode"
)

// sort's typed search/sorted wrappers and time.UnixMilli/UnixMicro
// bind to their host funcs; unicode.Han was already bound.
func main() {
	fmt.Println(sort.SearchInts([]int{1, 3, 5}, 3))
	fmt.Println(sort.SearchFloat64s([]float64{1.5}, 2.0))
	fmt.Println(sort.SearchStrings([]string{"a", "b"}, "b"))
	fmt.Println(sort.IntsAreSorted([]int{1, 2}), sort.StringsAreSorted([]string{"b", "a"}), sort.Float64sAreSorted([]float64{}))
	fmt.Println(time.UnixMilli(0).UTC().Year(), time.UnixMicro(0).UTC().Year())
	fmt.Println(unicode.Is(unicode.Han, 0x6f22))
}
