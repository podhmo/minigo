package main

import (
	"example.com/dsl"
	"github.com/podhmo/minigo/testdata/vetstub"
)

func main() {
	dsl.Registered()   // bound host symbol -> fine
	dsl.Unregistered() // bound package, missing symbol -> not a stub finding
	vetstub.Unreg()    // unregistered stub member -> vet finding
	vetstub.Real()     // real declaration -> fine
	_ = vetstub.Value  // not a call -> fine
	_ = dsl.Cfg        // selector without call -> fine
}

func stubShadow() {
	vetstub := localStub{} // local var shadows the import alias
	vetstub.Unreg()        // method call on a local — must NOT be a finding
	vetstub.Real()
}

type localStub struct{}

func (localStub) Unreg() {}
func (localStub) Real()  {}
