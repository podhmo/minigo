package main

import "os"

// Read touches the host environment — only bound in unrestricted engines.
func Read() string { return os.Getenv("PATH") }

// Exit must never terminate the host process.
func Exit() { os.Exit(3) }

func main() {}
