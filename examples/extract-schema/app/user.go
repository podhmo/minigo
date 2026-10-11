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

// Pair declares two names in one field spec — both become properties.
type Pair struct {
	A, B string
	C, D int `json:",omitempty"`
}

// Shadow exercises the shallower-wins rule: its explicit base_id field
// must shadow the one promoted from Base, not be overwritten by it.
type Shadow struct {
	Base
	Note string `json:"base_id"`
}

// Commented covers the comment positions a description can come from:
// a doc comment above the field and a line comment at its end.
type Commented struct {
	// Lead is described by the comment above it.
	Lead int `json:"lead"`
	// Both is described twice — the doc comment wins in inspect's
	// fieldDoc order.
	Both  int `json:"both"`  // trailing comment, second to the doc
	Trail int `json:"trail"` // Trail is described by the trailing comment only.
	Bare  int `json:"bare"`
	// Lids keeps a word that merely shares the field name's prefix —
	// a naive strip would leave "s keeps...".
	Lid int `json:"lid"`
}

// DeepX promotes X one level further inside an embed chain.
type DeepX struct{ X string }
type ViaDeepX struct{ DeepX }
type ShallowX struct{ X int }

// DepthClash gets X from both ViaDeepX (depth 2) and ShallowX
// (depth 1): the strictly shallower promotion wins — an int.
type DepthClash struct {
	ViaDeepX
	ShallowX
}

type LeftY struct{ Y string }
type RightY struct{ Y int }

// Ambig promotes Y from both embeds at the same depth: encoding/json
// drops *both*, so the schema must not pick a winner.
type Ambig struct {
	LeftY
	RightY
}

// Byte is a named uint8: encoding/json marshals []Byte as base64, the
// same wire shape as []byte.
type Byte byte

// Byte2 adds one hop to the named-byte chase.
type Byte2 Byte

// ByteM is a named byte whose pointer marshals itself: gc escapes the
// []byte base64 shortcut when the element (or its pointer) implements
// json.Marshaler — []ByteM is an array of marshaled elements.
type ByteM byte

func (b *ByteM) MarshalJSON() ([]byte, error) { return []byte(`"m"`), nil }

// ByteT marshals elements through encoding.TextMarshaler — the same
// base64 escape as MarshalJSON.
type ByteT byte

func (b ByteT) MarshalText() ([]byte, error) { return []byte("t"), nil }

// Byte2M chains to a marshaling type — `type Byte2M ByteM` has no
// methods of its own, so []Byte2M keeps the base64 shape.
type Byte2M ByteM

// Blobbed covers the byte-slice spellings: builtin and named byte
// elements marshal as base64, a named slice-of-bytes element stays an
// array of base64 strings, and a fixed array keeps the array shape.
type Blobbed struct {
	Data      []Byte      `json:"data"`
	Data2     []Byte2     `json:"data2"`
	Raw       []byte      `json:"raw"`
	Fixed     [4]byte     `json:"fixed"`
	Subs      []dep.Bin   `json:"subs"`
	Moneys    []dep.Money `json:"moneys"`
	Marshaled []ByteM     `json:"marshaled"`
	Texted    []ByteT     `json:"texted"`
	Chained   []Byte2M    `json:"chained"`
}
