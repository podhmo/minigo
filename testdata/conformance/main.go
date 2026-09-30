package main

// Conformance corpus: functions both minigo (v1 tree-walker) and minigo
// (stack VM) must evaluate identically. Keep to the shared subset.

func Add(a, b int) int { return a + b }

func Fib(n int) int {
	if n < 2 {
		return n
	}
	return Fib(n-1) + Fib(n-2)
}

func LoopSum() int {
	s := 0
	for i := 0; i < 10; i++ {
		s = s + i
	}
	return s
}

func RangeSlice() int {
	s := 0
	for _, v := range []int{1, 2, 3} {
		s = s + v
	}
	return s
}

func RangeMap() int {
	m := map[string]int{"a": 1, "b": 2}
	s := 0
	for _, v := range m {
		s = s + v
	}
	return s
}

func Closure() int {
	x := 10
	f := func() int { return x + 1 }
	return f()
}

func DeferRun() (r int) {
	defer func() { r = r + 5 }()
	return 1
}

func SwitchVal(x int) int {
	switch x {
	case 1:
		return 10
	case 2:
		return 20
	default:
		return -1
	}
}

func MultiRet() (int, int) { return 3, 4 }

func UseMulti() int {
	a, b := MultiRet()
	return a*10 + b
}

func StrCat() string {
	s := ""
	for i := 0; i < 3; i++ {
		s = s + "x"
	}
	return s
}

func main() {}

// v1's Run cannot pass arguments, so conformance cases take no args —
// these wrappers fix the inputs for arg-taking helpers.

func FibNoArg() int   { return Fib(10) }
func SwitchVal1() int { return SwitchVal(1) }
func SwitchVal2() int { return SwitchVal(2) }
func AddNoArg() int   { return Add(20, 22) }
