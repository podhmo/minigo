// Package app is the extract-schema fixture: one file of struct
// shapes the generator must cover — scalars, tags, pointers, slices,
// maps, anonymous structs, recursion, embedding, an enum, an alias,
// and a cross-package reference.
package app

import (
	"time"

	"github.com/podhmo/minigo/examples/extract-schema/app/internal/dep"
)

// Status is the account status.
type Status string

const (
	StatusActive  Status = "active"
	StatusPaused  Status = "paused"
	StatusDeleted Status = "deleted"
)

// ID aliases string — an alias def renders as a $ref-shaped alias.
type ID = string

// User is the root fixture type.
type User struct {
	ID       ID                `json:"id"`
	Name     string            `json:"name"`
	Email    string            `json:"email,omitempty"`
	Age      int               `json:"age"`
	Admin    bool              `json:"admin"`
	Status   Status            `json:"status"`
	Balance  dep.Money         `json:"balance"`
	Active   dep.Interval      `json:"active"`
	Friends  []*User           `json:"friends,omitempty"`
	Metadata map[string]string `json:"metadata"`
	Scores   []float64         `json:"scores"`
	Address  struct {
		City    string `json:"city"`
		ZipCode string `json:"zip"`
	} `json:"address"`
	CreatedAt  time.Time `json:"created_at"`
	Secret     string    `json:"-"`
	hidden     int
	*dep.Money // untagged pointer embed — its fields promote
}

// Tree is a recursive type in the entry package.
type Tree struct {
	Label string `json:"label"`
	Left  *Tree  `json:"left,omitempty"`
	Right *Tree  `json:"right,omitempty"`
}

// Wrapper embeds a same-package struct — its fields flatten.
type Base struct {
	BaseID string `json:"base_id"`
}

type Wrapper struct {
	Base
	Note string `json:"note"`
}

// Custom is a type the jsonschema-override tag can decorate.
type Custom struct {
	Level int `json:"level" jsonschema-override:"minimum=1;maximum=5"`
}
