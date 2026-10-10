package main

// a field typedef kept its array length as the raw const expression —
// `bits *[maxN + maxK]int` — while new([maxN + maxK]int) folded its
// length to a literal. TypIdentical's shape compare saw different
// spellings and the pointee check trapped
// "cannot use &[316]int as *[*ast.BinaryExpr]int" where gc assigns
// the identical type. Array lengths now fold to their constant value
// before the pointee's identity compare. Found via tmpltests
// TestParseZipFS (compress/flate huffmanDecoder.bits).

import "fmt"

const maxN = 286
const maxK = 30

type holder struct {
	bits *[maxN + maxK]int
}

func main() {
	h := &holder{}
	h.bits = new([maxN + maxK]int)
	h.bits[0] = 7
	fmt.Println(len(h.bits), h.bits[0])
}
