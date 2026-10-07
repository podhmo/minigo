package main

import (
	"fmt"
	"reflect"
)

// reflect.Kind as a declared type: parameters, a zero var and map keys —
// encoding/json/v2's mapKeyWithUniqueRepresentation(k reflect.Kind, ...).

func unique(k reflect.Kind) bool {
	switch k {
	case reflect.Bool, reflect.Int, reflect.String:
		return true
	}
	return false
}

func main() {
	var z reflect.Kind
	seen := map[reflect.Kind]int{}
	for _, x := range []any{1, "s", 1.5, true, []int{}} {
		k := reflect.TypeOf(x).Kind()
		seen[k]++
		fmt.Println(k, unique(k), k >= reflect.Int && k <= reflect.Int64)
	}
	fmt.Println(z, z == reflect.Invalid, len(seen))
}
