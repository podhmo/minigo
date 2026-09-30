package lazyboom

func mark() int { panic("BOOM: lazyboom initialized") }

// Touched forces an expensive init that only runs when the package is
// actually initialized (first member access).
var Touched = mark()

func Get() int { return Touched }
