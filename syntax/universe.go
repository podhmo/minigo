package syntax

// IsPredeclaredType reports whether name is a predeclared type of the
// universe scope (int, error, any, comparable, ...).
func IsPredeclaredType(name string) bool {
	switch name {
	case "bool", "string",
		"int", "int8", "int16", "int32", "int64",
		"uint", "uint8", "uint16", "uint32", "uint64", "uintptr",
		"byte", "rune", "float32", "float64", "complex64", "complex128",
		"error", "any", "comparable":
		return true
	}
	return false
}

// IsPredeclaredValue reports whether name is a predeclared constant,
// the zero value nil, or a builtin function of the universe scope.
func IsPredeclaredValue(name string) bool {
	switch name {
	case "true", "false", "iota", "nil",
		"append", "cap", "clear", "close", "complex", "copy", "delete",
		"imag", "len", "make", "max", "min", "new", "panic", "print",
		"println", "real", "recover":
		return true
	}
	return false
}
