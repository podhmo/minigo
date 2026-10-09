package main

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"unicode"
)

func find(s string) (idx int, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("recovered: %v", r)
		}
	}()
	return strings.IndexFunc(s, func(r rune) bool {
		if r == '!' {
			panic("bang")
		}
		return unicode.IsDigit(r)
	}), nil
}

func main() {
	fmt.Println(find("abc3"))
	fmt.Println(find("ab!3"))

	// nested: a callback that itself calls a SyncCallbacks builtin
	words := []string{"pear", "fig", "apple", "kiwi"}
	sort.SliceStable(words, func(i, j int) bool {
		return strings.IndexFunc(words[i], unicode.IsUpper) < 0 && len(words[i]) < len(words[j])
	})
	fmt.Println(words)

	xs := []int{1, 3, 5, 7, 9, 11}
	fmt.Println(sort.Search(len(xs), func(i int) bool { return xs[i] >= 7 }))

	// callbacks on script goroutines (child VMs)
	var wg sync.WaitGroup
	res := make([]int, 8)
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res[g] = sort.Search(100, func(i int) bool { return i*i >= g*10 })
		}()
	}
	wg.Wait()
	fmt.Println(res)
	fmt.Println(strings.Map(func(r rune) rune {
		if r == 'a' {
			return -1
		}
		return unicode.ToUpper(r)
	}, "banana"), strings.TrimFunc("xxhixx", func(r rune) bool { return r == 'x' }))
}
