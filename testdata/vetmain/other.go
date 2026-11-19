package main

import vetstub "github.com/podhmo/minigo/testdata/vetstub2"

func other() {
	vetstub.Unreg2() // unregistered stub member via the real package name
}
