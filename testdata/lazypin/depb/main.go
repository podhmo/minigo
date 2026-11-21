package depb

// Touched panics on package init — the package must stay un-initialized
// while only its type surface is queried.
var Touched = boom()

func boom() int { panic("BOOM: depb initialized") }

type T struct{ X int }

func Get() int { return Touched }
