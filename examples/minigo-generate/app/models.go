package app

//minigo:generate ../tools/stringer -type=Status
//minigo:generate ../tools/enumvals -type=Status
type Status int

const (
	Unknown Status = iota
	Active
	Suspended
)

//minigo:generate ../tools/stringer -type=Priority
type Priority string

const (
	Low    Priority = "low"
	Normal Priority = "normal"
	High   Priority = "high"
)
