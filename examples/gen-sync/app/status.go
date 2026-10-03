package app

// Code generated directives below are managed by gen-sync. DO NOT EDIT.
//go:generate stringer -type=Status

// Status is a lifecycle state.
type Status int

const (
	StatusOpen Status = iota
	StatusClosed
)
