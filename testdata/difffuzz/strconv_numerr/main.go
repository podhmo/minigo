package main

import (
	"errors"
	"fmt"
	"strconv"
)

func main() {
	_, err := strconv.Atoi("x")
	fmt.Println("Is syntax:", errors.Is(err, strconv.ErrSyntax)) // true (NumError.Unwrap)
	fmt.Println("Is range:", errors.Is(err, strconv.ErrRange))   // false
	fmt.Println("eq syntax:", err == strconv.ErrSyntax)          // false (boxed *NumError)
	_, err2 := strconv.ParseInt("99999999999999999999", 10, 64)
	fmt.Println("Is range:", errors.Is(err2, strconv.ErrRange))               // true
	fmt.Println("Is syntax (range err):", errors.Is(err2, strconv.ErrSyntax)) // false

	var ne *strconv.NumError
	fmt.Println("As:", errors.As(err, &ne))
	fmt.Println("fields:", ne.Func, ne.Num, ne.Err == strconv.ErrSyntax)
	fmt.Println("Unwrap:", errors.Unwrap(err) == strconv.ErrSyntax)

	var e error
	fmt.Println("As iface:", errors.As(err, &e), e != nil)

	var other *strconv.NumError
	ok := errors.As(err, &other) && other.Error() == "strconv.Atoi: parsing \"x\": invalid syntax"
	fmt.Println("As Error():", ok)

	// wrapped chain: Is/As reach through %w
	werr := fmt.Errorf("outer: %w", err)
	fmt.Println("Is wrapped:", errors.Is(werr, strconv.ErrSyntax))
	var wne *strconv.NumError
	fmt.Println("As wrapped:", errors.As(werr, &wne), wne.Num)
}
