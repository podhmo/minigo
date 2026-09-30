package inittable

var Table = map[string]int{}

func init() {
	Table["x"] = 5 // func-init mutation, not a var initializer
}

func Lookup() int { return Table["x"] }
