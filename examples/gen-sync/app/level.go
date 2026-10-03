package app

// Code generated directives below are managed by gen-sync. DO NOT EDIT.
//go:generate stringer -type=Priority

// Level is a log level.
type Level string

const (
	LevelDebug Level = "debug"
	LevelInfo  Level = "info"
)

/*
History: this file used to generate the Priority type instead. The old
wiring read

	//go:generate stringer -type=OldPriority

Quoting a directive inside a block comment must not resurrect it.
*/

// oldDirectives keeps the original managed block as a string literal —
// text that merely LOOKS like the sentinel + directives must not open a
// managed region here.
const oldDirectives = `
// Code generated directives below are managed by gen-sync. DO NOT EDIT.
//go:generate stringer -type=OldPriority
`
