// Package dep is a fixture dependency: types here resolve across a
// package boundary when app/ fields reference them.
package dep

import "time"

// Money is an amount in minor units with its currency.
type Money struct {
	// Amount in the smallest currency unit.
	Amount int64 `json:"amount"`
	// Currency code, e.g. "JPY".
	Currency string `json:"currency"`
}

// Interval is a half-open time range — its fields are standard-library
// types, the stdlib-leaf boundary the walk is expected to stop at.
type Interval struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// Node is a recursive tree node living across the package boundary.
type Node struct {
	Value    string  `json:"value"`
	Children []*Node `json:"children,omitempty"`
}

// Tag is an enum-shaped string type.
type Tag string

const (
	TagA Tag = "a"
	TagB Tag = "b"
)

// Bin is a named slice of bytes: elements of []Bin are themselves
// base64 strings on the wire, while the outer slice stays an array.
type Bin []byte
