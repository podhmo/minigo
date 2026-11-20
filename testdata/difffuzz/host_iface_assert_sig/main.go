package main

import (
	"errors"
	"fmt"
)

func main() {
	var a error = fmt.Errorf("one %w", errors.New("x"))
	_, one := a.(interface{ Unwrap() error })
	_, oneSlice := a.(interface{ Unwrap() []error })
	fmt.Println(one, oneSlice) // true false

	var b error = fmt.Errorf("two %w %w", errors.New("x"), errors.New("y"))
	_, two := b.(interface{ Unwrap() error })
	_, twoSlice := b.(interface{ Unwrap() []error })
	fmt.Println(two, twoSlice) // false true

	_, errm := a.(interface{ Error() string })
	fmt.Println(errm) // true
}
