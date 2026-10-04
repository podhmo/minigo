package main

func Loop() int {
	n := 0
	for i := range 10 {
		n += i
	}
	return n
}

func main() {}
