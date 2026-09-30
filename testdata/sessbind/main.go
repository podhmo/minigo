package main

import "myhost/lib"

// Main calls a host-bound symbol that only exists via Engine.Bind —
// sessions must inherit the binding to resolve the import.
func Main() int {
	return lib.Magic()
}

func main() {}
