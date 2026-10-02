package main

type I interface{ m() int }

type S struct{ v int }

func (s S) m() int { return s.v }
func (s *S) inc()  { s.v++ }

type U struct{ I }

func main() {
	fv := S.m
	println(fv(S{5}))
	fp := (*S).inc
	s := S{1}
	fp(&s)
	println(s.v)
	fi := I.m
	println(fi(S{7}))
	fu := U.m
	println(fu(U{I: S{9}}))
}
