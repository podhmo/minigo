package main

// Sibling in the same directory: dir-mode loading sees only this file
// (defs.go is excluded by its //go:build codegen constraint), while
// LoadFile sees only defs.go.

func main() {}

func OtherOnly() int { return 99 }
