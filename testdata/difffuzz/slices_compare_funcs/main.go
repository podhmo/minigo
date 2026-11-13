package main

import (
	"fmt"
	"io/fs"
	"math"
	"os"
	"slices"
	"strings"
)

type Celsius float64
type IDs []int
type Rec struct {
	Name string
	Age  int
}

func try(tag string, f func()) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("%s: panic: %v (%T)\n", tag, r, r)
		}
	}()
	f()
}

func main() {
	// slices.Compare
	fmt.Println(slices.Compare([]int{1, 2}, []int{1, 3}), slices.Compare([]int{1, 2}, []int{1, 2}), slices.Compare([]int{2}, []int{1, 3}))
	fmt.Println(slices.Compare([]int{1}, []int{1, 0}), slices.Compare([]int{1, 0}, []int{1}))
	fmt.Println(slices.Compare([]string{"a", "b"}, []string{"a", "c"}), slices.Compare([]string{}, []string(nil)))
	var nilInts []int
	fmt.Println(slices.Compare(nilInts, []int{}), slices.Compare(nilInts, []int{1}), slices.Compare([]int{1}, nilInts))
	nan := math.NaN()
	fmt.Println(slices.Compare([]float64{nan}, []float64{1}), slices.Compare([]float64{1}, []float64{nan}), slices.Compare([]float64{nan}, []float64{nan}))
	fmt.Println(slices.Compare([]float64{math.Copysign(0, -1)}, []float64{0}))
	fmt.Println(slices.Compare([]Celsius{1.5, 2}, []Celsius{1.5, 3}), slices.Compare(IDs{1, 2}, IDs{1, 2, 3}))
	fmt.Println(slices.Compare([]int64{1 << 60}, []int64{1<<60 + 1}))
	fmt.Println(slices.Compare([]fs.FileMode{fs.ModeDir}, []fs.FileMode{fs.ModeDir | 1}), slices.Compare([]fs.FileMode{os.ModeDir}, []fs.FileMode{fs.ModeDir}))
	fmt.Println(slices.Compare([]byte("abc"), []byte("abd")), slices.Compare([]rune("é"), []rune("e")))

	// slices.CompareFunc
	byLen := func(a, b string) int { return len(a) - len(b) }
	fmt.Println(slices.CompareFunc([]string{"a", "bb"}, []string{"x", "yyy"}, byLen))
	fmt.Println(slices.CompareFunc([]string{"a", "bb"}, []string{"x", "yy"}, byLen), slices.CompareFunc([]string{"a"}, []string{"x", "yy"}, byLen), slices.CompareFunc([]string{"a", "bb"}, []string{"x"}, byLen))
	fmt.Println(slices.CompareFunc([]string(nil), []string{}, byLen), slices.CompareFunc([]string(nil), []string{"q"}, byLen))
	mixed := slices.CompareFunc([]int{1, 2, 3}, []string{"a", "bb", "c"}, func(a int, b string) int { return a - len(b) })
	fmt.Println(mixed)
	fmt.Println(slices.CompareFunc([]int{5}, []int{1}, func(a, b int) int { return 42 }), slices.CompareFunc([]int{5}, []int{1}, func(a, b int) int { return -7 }))
	var trace []string
	slices.CompareFunc([]string{"a", "b", "c"}, []string{"a", "x", "c"}, func(a, b string) int {
		trace = append(trace, a+b)
		return strings.Compare(a, b)
	})
	fmt.Println(trace)
	try("cmpfunc-panic", func() {
		slices.CompareFunc([]int{1}, []int{1}, func(a, b int) int { panic("boom") })
	})

	// slices.IsSortedFunc
	byAge := func(a, b Rec) int { return a.Age - b.Age }
	fmt.Println(slices.IsSortedFunc([]Rec{{"a", 1}, {"b", 2}, {"c", 2}}, byAge), slices.IsSortedFunc([]Rec{{"a", 3}, {"b", 2}}, byAge))
	fmt.Println(slices.IsSortedFunc([]Rec(nil), byAge), slices.IsSortedFunc([]Rec{{"z", 9}}, byAge))
	trace = nil
	slices.IsSortedFunc([]int{1, 2, 3}, func(a, b int) int {
		trace = append(trace, fmt.Sprint(a, b))
		return a - b
	})
	fmt.Println(trace)
	trace = nil
	fmt.Println(slices.IsSortedFunc([]int{1, 5, 3, 2}, func(a, b int) int {
		trace = append(trace, fmt.Sprint(a, b))
		return a - b
	}), trace)

	// slices.MaxFunc / MinFunc
	people := []Rec{{"ann", 30}, {"bob", 25}, {"cy", 30}, {"di", 25}}
	fmt.Println(slices.MaxFunc(people, byAge), slices.MinFunc(people, byAge))
	fmt.Println(slices.MaxFunc([]string{"bb", "a", "cc"}, byLen), slices.MinFunc([]string{"bb", "a", "c"}, byLen))
	fmt.Println(slices.MaxFunc(IDs{3, 9, 4}, func(a, b int) int { return a - b }), slices.MinFunc([]Celsius{3, 1.5}, func(a, b Celsius) int {
		if a < b {
			return -1
		}
		if a > b {
			return 1
		}
		return 0
	}))
	trace = nil
	slices.MaxFunc([]int{1, 3, 2}, func(a, b int) int {
		trace = append(trace, fmt.Sprint(a, b))
		return a - b
	})
	slices.MinFunc([]int{1, 3, 2}, func(a, b int) int {
		trace = append(trace, fmt.Sprint(a, b))
		return a - b
	})
	fmt.Println(trace)
	try("maxfunc-empty", func() { slices.MaxFunc([]int{}, func(a, b int) int { return 0 }) })
	try("minfunc-nil", func() { slices.MinFunc(nilInts, func(a, b int) int { return 0 }) })
	try("max-empty", func() { slices.Max([]int{}) })
	try("min-nil", func() { slices.Min(nilInts) })

}
