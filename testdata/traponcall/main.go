package main

// Good proves the package still compiled: functions without unsupported
// constructs run normally.
func Good() int { return 7 }

// Bad hits OpTrap at call time (3-index slice is not supported) — never at
// parse or compile time.
func Bad() int {
	s := []int{1, 2, 3}
	_ = s[0:1:2]
	return 0
}

// Channy traps on a goto to a label that is never defined: the forward
// jump is patched to a trap at compile end.
func Channy() int {
	goto missing
	return 1
}

// FallthroughAndAssert now WORK: both were OpTrap sites in earlier rounds.
func FallthroughAndAssert() int {
	n := 0
	switch 1 {
	case 1:
		n = 1
		fallthrough
	case 2:
		n += 10
	default:
		n += 100
	}
	var x any = 5
	return n + x.(int) // n=1, then +10 falls through -> 11; x assert -> 16
}

func main() {}
