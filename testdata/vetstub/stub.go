// Package vetstub models a stub package: members whose bodies are the
// intrinsic panic marker, meant to be intercepted by a special form or a
// bound host symbol at interpretation time.
package vetstub

func Unreg() { panic("minigo intrinsic") }

func Real() int { return 1 }

var Value = 7
