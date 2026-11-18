package inner

func G() [1 << 2]byte { return [4]byte{1, 2, 3, 4} }

// a shift count past the operand width is legal Go — 7 >> 64 is 0.
func H() [(7 >> 64) + 4]byte { return [4]byte{5, 6, 7, 8} }
