package main

import "fmt"

type Foo[T1, T2 any] struct {
	valueA T1
	valueB T2
}

func (f *Foo[_, _]) String() string {
	return fmt.Sprintf("%v %v", f.valueA, f.valueB)
}

func main() {
	foo := &Foo[string, int]{valueA: "i am a string", valueB: 123}
	fmt.Println(foo)
}
