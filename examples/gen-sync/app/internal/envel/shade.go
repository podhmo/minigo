// Package shade lives in a directory that does not match its package
// name — qualifying its types by directory base (envel) would print
// the wrong package name.
package shade

// Ghost implements app's Envelope from a package whose name differs
// from its directory: the variants list must spell it shade.Ghost.
type Ghost struct{ At int64 }

func (Ghost) Discriminator() string { return "ghost" }
