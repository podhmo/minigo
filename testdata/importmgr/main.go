package main

// importmgr exercises the minigo.dev/importmanager intrinsic: scripts
// generating code share the host alias rules (keyword escape, collision
// numbering, Qualify).
import "minigo.dev/importmanager"

func AliasRun() string {
	im := importmanager.New("example.com/m/gen")
	a := im.Add("example.com/m/foo", "")
	b := im.Add("example.com/m/bar", "foo")
	return a + "|" + b + "|" + im.Qualify("example.com/m/baz", "T")
}

func main() {}
