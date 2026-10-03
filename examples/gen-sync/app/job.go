package app

import (
	"github.com/podhmo/minigo/examples/gen-sync/app/internal/meta"
	"github.com/podhmo/minigo/examples/gen-sync/app/internal/mood"
	"github.com/podhmo/minigo/examples/gen-sync/scanx"
)

// dependency edges for -deps scans: mood stays inside the scanned
// package's subtree, meta is visited but matches nothing, and scanx —
// the tool's own helper library — must never be followed or rewritten.
var (
	_ = mood.Happy
	_ = meta.Label{}
	_ = scanx.Sentinel
)

// Mode is a job mode.
type Mode int

const (
	ModeFast Mode = iota
	ModeSafe
)

// Default is a non-matching var declaration.
var Default = PingEvent{Seq: 0}

// Do is a non-matching func declaration.
func Do() {}

// Point is a non-matching struct (no required tags).
type Point struct{ X, Y int }
