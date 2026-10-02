package main

type D struct{}

func (d D) h() int { return 1 }

type B struct{ *D }
type A struct{ B }

func main() {
	defer func() {
		println(recover() != nil)
	}()
	var a A
	println(a.h())
}
