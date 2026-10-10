package main

import (
	"fmt"
	"reflect"
	"time"
)

// the liveness-tracking boxes NewTimer/NewTicker hand out spell their
// API type in %T and reflect — never the box's own name.
func main() {
	tk := time.NewTicker(time.Hour)
	defer tk.Stop()
	tm := time.NewTimer(time.Hour)
	defer tm.Stop()
	fmt.Printf("%T\n", tk)
	fmt.Printf("%T\n", tm)
	fmt.Println(reflect.TypeOf(tk).String())
	fmt.Println(reflect.TypeOf(tk).Elem())
	fmt.Println(reflect.TypeOf(tm).String())
}
