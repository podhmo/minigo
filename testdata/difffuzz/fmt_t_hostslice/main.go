package main

import (
	"fmt"
	"os"
	"reflect"
)

func main() {
	// a host-bound function's unnamed-slice result keeps its element's
	// qualified spelling: gc prints []fs.DirEntry, not a bare element
	// name or interface{}.
	es, _ := os.ReadDir(".")
	fmt.Printf("%T\n", es)
	fmt.Println(reflect.TypeOf(es))
}
