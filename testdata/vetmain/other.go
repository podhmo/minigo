package main

import vetstub "github.com/podhmo/minigo/testdata/vetstub2"

func other() {
	vetstub.Unreg2() // unregistered stub member via the real package name
}

func funclitShadow() {
	f := func(vetstub int) int { return vetstub }
	_ = f(0)
	vetstub.Unreg2() // param shadow lives in the funclit's scope only
}
