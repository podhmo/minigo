package main

import . "strings"

// DotMe calls TrimSpace, which arrives through the dot import — no
// declaration in this package claims it.
func DotMe(s string) string { return TrimSpace(s) }

func main() { _ = DotMe(" x ") }
