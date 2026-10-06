package main

import (
	"fmt"
	"reflect"
)

// reflect.ValueOf(F).Call must accept script-declared types as args.
// $GOROOT/test/fixedbugs/issue26335.go.

type Empty struct {
	f1, f2 *byte
	empty  struct{}
}

func F(e Empty, s []string) string {
	return fmt.Sprintf("%d %s", len(s), s[0])
}

func main() {
	out := reflect.ValueOf(F).Call([]reflect.Value{
		reflect.ValueOf(Empty{}),
		reflect.ValueOf([]string{"hi"}),
	})
	fmt.Println(out[0].String())
}
