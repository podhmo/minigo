// run
package main

import "fmt"

// print/println spell a nil interface like Go's printeface —
// the (type,value) nil pair "(0x0,0x0)" — and a complex prints
// bare "(9+10i)", not a host-box wrapper.

func main() {
	var e interface{}
	var ni interface {
		F()
	}
	_ = ni
	fmt.Println(e == nil, ni == nil)
	println((interface{})(nil))
	println(complex(9.0, 10.0))
	println(([]int)(nil))
	println((map[int]int)(nil))
	fmt.Println("done")
}
