package main

// Good registers before the failing initializer below — a member lookup
// must still surface the init error instead of serving partial state.
var Good = 42

var bad = func() int { panic("init went wrong") }()

func Use() int { return Good }

func main() {}
