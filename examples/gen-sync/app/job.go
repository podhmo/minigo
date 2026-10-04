package app

import (
	"github.com/podhmo/minigo/examples/gen-sync/app/internal/meta"
	"github.com/podhmo/minigo/examples/gen-sync/app/internal/mood"
	"github.com/podhmo/minigo/examples/gen-sync/scanx"

	shade "github.com/podhmo/minigo/examples/gen-sync/app/internal/envel"
)

// dependency edges for -deps scans: mood stays inside the scanned
// package's subtree, meta is visited but matches nothing, envel holds
// package shade — a dir/name mismatch the variants list must spell
// correctly — and scanx, the tool's own helper library, must never be
// followed or rewritten.
var (
	_ = mood.Happy
	_ = meta.Label{}
	_ = shade.Ghost{}
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
