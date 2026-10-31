package main

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
)

type T struct{ n int }

func main() {
	enc := json.NewEncoder(os.Stdout)
	fmt.Printf("%T\n", enc)
	var any1 any = enc
	_, ok := any1.(*json.Encoder)
	fmt.Println(ok)
	t := &T{1}
	c := runtime.AddCleanup(t, func(int) {}, 0)
	fmt.Printf("%T\n", c)
	c.Stop()
	fmt.Println(fmt.Sprintf("%T %T", enc.Encode, enc.SetIndent))
}
