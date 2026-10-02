package main

import "fmt"

type Sink struct {
	out []byte
}

func (s *Sink) Write(p []byte) (int, error) {
	s.out = append(s.out, p...)
	return len(p), nil
}

type ErrSink struct{}

func (s *ErrSink) Write(p []byte) (int, error) {
	return 0, fmt.Errorf("nope %d", len(p))
}

func main() {
	s := &Sink{}
	n, err := fmt.Fprintf(s, "hello %d", 42)
	fmt.Println(n, err, string(s.out))

	e := &ErrSink{}
	n2, err2 := fmt.Fprintf(e, "abc")
	fmt.Println(n2, err2)

	fmt.Fprint(s, "xyz")
	fmt.Fprintln(s, "!")
	fmt.Println(string(s.out))

}
