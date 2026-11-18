package main

var s S

// s.M()'s result type is spelled in decl.go as q.T — q there is dep2.
var r = s.M()

func main() {}
