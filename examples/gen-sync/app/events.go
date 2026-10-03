package app

// Envelope is a discriminated union of events (OpenAPI oneOf style) —
// the union itself wants the generator, like its variants.
type Envelope interface {
	Discriminator() string
}

// PingEvent is an Envelope variant.
type PingEvent struct{ Seq int }

func (PingEvent) Discriminator() string { return "ping" }

// PongEvent is an Envelope variant with a pointer receiver.
type PongEvent struct{ Seq int }

func (p *PongEvent) Discriminator() string { return "pong" }

// Pulse is a oneOf-shaped type outside Envelope.
type Pulse struct{ At int64 }

func (Pulse) Discriminator() string { return "pulse" }

// Tick carries the right method NAME with the wrong signature — a
// name-only scan would emit a directive for it.
type Tick struct{}

// Discriminator reports the tick length, not a variant name.
func (Tick) Discriminator() int { return 60 }

// Discriminator is a plain function that shares the marker name; it is
// not a method, so it marks nothing.
func Discriminator() string { return "decoy" }
