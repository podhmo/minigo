package main

import lib "example.com/lib"

func AssertStarStar() bool {
	x := lib.New()
	_, ok := x.(**lib.T)
	return ok
}

func AssertStar() bool {
	x := lib.New()
	_, ok := x.(*lib.T)
	return ok
}

func WrongElem() bool {
	x := lib.New()
	_, ok := x.(**lib.U)
	return ok
}
