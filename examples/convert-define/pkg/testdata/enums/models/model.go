package models

// Status represents the status of a task.
type Status int

const (
	// ToDo is the initial state.
	ToDo Status = iota
	// InProgress is the state when the task is being worked on.
	InProgress
	// Done is the final state.
	Done
)

type Priority string

const (
	Low  Priority = "low"
	High Priority = "high"
)
