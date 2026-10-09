package main

import "fmt"

func main() {
	a := []int{1, 2, 3}
	var u uint64 = 1<<64 - 1
	report := func(name string, f func()) {
		defer func() {
			if r := recover(); r != nil {
				fmt.Println(name, r)
			}
		}()
		f()
	}
	report("index", func() { _ = a[u] })
	report("slice", func() { _ = a[u:2] })
	s := "123"
	report("string index", func() { _ = s[u] })
	report("neg index", func() { i := -1; _ = a[i] })
}
