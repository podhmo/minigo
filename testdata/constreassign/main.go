package main

const K = 42

// AssignConst must trap: K is a read-only constant binding.
func AssignConst() int {
	K = 9
	return K
}
