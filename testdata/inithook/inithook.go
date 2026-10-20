// Package inithook holds a hook another package's init() installs — the
// shape of go/internal/scannerhooks, which go/scanner fills in.
package inithook

var End func(x any) int
