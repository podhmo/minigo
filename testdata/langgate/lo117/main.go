package main

// local declarations shadow the versioned builtins inside their scope —
// a go1.17 program may define and call its own min/new/clear freely.

func Lo() int {
	min := func(a, b int) int {
		if a < b {
			return a
		}
		return b
	}
	return min(1, 2)
}

func Pm(min func(a, b int) int) int {
	return min(1, 2)
}

func Pm2() int {
	return Pm(func(a, b int) int {
		if a > b {
			return a
		}
		return b
	})
}

func Ty() int {
	type any = int
	var x any = 5
	return x
}

func If() int {
	if new := func(v int) *int { return &v }; new != nil {
		return *new(7)
	}
	return 0
}

func Sel() int {
	clear := func(m map[string]int) {
		for k := range m {
			delete(m, k)
		}
	}
	m := map[string]int{"a": 1}
	clear(m)
	return len(m)
}

func main() {}
