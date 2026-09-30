package main

const (
	A = iota + 1
	B
	C
)

var Global = 10

type Counter struct {
	n int
}

func (c *Counter) Inc() { c.n++ }

func (c *Counter) Get() int { return c.n }

func fib(n int) int {
	if n < 2 {
		return n
	}
	return fib(n-1) + fib(n-2)
}

func swap(a, b int) (int, int) {
	return b, a
}

func makeCounter() func() int {
	x := 0
	return func() int {
		x++
		return x
	}
}

func Answer() int {
	return fib(10) + Global + B
}

func SumRange() int {
	s := []int{1, 2, 3, 4}
	total := 0
	for i, v := range s {
		total += i + v
	}
	return total
}

func Switchy(x int) int {
	switch x {
	case 1, 2:
		return 10
	case 3:
		return 30
	default:
		return -1
	}
}

func Methods() int {
	c := &Counter{n: 5}
	c.Inc()
	c.Inc()
	return c.Get()
}

func Closure() int {
	next := makeCounter()
	next()
	next()
	return next()
}

func Multi() int {
	a, b := swap(3, 4)
	return a*10 + b
}

func Mapy() int {
	m := map[string]int{"a": 1}
	m["b"] = 2
	total := 0
	for _, v := range m {
		total += v
	}
	return total + len(m)
}

func Consts() int {
	return A + B + C
}

func Strings() string {
	s := "hello"
	for i := 0; i < 2; i++ {
		s += "!"
	}
	return s + string(33)
}

func main() {
	println(Answer())
}
