package main

import "fmt"

// Blank labels may be repeated freely — gc accepts any number of `_:` in
// one function; only named labels redeclare.
func main() {
	n := 0
	goto first
_:
	n++
	goto end
first:
_:
	n += 10
	goto end
end:
	fmt.Println(n)
}
