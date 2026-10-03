package main

func main() {
	switch 1 {
	default:
		println("default")
	case 1:
		println("one")
	}
	switch 2 {
	case 1:
		println("one")
	default:
		println("default")
	case 2:
		println("two")
	}
	switch 1 {
	case 2:
		println("two")
	default:
		println("default")
	}
	switch 1 {
	case 1:
		println("one")
		fallthrough
	default:
		println("default")
	case 3:
		println("three")
	}
	switch 9 {
	default:
		println("default")
	}
	switch 1 {
	default:
		println("default")
		fallthrough
	case 2:
		println("two")
	case 1:
		println("one")
	}
}
